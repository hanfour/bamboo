// SPDX-License-Identifier: AGPL-3.0-or-later

package operate

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCallOffline(t *testing.T) {
	h := NewHub()
	_, err := h.Call(context.Background(), "peer", Frame{Kind: KindMsg, Text: "hi"})
	if !errors.Is(err, ErrOffline) {
		t.Fatalf("err = %v", err)
	}
}

func TestExecOffDoesNotSend(t *testing.T) {
	h := NewHub()
	frames, _, detach := h.Attach("peer", false)
	defer detach()
	_, err := h.Call(context.Background(), "peer", Frame{Kind: KindPTYStart})
	if !errors.Is(err, ErrExecOff) {
		t.Fatalf("err = %v", err)
	}
	select {
	case f := <-frames:
		t.Fatalf("sent %+v", f)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestCallResultAndPTYExit(t *testing.T) {
	h := NewHub()
	frames, _, detach := h.Attach("peer", true)
	defer detach()

	got := make(chan Result, 1)
	go func() {
		res, err := h.Call(context.Background(), "peer", Frame{OpID: "op-1", Kind: KindPTYStart, Rows: 24, Cols: 80})
		if err != nil {
			got <- Result{Error: err.Error()}
			return
		}
		got <- res
	}()

	f := recvFrame(t, frames)
	if f.Kind != KindPTYStart || f.OpID != "op-1" {
		t.Fatalf("frame = %+v", f)
	}
	sub, ok := h.Subscribe("peer")
	if !ok || sub.OpID != "op-1" {
		t.Fatalf("subscribe %+v %v", sub, ok)
	}
	h.Output("peer", "op-1", []byte("hi"))
	select {
	case b := <-sub.Bytes:
		if string(b) != "hi" {
			t.Fatalf("bytes %q", b)
		}
	case <-time.After(time.Second):
		t.Fatal("missing output")
	}
	h.Result("peer", Result{OpID: "op-1", OK: true})
	res := <-got
	if !res.OK || res.OpID != "op-1" {
		t.Fatalf("result %+v", res)
	}

	h.Result("peer", Result{OpID: "op-1", OK: true, ExitCode: 0})
	select {
	case <-sub.Finished:
	case <-time.After(time.Second):
		t.Fatal("terminal did not finish")
	}
}

func TestReplacePTYKeepsNewSession(t *testing.T) {
	h := NewHub()
	frames, _, detach := h.Attach("peer", true)
	defer detach()
	go func() {
		for f := range frames {
			h.Result("peer", Result{OpID: f.OpID, OK: true})
		}
	}()
	if _, err := h.Call(context.Background(), "peer", Frame{OpID: "a", Kind: KindPTYStart}); err != nil {
		t.Fatal(err)
	}
	first, ok := h.Subscribe("peer")
	if !ok {
		t.Fatal("missing first")
	}
	if _, err := h.Call(context.Background(), "peer", Frame{OpID: "b", Kind: KindPTYStart}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.Finished:
	case <-time.After(time.Second):
		t.Fatal("old terminal stayed open")
	}
	second, ok := h.Subscribe("peer")
	if !ok || second.OpID != "b" {
		t.Fatalf("second %+v", second)
	}
	h.Result("peer", Result{OpID: "a", OK: true, ExitCode: 1})
	select {
	case <-second.Finished:
		t.Fatal("old exit finished the new shell")
	case <-time.After(50 * time.Millisecond):
	}
	h.Result("peer", Result{OpID: "b", OK: true, ExitCode: 0})
	select {
	case <-second.Finished:
	case <-time.After(time.Second):
		t.Fatal("new shell did not finish")
	}
}

func TestSecondAttachClosesFirst(t *testing.T) {
	h := NewHub()
	_, done, detach := h.Attach("peer", true)
	_, _, detach2 := h.Attach("peer", false)
	defer detach2()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old attachment stayed open")
	}
	detach()
	enabled, online := h.ExecEnabled("peer")
	if enabled || !online {
		t.Fatalf("enabled=%v online=%v", enabled, online)
	}
}

func TestPushBusyWhenFull(t *testing.T) {
	h := NewHub()
	_, _, detach := h.Attach("peer", true)
	defer detach()
	for i := 0; i < outCap; i++ {
		if err := h.Push(context.Background(), "peer", Frame{Kind: KindPTYData, Data: []byte{1}}); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}
	err := h.Push(context.Background(), "peer", Frame{Kind: KindPTYData, Data: []byte{2}})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v", err)
	}
}

func TestOutputDropsWhenFull(t *testing.T) {
	h := NewHub()
	frames, _, detach := h.Attach("peer", true)
	defer detach()
	go func() {
		f := <-frames
		h.Result("peer", Result{OpID: f.OpID, OK: true})
	}()
	if _, err := h.Call(context.Background(), "peer", Frame{OpID: "op", Kind: KindPTYStart}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < ptyCap; i++ {
		h.Output("peer", "op", []byte{1})
	}
	done := make(chan struct{})
	go func() {
		h.Output("peer", "op", []byte{2})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("output blocked on a full buffer")
	}
}

func TestDetachUnblocksCall(t *testing.T) {
	h := NewHub()
	frames, _, detach := h.Attach("peer", true)
	got := make(chan error, 1)
	go func() {
		_, err := h.Call(context.Background(), "peer", Frame{Kind: KindMsg, Text: "x", From: "a"})
		got <- err
	}()
	recvFrame(t, frames)
	detach()
	select {
	case err := <-got:
		if !errors.Is(err, ErrOffline) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("call stayed blocked")
	}
}

func recvFrame(t *testing.T, frames <-chan Frame) Frame {
	t.Helper()
	select {
	case f := <-frames:
		return f
	case <-time.After(time.Second):
		t.Fatal("no frame")
		return Frame{}
	}
}
