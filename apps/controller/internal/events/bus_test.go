// SPDX-License-Identifier: AGPL-3.0-or-later

package events_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/events"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

func TestBus_singleSubscriberReceivesEvent(t *testing.T) {
	bus := events.NewBus()
	tenantID := uuid.New()

	ch, cancel := bus.Subscribe(tenantID)
	defer cancel()

	want := &bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_PolicyChanged{
			PolicyChanged: &bamboov1.PolicyChanged{PolicyRevision: 42},
		},
	}
	bus.Publish(tenantID, want)

	select {
	case got := <-ch:
		if got.GetPolicyChanged().GetPolicyRevision() != 42 {
			t.Errorf("got revision %d, want 42", got.GetPolicyChanged().GetPolicyRevision())
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive event")
	}
}

// TestBus_PublishAllReachesEveryTenant pins the §4 P2 stage 4a
// broadcast contract: PublishAll delivers to every subscriber
// across every tenant, not just one. Without this, RelaysChanged
// events emitted by the health reaper would silently reach only
// the tenant whose Publish came first.
func TestBus_PublishAllReachesEveryTenant(t *testing.T) {
	bus := events.NewBus()
	tenantA, tenantB, tenantC := uuid.New(), uuid.New(), uuid.New()

	chA, cancelA := bus.Subscribe(tenantA)
	chB, cancelB := bus.Subscribe(tenantB)
	chC, cancelC := bus.Subscribe(tenantC)
	defer cancelA()
	defer cancelB()
	defer cancelC()

	bus.PublishAll(&bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_RelaysChanged{
			RelaysChanged: &bamboov1.RelaysChanged{},
		},
	})

	for name, ch := range map[string]<-chan *bamboov1.WatchPeersEvent{
		"A": chA, "B": chB, "C": chC,
	} {
		select {
		case ev := <-ch:
			if ev.GetRelaysChanged() == nil {
				t.Errorf("tenant %s got non-RelaysChanged event", name)
			}
		case <-time.After(time.Second):
			t.Fatalf("tenant %s did not receive PublishAll event", name)
		}
	}
}

// TestBus_PublishAllNilEventIsNoop guards against a nil-pointer
// panic — the reaper's edge case where ListEligible returns an
// error must not propagate as a crash through PublishAll.
func TestBus_PublishAllNilEventIsNoop(t *testing.T) {
	bus := events.NewBus()
	_, cancel := bus.Subscribe(uuid.New())
	defer cancel()
	bus.PublishAll(nil) // must not panic
}

func TestBus_publishToOtherTenantDoesNotLeak(t *testing.T) {
	bus := events.NewBus()
	a := uuid.New()
	b := uuid.New()

	chA, cancelA := bus.Subscribe(a)
	defer cancelA()

	bus.Publish(b, &bamboov1.WatchPeersEvent{})

	select {
	case <-chA:
		t.Fatal("tenant A received event for tenant B")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestBus_cancelRemovesSubscription(t *testing.T) {
	bus := events.NewBus()
	tenantID := uuid.New()

	_, cancel := bus.Subscribe(tenantID)
	if got := bus.SubscriberCount(tenantID); got != 1 {
		t.Fatalf("after subscribe, count = %d, want 1", got)
	}

	cancel()
	if got := bus.SubscriberCount(tenantID); got != 0 {
		t.Fatalf("after cancel, count = %d, want 0", got)
	}
}

func TestBus_slowSubscriberDoesNotBlockPublisher(t *testing.T) {
	bus := events.NewBus()
	tenantID := uuid.New()
	_, cancel := bus.Subscribe(tenantID)
	defer cancel()

	// Don't drain the channel. After buffer fills, Publish must still return
	// promptly (drop strategy).
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			bus.Publish(tenantID, &bamboov1.WatchPeersEvent{})
		}
		close(done)
	}()
	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on slow subscriber")
	}
}
