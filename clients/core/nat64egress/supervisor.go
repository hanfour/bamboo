// SPDX-License-Identifier: AGPL-3.0-or-later

package nat64egress

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// runnable is one started process the supervisor watches. Wait blocks
// until it exits. *exec.Cmd satisfies this directly; tests inject a fake.
type runnable interface{ Wait() error }

// supervisor keeps a single logical process alive: it starts it via
// startFn, and on an unexpected exit waits backoff and restarts, until
// Stop cancels the context (which kills the child via
// exec.CommandContext). One supervisor owns one tayga daemon.
type supervisor struct {
	startFn func(context.Context) (runnable, error)
	backoff time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	stopped chan struct{}
}

func newSupervisor(startFn func(context.Context) (runnable, error)) *supervisor {
	return &supervisor{startFn: startFn, backoff: time.Second}
}

// Start launches the supervise loop. Idempotent: a second Start while
// already running is a no-op.
func (s *supervisor) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.stopped = make(chan struct{})
	go s.loop(ctx, s.stopped)
}

func (s *supervisor) loop(ctx context.Context, stopped chan struct{}) {
	defer close(stopped)
	for {
		r, err := s.startFn(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // Stop() cancelled us mid-start; not a real failure
			}
			slog.Warn("nat64 egress: tayga start failed", "err", err)
		} else if waitErr := r.Wait(); ctx.Err() != nil {
			return // Stop() cancelled us; the exit was expected
		} else {
			slog.Warn("nat64 egress: tayga exited; restarting", "err", waitErr)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.backoff):
		}
	}
}

// Stop cancels the child and waits for the loop to exit. Idempotent.
func (s *supervisor) Stop() {
	s.mu.Lock()
	cancel, stopped := s.cancel, s.stopped
	s.cancel = nil
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-stopped
}
