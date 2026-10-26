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

// startTerm opens a terminal on peer and answers the start request.
func startTerm(t *testing.T, h *Hub, frames <-chan Frame, opID string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.Call(context.Background(), "peer", Frame{OpID: opID, Kind: KindPTYStart, Rows: 24, Cols: 80})
	}()
	recvFrame(t, frames)
	h.Result("peer", Result{OpID: opID, OK: true})
	<-done
}

func recvEnd(t *testing.T, ch <-chan PTYEnd) PTYEnd {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(time.Second):
		t.Fatal("no PTYEnd")
		return PTYEnd{}
	}
}

func TestPTYEndReportsSizesAndExit(t *testing.T) {
	h := NewHub()
	ends := make(chan PTYEnd, 4)
	h.SetOnPTYEnd(func(_ string, e PTYEnd) { ends <- e })
	frames, _, detach := h.Attach("peer", true)
	defer detach()
	startTerm(t, h, frames, "op-1")

	if err := h.Push(context.Background(), "peer", Frame{OpID: "op-1", Kind: KindPTYData, Data: []byte("ls\n")}); err != nil {
		t.Fatal(err)
	}
	// Input for another shell is not counted.
	if err := h.Push(context.Background(), "peer", Frame{OpID: "other", Kind: KindPTYData, Data: []byte("xxxx")}); err != nil {
		t.Fatal(err)
	}
	h.Output("peer", "op-1", []byte("file1\nfile2\n"))
	h.Result("peer", Result{OpID: "op-1", OK: true, ExitCode: 3})

	e := recvEnd(t, ends)
	if e.OpID != "op-1" || e.Reason != EndExit || e.ExitCode != 3 || e.BytesIn != 3 || e.BytesOut != 12 {
		t.Fatalf("end = %+v", e)
	}
	if e.Started.IsZero() || e.Duration < 0 {
		t.Fatalf("timing = %+v", e)
	}
	select {
	case extra := <-ends:
		t.Fatalf("reported twice: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPTYEndOnDisconnectAndReplace(t *testing.T) {
	h := NewHub()
	ends := make(chan PTYEnd, 4)
	h.SetOnPTYEnd(func(_ string, e PTYEnd) { ends <- e })
	frames, _, detach := h.Attach("peer", true)
	startTerm(t, h, frames, "op-1")
	startTerm(t, h, frames, "op-2")
	if e := recvEnd(t, ends); e.OpID != "op-1" || e.Reason != EndReplaced || e.ExitCode != -1 {
		t.Fatalf("replaced = %+v", e)
	}
	detach()
	if e := recvEnd(t, ends); e.OpID != "op-2" || e.Reason != EndDisconnected {
		t.Fatalf("disconnected = %+v", e)
	}
}
