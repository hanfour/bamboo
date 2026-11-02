// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// idleTimer cancels a request when no bytes move in either direction for
// timeout. Touch is called from the response writer and the request body
// reader, which run on different goroutines, so the clock is atomic.
type idleTimer struct {
	timeout time.Duration
	last    atomic.Int64
	cancel  context.CancelFunc
	stop    chan struct{}
	once    sync.Once
}

func newIdleTimer(timeout time.Duration, cancel context.CancelFunc) *idleTimer {
	t := &idleTimer{timeout: timeout, cancel: cancel, stop: make(chan struct{})}
	t.Touch()
	go t.watch()
	return t
}

// Touch records activity.
func (t *idleTimer) Touch() { t.last.Store(time.Now().UnixNano()) }

// Stop ends the watcher. Safe to call more than once.
func (t *idleTimer) Stop() { t.once.Do(func() { close(t.stop) }) }

func (t *idleTimer) watch() {
	tick := t.timeout / 4
	if tick < 5*time.Millisecond {
		tick = 5 * time.Millisecond
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-t.stop:
			return
		case now := <-ticker.C:
			if now.UnixNano()-t.last.Load() >= int64(t.timeout) {
				t.cancel()
				return
			}
		}
	}
}
