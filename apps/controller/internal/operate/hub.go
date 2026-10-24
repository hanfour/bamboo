// SPDX-License-Identifier: AGPL-3.0-or-later

// Package operate matches an admin desk request to the device that
// already holds a stream. It does not dial the mesh.
package operate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Frame kinds carried between the admin desk and a device.
const (
	KindMsg       = "msg"
	KindPut       = "put"
	KindGet       = "get"
	KindPTYStart  = "pty-start"
	KindPTYData   = "pty-data"
	KindPTYResize = "pty-resize"
	KindPTYClose  = "pty-close"
)

const (
	outCap = 16
	ptyCap = 256
)

// ErrOffline means the device has no live Operate stream.
var ErrOffline = errors.New("device offline")

// ErrExecOff means the device connected with the terminal disabled.
var ErrExecOff = errors.New("exec disabled")

// ErrBusy means the device is not reading frames quickly enough.
var ErrBusy = errors.New("device busy")

// ErrNoSession means the device is connected and has no open terminal.
var ErrNoSession = errors.New("no terminal session")

// Frame is one admin operation for a device.
type Frame struct {
	OpID string
	Kind string
	From string
	Text string
	Path string
	Data []byte
	Argv []string
	Rows int
	Cols int
}

// Result is the device's answer to a Frame that waits.
type Result struct {
	OpID     string
	OK       bool
	Error    string
	ExitCode int
	Data     []byte
}

// PTYSub is the terminal byte stream for one shell.
// Bytes is never closed. Finished closes when that shell exits or
// the device disconnects. A slow reader drops bytes rather than
// blocking the device.
type PTYSub struct {
	OpID     string
	Bytes    <-chan []byte
	Finished <-chan struct{}
}

// Hub is the in-memory desk router. The zero value is not usable;
// call NewHub.
type Hub struct {
	mu      sync.Mutex
	links   map[string]*link
	waiters map[string]map[string]chan Result
	pty     map[string]*ptySess
}

type link struct {
	exec bool
	out  chan Frame
	done chan struct{}
	once sync.Once
}

func (l *link) close() {
	l.once.Do(func() { close(l.done) })
}

type ptySess struct {
	opID     string
	bytes    chan []byte
	finished chan struct{}
	once     sync.Once
}

func (s *ptySess) finish() {
	s.once.Do(func() { close(s.finished) })
}

// NewHub returns an empty desk router.
func NewHub() *Hub {
	return &Hub{
		links:   map[string]*link{},
		waiters: map[string]map[string]chan Result{},
		pty:     map[string]*ptySess{},
	}
}

// Attach registers the device stream. The returned channel carries
// admin frames. done closes when this attachment is replaced or
// detached. detach releases the registration. A later Attach for the
// same peer closes the previous done channel.
func (h *Hub) Attach(peerID string, exec bool) (frames <-chan Frame, done <-chan struct{}, detach func()) {
	h.mu.Lock()
	if old := h.links[peerID]; old != nil {
		h.dropLinkLocked(peerID, old)
	}
	l := &link{
		exec: exec,
		out:  make(chan Frame, outCap),
		done: make(chan struct{}),
	}
	h.links[peerID] = l
	h.mu.Unlock()
	return l.out, l.done, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		cur := h.links[peerID]
		if cur != l {
			return
		}
		h.dropLinkLocked(peerID, l)
	}
}

func (h *Hub) dropLinkLocked(peerID string, l *link) {
	delete(h.links, peerID)
	l.close()
	delete(h.waiters, peerID)
	h.finishPTYLocked(peerID, "")
}

// Call sends f and waits for the matching result. A pty-start registers
// the terminal buffer before the frame is sent, so output cannot arrive
// before a subscriber can see it.
func (h *Hub) Call(ctx context.Context, peerID string, f Frame) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if f.OpID == "" {
		f.OpID = newID()
	}
	h.mu.Lock()
	l := h.links[peerID]
	if l == nil {
		h.mu.Unlock()
		return Result{}, ErrOffline
	}
	if f.Kind == KindPTYStart && !l.exec {
		h.mu.Unlock()
		return Result{}, ErrExecOff
	}
	if f.Kind == KindPTYStart {
		h.replacePTYLocked(peerID, f.OpID)
	}
	ch := make(chan Result, 1)
	if h.waiters[peerID] == nil {
		h.waiters[peerID] = map[string]chan Result{}
	}
	h.waiters[peerID][f.OpID] = ch
	out, done := l.out, l.done
	h.mu.Unlock()

	sent := false
	select {
	case out <- f:
		sent = true
	case <-done:
	case <-ctx.Done():
	default:
	}
	if !sent {
		h.forget(peerID, f.OpID)
		if f.Kind == KindPTYStart {
			h.mu.Lock()
			if s := h.pty[peerID]; s != nil && s.opID == f.OpID {
				s.finish()
				delete(h.pty, peerID)
			}
			h.mu.Unlock()
		}
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		h.mu.Lock()
		live := h.links[peerID]
		h.mu.Unlock()
		if live == nil {
			return Result{}, ErrOffline
		}
		select {
		case <-done:
			return Result{}, ErrOffline
		default:
		}
		return Result{}, ErrBusy
	}

	select {
	case res := <-ch:
		if !res.OK && res.Error == ErrOffline.Error() {
			return Result{}, ErrOffline
		}
		if res.OpID == "" {
			res.OpID = f.OpID
		}
		return res, nil
	case <-done:
		h.forget(peerID, f.OpID)
		return Result{}, ErrOffline
	case <-ctx.Done():
		h.forget(peerID, f.OpID)
		return Result{}, ctx.Err()
	}
}

// Push sends a frame that has no result, such as terminal input.
func (h *Hub) Push(ctx context.Context, peerID string, f Frame) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	l := h.links[peerID]
	if l == nil {
		h.mu.Unlock()
		return ErrOffline
	}
	out, done := l.out, l.done
	h.mu.Unlock()
	select {
	case out <- f:
		return nil
	case <-done:
		return ErrOffline
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrBusy
	}
}

// Result delivers a device answer. A result with no waiter is the
// shell's exit and finishes that terminal only.
func (h *Hub) Result(peerID string, res Result) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ws := h.waiters[peerID]; ws != nil {
		if ch, ok := ws[res.OpID]; ok {
			delete(ws, res.OpID)
			select {
			case ch <- res:
			default:
			}
			return
		}
	}
	h.finishPTYLocked(peerID, res.OpID)
}

// Output forwards terminal bytes for opID. Bytes for a different shell
// are ignored. A full buffer drops the chunk.
func (h *Hub) Output(peerID, opID string, data []byte) {
	if len(data) == 0 || opID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.pty[peerID]
	if s == nil || s.opID != opID {
		return
	}
	chunk := append([]byte(nil), data...)
	select {
	case s.bytes <- chunk:
	default:
	}
}

// Subscribe returns the current terminal, if one has been started.
func (h *Hub) Subscribe(peerID string) (PTYSub, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.pty[peerID]
	if s == nil {
		return PTYSub{}, false
	}
	return PTYSub{OpID: s.opID, Bytes: s.bytes, Finished: s.finished}, true
}

// ExecEnabled reports whether peerID is connected and whether it
// accepted a terminal.
func (h *Hub) ExecEnabled(peerID string) (enabled, online bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.links[peerID]
	if l == nil {
		return false, false
	}
	return l.exec, true
}

func (h *Hub) replacePTYLocked(peerID, opID string) {
	if old := h.pty[peerID]; old != nil {
		old.finish()
	}
	h.pty[peerID] = &ptySess{
		opID:     opID,
		bytes:    make(chan []byte, ptyCap),
		finished: make(chan struct{}),
	}
}

func (h *Hub) finishPTYLocked(peerID, opID string) {
	s := h.pty[peerID]
	if s == nil {
		return
	}
	if opID != "" && s.opID != opID {
		return
	}
	s.finish()
}

func (h *Hub) forget(peerID, opID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ws := h.waiters[peerID]; ws != nil {
		delete(ws, opID)
	}
}

var idSeq atomic.Uint64

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		n := idSeq.Add(1)
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano))) + "-" + hex.EncodeToString([]byte{byte(n)})
	}
	return hex.EncodeToString(b[:])
}
