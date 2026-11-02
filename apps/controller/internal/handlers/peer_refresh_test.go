// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type refreshRecorder struct {
	mu   sync.Mutex
	revs map[uuid.UUID]int64 // revision returned at send time
	sent map[uuid.UUID][]int64
}

func newRefreshRecorder() *refreshRecorder {
	return &refreshRecorder{revs: map[uuid.UUID]int64{}, sent: map[uuid.UUID][]int64{}}
}

func (r *refreshRecorder) revision(t uuid.UUID) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.revs[t]
}

func (r *refreshRecorder) publish(t uuid.UUID, rev int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent[t] = append(r.sent[t], rev)
}

func (r *refreshRecorder) setRev(t uuid.UUID, rev int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revs[t] = rev
}

func (r *refreshRecorder) sentFor(t uuid.UUID) []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.sent[t]...)
}

const testWindow = 40 * time.Millisecond

func TestPeerRefresh_CoalescesBurstIntoOneTrailingSend(t *testing.T) {
	rec := newRefreshRecorder()
	c := newPeerRefreshCoalescer(testWindow, rec.revision, rec.publish)
	defer c.Close()
	tenant := uuid.New()
	rec.setRev(tenant, 3)
	for range 10 {
		c.Schedule(tenant)
	}
	if got := rec.sentFor(tenant); len(got) != 0 {
		t.Fatalf("sent before the window closed: %v", got)
	}
	rec.setRev(tenant, 4) // the revision at send time wins
	time.Sleep(3 * testWindow)
	if got := rec.sentFor(tenant); len(got) != 1 || got[0] != 4 {
		t.Errorf("sent = %v, want exactly one PolicyChanged at revision 4", got)
	}
}

func TestPeerRefresh_OnePerTenant(t *testing.T) {
	rec := newRefreshRecorder()
	c := newPeerRefreshCoalescer(testWindow, rec.revision, rec.publish)
	defer c.Close()
	a, b := uuid.New(), uuid.New()
	rec.setRev(a, 1)
	rec.setRev(b, 2)
	for range 5 {
		c.Schedule(a)
		c.Schedule(b)
	}
	time.Sleep(3 * testWindow)
	if got := rec.sentFor(a); len(got) != 1 {
		t.Errorf("tenant a sent %v, want one", got)
	}
	if got := rec.sentFor(b); len(got) != 1 {
		t.Errorf("tenant b sent %v, want one", got)
	}
}

func TestPeerRefresh_RevisionZeroAtSendTimeSendsNothing(t *testing.T) {
	rec := newRefreshRecorder()
	c := newPeerRefreshCoalescer(testWindow, rec.revision, rec.publish)
	defer c.Close()
	tenant := uuid.New()
	c.Schedule(tenant)
	time.Sleep(3 * testWindow)
	if got := rec.sentFor(tenant); len(got) != 0 {
		t.Errorf("sent %v at revision 0, want nothing", got)
	}
}

func TestPeerRefresh_NewWindowAfterSend(t *testing.T) {
	rec := newRefreshRecorder()
	c := newPeerRefreshCoalescer(testWindow, rec.revision, rec.publish)
	defer c.Close()
	tenant := uuid.New()
	rec.setRev(tenant, 1)
	c.Schedule(tenant)
	time.Sleep(3 * testWindow)
	c.Schedule(tenant)
	time.Sleep(3 * testWindow)
	if got := rec.sentFor(tenant); len(got) != 2 {
		t.Errorf("sent %v, want one per window", got)
	}
}

func TestPeerRefresh_CloseStopsPendingTimers(t *testing.T) {
	rec := newRefreshRecorder()
	c := newPeerRefreshCoalescer(testWindow, rec.revision, rec.publish)
	tenant := uuid.New()
	rec.setRev(tenant, 1)
	c.Schedule(tenant)
	c.Close()
	c.Schedule(tenant) // after Close: ignored
	time.Sleep(3 * testWindow)
	if got := rec.sentFor(tenant); len(got) != 0 {
		t.Errorf("sent %v after Close, want nothing", got)
	}
	c.Close() // idempotent
}

// TestPeerRefresh_NilReceiverIsSafe: a CoordinatorHandler built without
// NewCoordinatorHandler (tests, partial wiring) has no coalescer; peer
// events and shutdown must not panic.
func TestPeerRefresh_NilReceiverIsSafe(t *testing.T) {
	var c *peerRefreshCoalescer
	c.Schedule(uuid.New())
	c.SetWindow(time.Second)
	c.Close()
	h := &CoordinatorHandler{}
	h.Close()
	h.SetPeerRefreshWindow(time.Second)
}
