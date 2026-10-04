// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanfour/bamboo/clients/core/session"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestOperateOnceDeliversMessage(t *testing.T) {
	root := t.TempDir()
	desk, err := session.OpenDesk(root, false)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeOperate{recv: make(chan *bamboov1.AdminOperateFrame, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- operateOnce(ctx, func(context.Context) (OperateStream, error) { return fake, nil }, "peer-1", false, desk)
	}()
	hello := fake.wait(t, 1)
	if hello.GetHello() == nil || hello.GetHello().GetPeerId() != "peer-1" || hello.GetHello().GetExecEnabled() {
		t.Fatalf("hello %+v", hello)
	}
	fake.recv <- &bamboov1.AdminOperateFrame{Frame: &bamboov1.AdminOperateFrame_Msg{Msg: &bamboov1.AdminOperateMsg{
		OpId: "m1", From: "admin@example.com", Text: "from-web",
	}}}
	result := fake.wait(t, 2)
	got := result.GetResult()
	if got == nil || !got.GetOk() || got.GetOpId() != "m1" {
		t.Fatalf("result %+v", result)
	}
	close(fake.recv)
	select {
	case err := <-done:
		if err != io.EOF {
			t.Fatalf("once: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("operateOnce did not return")
	}
	inbox, err := session.ReadInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Text != "from-web" || inbox[0].From != "admin@example.com" {
		t.Fatalf("inbox %+v", inbox)
	}
}

func TestOperateOnceRejectsTerminalWhenExecOff(t *testing.T) {
	desk, err := session.OpenDesk(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeOperate{recv: make(chan *bamboov1.AdminOperateFrame, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- operateOnce(ctx, func(context.Context) (OperateStream, error) { return fake, nil }, "peer-1", false, desk)
	}()
	_ = fake.wait(t, 1)
	fake.recv <- &bamboov1.AdminOperateFrame{Frame: &bamboov1.AdminOperateFrame_PtyStart{PtyStart: &bamboov1.AdminOperatePTYStart{OpId: "p1"}}}
	result := fake.wait(t, 2)
	got := result.GetResult()
	if got == nil || got.GetOk() || got.GetError() != session.ErrExecOff {
		t.Fatalf("result %+v", result)
	}
	cancel()
	close(fake.recv)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("operateOnce did not return")
	}
}

func TestRunOperateRetriesUnsupportedController(t *testing.T) {
	desk, err := session.OpenDesk(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	previous := operateAfter
	operateAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	t.Cleanup(func() { operateAfter = previous })

	var n atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() {
		RunOperate(ctx, func(context.Context) (OperateStream, error) {
			if n.Add(1) >= 2 {
				cancel()
			}
			return nil, status.Error(codes.Unimplemented, "unknown method")
		}, "peer-1", false, desk)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("retry loop did not stop")
	}
	if n.Load() < 2 {
		t.Fatalf("dials = %d", n.Load())
	}
}

type fakeOperate struct {
	mu   sync.Mutex
	sent []*bamboov1.DeviceOperateFrame
	recv chan *bamboov1.AdminOperateFrame
}

func (f *fakeOperate) Send(frame *bamboov1.DeviceOperateFrame) error {
	f.mu.Lock()
	f.sent = append(f.sent, frame)
	f.mu.Unlock()
	return nil
}

func (f *fakeOperate) Recv() (*bamboov1.AdminOperateFrame, error) {
	frame, ok := <-f.recv
	if !ok {
		return nil, io.EOF
	}
	return frame, nil
}

func (f *fakeOperate) wait(t *testing.T, n int) *bamboov1.DeviceOperateFrame {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		f.mu.Lock()
		if len(f.sent) >= n {
			frame := f.sent[n-1]
			f.mu.Unlock()
			return frame
		}
		f.mu.Unlock()
		select {
		case <-deadline:
			t.Fatalf("wanted %d frames", n)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
