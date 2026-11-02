// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// defaultPeerRefreshWindow bounds the PolicyChanged follow-ups that peer
// membership events trigger: at most one per tenant per window.
const defaultPeerRefreshWindow = 2 * time.Second

// peerRefreshCoalescer turns bursts of "peers need fresh AllowedIps"
// into at most one PolicyChanged per tenant per window, sent on the
// trailing edge with the revision current at send time (none at
// revision 0). Explicit policy bumps do not go through it.
type peerRefreshCoalescer struct {
	revision func(tenantID uuid.UUID) int64
	publish  func(tenantID uuid.UUID, rev int64)

	mu       sync.Mutex
	window   time.Duration
	pending  map[uuid.UUID]*time.Timer
	closed   bool
	inflight sync.WaitGroup
}

func newPeerRefreshCoalescer(window time.Duration, revision func(uuid.UUID) int64, publish func(uuid.UUID, int64)) *peerRefreshCoalescer {
	return &peerRefreshCoalescer{
		revision: revision,
		publish:  publish,
		window:   window,
		pending:  map[uuid.UUID]*time.Timer{},
	}
}

// Schedule opens a window for tenantID unless one is already open. A nil
// coalescer (handler built without NewCoordinatorHandler) does nothing.
func (c *peerRefreshCoalescer) Schedule(tenantID uuid.UUID) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if _, open := c.pending[tenantID]; open {
		return
	}
	c.pending[tenantID] = time.AfterFunc(c.window, func() { c.fire(tenantID) })
}

func (c *peerRefreshCoalescer) fire(tenantID uuid.UUID) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	delete(c.pending, tenantID)
	c.inflight.Add(1)
	c.mu.Unlock()
	defer c.inflight.Done()

	if rev := c.revision(tenantID); rev > 0 {
		c.publish(tenantID, rev)
	}
}

// SetWindow changes the window for windows opened afterwards (tests).
func (c *peerRefreshCoalescer) SetWindow(d time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.window = d
}

// Close stops pending timers, waits for sends already running, and
// ignores later Schedule calls. Safe to call more than once and on nil.
func (c *peerRefreshCoalescer) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	for id, t := range c.pending {
		t.Stop()
		delete(c.pending, id)
	}
	c.mu.Unlock()
	c.inflight.Wait()
}
