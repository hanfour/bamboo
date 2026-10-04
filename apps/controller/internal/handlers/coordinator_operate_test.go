// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/operate"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

func TestServeOperateForwardsMessage(t *testing.T) {
	h := &CoordinatorHandler{ops: operate.NewHub()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeOperate(ctx)
	done := make(chan error, 1)
	go func() { done <- h.serveOperate(stream) }()

	stream.in <- &bamboov1.DeviceOperateFrame{Frame: &bamboov1.DeviceOperateFrame_Hello{Hello: &bamboov1.DeviceOperateHello{
		PeerId: "11111111-1111-1111-1111-111111111111", ExecEnabled: false,
	}}}

	resCh := make(chan operate.Result, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := h.DeskCall(ctx, mustPeer(t, "11111111-1111-1111-1111-111111111111"), operate.Frame{
			OpID: "m1", Kind: operate.KindMsg, From: "admin@example.com", Text: "ping",
		})
		if err != nil {
			errCh <- err
			return
		}
		resCh <- res
	}()

	select {
	case frame := <-stream.out:
		msg := frame.GetMsg()
		if msg == nil || msg.GetText() != "ping" || msg.GetFrom() != "admin@example.com" || msg.GetOpId() != "m1" {
			t.Fatalf("frame %+v", frame)
		}
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("desk frame was not sent")
	}

	stream.in <- &bamboov1.DeviceOperateFrame{Frame: &bamboov1.DeviceOperateFrame_Result{Result: &bamboov1.DeviceOperateResult{
		OpId: "m1", Ok: true,
	}}}
	select {
	case res := <-resCh:
		if !res.OK {
			t.Fatalf("result %+v", res)
		}
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("result did not return")
	}

	online, exec, err := h.DeskStatus(ctx, mustPeer(t, "11111111-1111-1111-1111-111111111111"))
	if err != nil || !online || exec {
		t.Fatalf("status online=%v exec=%v err=%v", online, exec, err)
	}
	close(stream.in)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not finish")
	}
}

func TestServeOperateRejectsMissingHello(t *testing.T) {
	h := &CoordinatorHandler{ops: operate.NewHub()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeOperate(ctx)
	stream.in <- &bamboov1.DeviceOperateFrame{Frame: &bamboov1.DeviceOperateFrame_Result{Result: &bamboov1.DeviceOperateResult{}}}
	err := h.serveOperate(stream)
	if err == nil {
		t.Fatal("expected an error")
	}
}

type fakeOperate struct {
	ctx context.Context
	in  chan *bamboov1.DeviceOperateFrame
	out chan *bamboov1.AdminOperateFrame
}

func newFakeOperate(ctx context.Context) *fakeOperate {
	return &fakeOperate{
		ctx: ctx,
		in:  make(chan *bamboov1.DeviceOperateFrame, 4),
		out: make(chan *bamboov1.AdminOperateFrame, 4),
	}
}

func (f *fakeOperate) Context() context.Context { return f.ctx }

func (f *fakeOperate) Send(frame *bamboov1.AdminOperateFrame) error {
	select {
	case <-f.ctx.Done():
		return f.ctx.Err()
	case f.out <- frame:
		return nil
	}
}

func (f *fakeOperate) Recv() (*bamboov1.DeviceOperateFrame, error) {
	select {
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	case frame, ok := <-f.in:
		if !ok {
			return nil, io.EOF
		}
		return frame, nil
	}
}

func mustPeer(t *testing.T, id string) uuid.UUID {
	t.Helper()
	parsed, err := uuid.Parse(id)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
