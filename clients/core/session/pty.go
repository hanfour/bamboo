// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/hanfour/bamboo/clients/core/wg"
)

const ptyChunk = 16 << 10

type winsizeMsg struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

type exitMsg struct {
	Exit int `json:"exit"`
}

// Term is an interactive terminal on an allowlisted peer.
// Read returns io.EOF after the remote shell exits.
type Term struct {
	conn  net.Conn
	local wg.PrivateKey
	peer  wg.PublicKey
	wmu   sync.Mutex
	r     *io.PipeReader
	w     *io.PipeWriter
	once  sync.Once
	pipe  sync.Once
	done  chan struct{}
	code  int
	err   error
}

// DialTerm starts a PTY on the peer. An empty argv runs /bin/sh.
// ctx bounds the dial and handshake. Close ends the terminal.
func DialTerm(ctx context.Context, addr string, local wg.PrivateKey, peer wg.PublicKey, argv []string, rows, cols int) (*Term, error) {
	conn, err := dialAndHandshake(ctx, addr, local, peer)
	if err != nil {
		return nil, err
	}
	sealed, err := seal(peer, local, Request{Op: OpPTY, Argv: argv, Rows: rows, Cols: cols})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := writeFrame(conn, kindRequest, local.PublicKey(), sealed); err != nil {
		_ = conn.Close()
		return nil, err
	}
	kind, remote, body, err := readFrame(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if kind != kindResponse || remote != peer {
		_ = conn.Close()
		return nil, errors.New("bad response")
	}
	opened, err := open(peer, local, body)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(opened, &resp); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !resp.OK {
		_ = conn.Close()
		if resp.Error == "" {
			resp.Error = ErrBadReq
		}
		return nil, errors.New("session: " + resp.Error)
	}
	_ = conn.SetDeadline(time.Time{})
	pr, pw := io.Pipe()
	t := &Term{
		conn:  conn,
		local: local,
		peer:  peer,
		r:     pr,
		w:     pw,
		done:  make(chan struct{}),
	}
	go t.readLoop()
	return t, nil
}

// Read returns output from the remote terminal.
func (t *Term) Read(p []byte) (int, error) { return t.r.Read(p) }

// Write sends keystrokes to the remote terminal.
func (t *Term) Write(p []byte) (int, error) {
	sent := 0
	for len(p) > 0 {
		n := len(p)
		if n > ptyChunk {
			n = ptyChunk
		}
		if err := t.send(kindData, p[:n]); err != nil {
			return sent, err
		}
		sent += n
		p = p[n:]
	}
	return sent, nil
}

// Resize updates the remote terminal size.
func (t *Term) Resize(rows, cols int) error {
	raw, err := json.Marshal(winsizeMsg{Rows: rows, Cols: cols})
	if err != nil {
		return err
	}
	return t.send(kindWinsize, raw)
}

// Close hangs up the terminal.
func (t *Term) Close() error {
	_ = t.send(kindExit, nil)
	err := t.conn.Close()
	t.finish(-1, io.EOF)
	return err
}

// ExitCode waits until the remote shell exits.
func (t *Term) ExitCode() (int, error) {
	<-t.done
	return t.code, t.err
}

func (t *Term) send(kind byte, payload []byte) error {
	sealed, err := sealBytes(t.peer, t.local, payload)
	if err != nil {
		return err
	}
	t.wmu.Lock()
	defer t.wmu.Unlock()
	return writeFrame(t.conn, kind, t.local.PublicKey(), sealed)
}

func (t *Term) readLoop() {
	defer t.closePipe()
	for {
		kind, remote, body, err := readFrame(t.conn)
		if err != nil {
			t.closePipe()
			t.finish(-1, err)
			return
		}
		if remote != t.peer {
			t.closePipe()
			t.finish(-1, errors.New("bad response"))
			return
		}
		opened, err := open(t.peer, t.local, body)
		if err != nil {
			t.closePipe()
			t.finish(-1, err)
			return
		}
		switch kind {
		case kindData:
			if _, err := t.w.Write(opened); err != nil {
				t.closePipe()
				t.finish(-1, err)
				return
			}
		case kindExit:
			code := -1
			var msg exitMsg
			if json.Unmarshal(opened, &msg) == nil {
				code = msg.Exit
			}
			t.closePipe()
			t.finish(code, nil)
			return
		}
	}
}

func (t *Term) closePipe() {
	t.pipe.Do(func() { _ = t.w.Close() })
}

func (t *Term) finish(code int, err error) {
	t.once.Do(func() {
		t.code = code
		t.err = err
		close(t.done)
	})
}

func (s *server) servePTY(ctx context.Context, conn net.Conn, peer wg.PublicKey, req Request) {
	failWrite := func(code string) {
		sealed, err := seal(peer, s.cfg.PrivateKey, fail(code))
		if err != nil {
			return
		}
		_ = writeFrame(conn, kindResponse, s.cfg.PrivateKey.PublicKey(), sealed)
	}
	if !s.cfg.EnableExec {
		failWrite(ErrExecOff)
		return
	}
	argv, err := ptyArgv(req.Argv)
	if err != nil {
		failWrite(ErrBadReq)
		return
	}
	master, cmd, err := startPTY(argv, req.Rows, req.Cols)
	if err != nil {
		failWrite(ErrBadReq)
		return
	}
	defer func() { _ = master.Close() }()

	ok, err := seal(peer, s.cfg.PrivateKey, Response{OK: true})
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return
	}
	if err := writeFrame(conn, kindResponse, s.cfg.PrivateKey.PublicKey(), ok); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return
	}

	var wmu sync.Mutex
	send := func(kind byte, payload []byte) error {
		sealed, err := sealBytes(peer, s.cfg.PrivateKey, payload)
		if err != nil {
			return err
		}
		wmu.Lock()
		defer wmu.Unlock()
		return writeFrame(conn, kind, s.cfg.PrivateKey.PublicKey(), sealed)
	}

	var pumps sync.WaitGroup
	pumps.Add(2)
	outDone := make(chan struct{})
	go func() {
		defer pumps.Done()
		defer close(outDone)
		buf := make([]byte, ptyChunk)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				if send(kindData, chunk) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	inputDone := make(chan struct{})
	go func() {
		defer pumps.Done()
		defer close(inputDone)
		for {
			kind, remote, body, err := readFrame(conn)
			if err != nil || remote != peer {
				return
			}
			opened, err := open(peer, s.cfg.PrivateKey, body)
			if err != nil {
				return
			}
			switch kind {
			case kindData:
				if _, err := master.Write(opened); err != nil {
					return
				}
			case kindWinsize:
				var sz winsizeMsg
				if json.Unmarshal(opened, &sz) == nil {
					_ = resizePTY(master, sz.Rows, sz.Cols)
				}
			case kindExit:
				return
			}
		}
	}()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-inputDone:
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		waitErr = <-waitCh
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		waitErr = <-waitCh
	}
	<-outDone

	code := 0
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		code = exitErr.ExitCode()
	} else if waitErr != nil {
		code = -1
	}
	raw, err := json.Marshal(exitMsg{Exit: code})
	if err == nil {
		_ = send(kindExit, raw)
	}
	_ = conn.Close()
	_ = master.Close()
	pumps.Wait()
}

func ptyArgv(argv []string) ([]string, error) {
	if len(argv) == 0 {
		return []string{"/bin/sh"}, nil
	}
	if len(argv) > maxArgs {
		return nil, errors.New(ErrBadReq)
	}
	for _, a := range argv {
		if a == "" || len(a) > maxArgLen || strings.ContainsRune(a, 0) {
			return nil, errors.New(ErrBadReq)
		}
	}
	return argv, nil
}
