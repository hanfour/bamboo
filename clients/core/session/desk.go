// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"errors"
	"os"
	"os/exec"
	"sync"
)

// Desk operation kinds. The admin channel and this package share the
// same strings.
const (
	DeskMsg       = "msg"
	DeskPut       = "put"
	DeskGet       = "get"
	DeskPTYStart  = "pty-start"
	DeskPTYData   = "pty-data"
	DeskPTYResize = "pty-resize"
	DeskPTYClose  = "pty-close"
)

// DeskOp is one admin operation. From is the admin identity for a
// message, not a WireGuard key.
type DeskOp struct {
	Kind string
	From string
	Text string
	Path string
	Data []byte
	Argv []string
	Rows int
	Cols int
}

// DeskReply is the immediate answer. A terminal start that succeeds
// also returns TermBytes; the shell's exit code is valid after Out
// closes.
type DeskReply struct {
	OK       bool
	Error    string
	ExitCode int
	Data     []byte
}

// TermBytes is the shell's output. Code is safe to call after Out
// closes.
type TermBytes struct {
	Out  <-chan []byte
	Code func() int
}

// Desk serves the admin channel from the device. It reuses the session
// file jail and inbox. A terminal requires enableExec.
type Desk struct {
	srv  *server
	mu   sync.Mutex
	term *deskTerm
}

// OpenDesk prepares a desk rooted at root. root must be a directory.
func OpenDesk(root string, enableExec bool) (*Desk, error) {
	if root == "" {
		return nil, errors.New("session root is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("session root is not a directory")
	}
	return &Desk{srv: &server{cfg: Config{Root: root, EnableExec: enableExec}}}, nil
}

// Handle runs one operation. The terminal byte channel is non-nil only
// for a successful pty-start.
func (d *Desk) Handle(op DeskOp) (DeskReply, *TermBytes) {
	if d == nil || d.srv == nil {
		return failReply(ErrBadReq), nil
	}
	switch op.Kind {
	case DeskMsg:
		return fromResponse(appendInbox(d.srv.cfg.Root, d.srv.now(), op.From, op.Text)), nil
	case DeskPut:
		return fromResponse(d.srv.put(Request{Path: op.Path, Data: op.Data})), nil
	case DeskGet:
		return fromResponse(d.srv.get(Request{Path: op.Path})), nil
	case DeskPTYStart:
		return d.startTerm(op)
	case DeskPTYData:
		return d.writeTerm(op.Data), nil
	case DeskPTYResize:
		return d.resizeTerm(op.Rows, op.Cols), nil
	case DeskPTYClose:
		d.closeTerm()
		return DeskReply{OK: true}, nil
	default:
		return failReply(ErrBadReq), nil
	}
}

func (d *Desk) startTerm(op DeskOp) (DeskReply, *TermBytes) {
	if !d.srv.cfg.EnableExec {
		return failReply(ErrExecOff), nil
	}
	argv, err := ptyArgv(op.Argv)
	if err != nil {
		return failReply(err.Error()), nil
	}
	master, cmd, err := startPTY(argv, op.Rows, op.Cols)
	if err != nil {
		return failReply(ErrBadReq), nil
	}
	t := newDeskTerm(master, cmd)
	d.mu.Lock()
	old := d.term
	d.term = t
	d.mu.Unlock()
	if old != nil {
		old.stop()
	}
	go t.run(d)
	return DeskReply{OK: true}, &TermBytes{Out: t.out, Code: t.exitCode}
}

func (d *Desk) writeTerm(p []byte) DeskReply {
	d.mu.Lock()
	t := d.term
	d.mu.Unlock()
	if t == nil {
		return failReply(ErrBadReq)
	}
	if err := t.write(p); err != nil {
		return failReply(ErrBadReq)
	}
	return DeskReply{OK: true}
}

func (d *Desk) resizeTerm(rows, cols int) DeskReply {
	d.mu.Lock()
	t := d.term
	d.mu.Unlock()
	if t == nil {
		return failReply(ErrBadReq)
	}
	t.wmu.Lock()
	err := resizePTY(t.master, rows, cols)
	t.wmu.Unlock()
	if err != nil {
		return failReply(ErrBadReq)
	}
	return DeskReply{OK: true}
}

func (d *Desk) closeTerm() {
	d.mu.Lock()
	t := d.term
	d.mu.Unlock()
	if t != nil {
		t.stop()
	}
}

type deskTerm struct {
	master *os.File
	cmd    *exec.Cmd
	out    chan []byte
	stopCh chan struct{}
	once   sync.Once
	wmu    sync.Mutex
	code   int
}

func newDeskTerm(master *os.File, cmd *exec.Cmd) *deskTerm {
	return &deskTerm{
		master: master,
		cmd:    cmd,
		out:    make(chan []byte, 32),
		stopCh: make(chan struct{}),
	}
}

func (t *deskTerm) stop() {
	t.once.Do(func() { close(t.stopCh) })
}

func (t *deskTerm) exitCode() int { return t.code }

func (t *deskTerm) write(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_, err := t.master.Write(p)
	return err
}

func (t *deskTerm) run(d *Desk) {
	defer close(t.out)
	defer func() {
		d.mu.Lock()
		if d.term == t {
			d.term = nil
		}
		d.mu.Unlock()
	}()
	readDone := make(chan struct{})
	go t.pump(readDone)
	waitCh := make(chan error, 1)
	go func() { waitCh <- t.cmd.Wait() }()
	select {
	case err := <-waitCh:
		t.code = exitCodeOf(err)
	case <-t.stopCh:
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		t.code = exitCodeOf(<-waitCh)
	}
	_ = t.master.Close()
	<-readDone
}

func (t *deskTerm) pump(done chan struct{}) {
	defer close(done)
	buf := make([]byte, ptyChunk)
	for {
		n, err := t.master.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			select {
			case t.out <- chunk:
			case <-t.stopCh:
				return
			default:
			}
		}
		if err != nil {
			return
		}
		select {
		case <-t.stopCh:
			return
		default:
		}
	}
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func fromResponse(resp Response) DeskReply {
	return DeskReply{OK: resp.OK, Error: resp.Error, ExitCode: resp.ExitCode, Data: append([]byte(nil), resp.Data...)}
}

func failReply(code string) DeskReply {
	return DeskReply{OK: false, Error: code}
}
