// SPDX-License-Identifier: AGPL-3.0-or-later

package nat64egress

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProc returns from Wait when its ctx is cancelled or an error is
// pushed onto exit (simulating the child dying).
type fakeProc struct {
	ctx  context.Context
	exit chan error
}

func (f *fakeProc) Wait() error {
	select {
	case <-f.ctx.Done():
		return f.ctx.Err()
	case err := <-f.exit:
		return err
	}
}

func TestSupervisor_RestartsOnExit(t *testing.T) {
	var starts int32
	exit := make(chan error)
	var mu sync.Mutex
	var lastCtx context.Context

	s := newSupervisor(func(ctx context.Context) (runnable, error) {
		atomic.AddInt32(&starts, 1)
		mu.Lock()
		lastCtx = ctx
		mu.Unlock()
		return &fakeProc{ctx: ctx, exit: exit}, nil
	})
	s.backoff = time.Millisecond // make restarts fast
	s.Start()

	// Drive two unexpected exits → expect three total starts (initial + 2 restarts).
	exit <- errors.New("crash 1")
	exit <- errors.New("crash 2")

	// Give the loop a moment to spin the third start.
	waitFor(t, func() bool { return atomic.LoadInt32(&starts) == 3 })

	s.Stop()
	final := atomic.LoadInt32(&starts)
	// No restart after Stop.
	time.Sleep(10 * time.Millisecond)
	if atomic.LoadInt32(&starts) != final {
		t.Errorf("started again after Stop: %d -> %d", final, atomic.LoadInt32(&starts))
	}
	mu.Lock()
	ctx := lastCtx
	mu.Unlock()
	if ctx.Err() == nil {
		t.Error("Stop should cancel the child context")
	}
}

func TestSupervisor_StopIdempotent(t *testing.T) {
	s := newSupervisor(func(ctx context.Context) (runnable, error) {
		return &fakeProc{ctx: ctx, exit: make(chan error)}, nil
	})
	s.Start()
	s.Stop()
	s.Stop() // must not panic or block
}

// waitFor polls cond up to ~1s.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestSupervisor_Alive(t *testing.T) {
	exit := make(chan error)
	started := make(chan struct{}, 8)
	s := newSupervisor(func(ctx context.Context) (runnable, error) {
		started <- struct{}{}
		return &fakeProc{ctx: ctx, exit: exit}, nil
	})
	s.backoff = time.Millisecond

	if s.Alive() {
		t.Fatal("Alive() should be false before Start")
	}
	s.Start()
	<-started // first child up
	waitFor(t, func() bool { return s.Alive() })

	// Child crashes → Alive() drops until the supervisor restarts it.
	exit <- errors.New("crash")
	<-started // restart spun a new child
	waitFor(t, func() bool { return s.Alive() })

	s.Stop()
	if s.Alive() {
		t.Error("Alive() should be false after Stop")
	}
}

func TestSupervisor_AliveFalseWhenStartFails(t *testing.T) {
	var calls int32
	s := newSupervisor(func(ctx context.Context) (runnable, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("boom") // startFn keeps failing
	})
	s.backoff = time.Millisecond
	s.Start()
	waitFor(t, func() bool { return atomic.LoadInt32(&calls) >= 2 }) // looping on failure
	if s.Alive() {
		t.Error("Alive() must be false while startFn keeps failing")
	}
	s.Stop()
}
