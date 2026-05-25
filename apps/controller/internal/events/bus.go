// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events provides an in-memory pub/sub for tenant-scoped events.
//
// Phase 1 supports a single controller instance. The Bus interface is
// designed so a future Postgres LISTEN/NOTIFY or NATS-backed implementation
// can be dropped in without changing handler call sites.
//
// Events delivered to slow subscribers are dropped — the client then
// reconciles by reopening WatchPeers, which sends fresh state. This keeps
// the Bus lock-free in the hot path and prevents one stuck client from
// pausing the rest.
package events

import (
	"sync"

	"github.com/google/uuid"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// Bus is the publish/subscribe surface used by Coordinator.WatchPeers
// and the Register / Heartbeat handlers.
type Bus struct {
	mu          sync.Mutex
	subscribers map[uuid.UUID]map[chan *bamboov1.WatchPeersEvent]struct{}
}

// NewBus constructs an empty Bus.
func NewBus() *Bus {
	return &Bus{
		subscribers: make(map[uuid.UUID]map[chan *bamboov1.WatchPeersEvent]struct{}),
	}
}

// Subscribe returns a buffered channel that receives every event for the
// tenant. The caller must invoke the returned cancel func when done; it
// removes the subscription and closes the channel.
func (b *Bus) Subscribe(tenantID uuid.UUID) (<-chan *bamboov1.WatchPeersEvent, func()) {
	const bufferSize = 32
	ch := make(chan *bamboov1.WatchPeersEvent, bufferSize)

	b.mu.Lock()
	if b.subscribers[tenantID] == nil {
		b.subscribers[tenantID] = make(map[chan *bamboov1.WatchPeersEvent]struct{})
	}
	b.subscribers[tenantID][ch] = struct{}{}
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		delete(b.subscribers[tenantID], ch)
		if len(b.subscribers[tenantID]) == 0 {
			delete(b.subscribers, tenantID)
		}
		b.mu.Unlock()
		close(ch)
	}
	return ch, cancel
}

// Publish delivers event to every subscriber of tenantID. Slow subscribers
// have their messages dropped rather than blocking the publisher.
func (b *Bus) Publish(tenantID uuid.UUID, event *bamboov1.WatchPeersEvent) {
	if event == nil {
		return
	}
	b.mu.Lock()
	subs := make([]chan *bamboov1.WatchPeersEvent, 0, len(b.subscribers[tenantID]))
	for ch := range b.subscribers[tenantID] {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- event:
		default:
			// dropped — slow consumer reconciles by reopening WatchPeers.
		}
	}
}

// PublishAll broadcasts event to every active subscriber across all
// tenants. Relays are tenant-agnostic infrastructure (the
// relay_servers table has no tenant_id column — see the
// 00003_relay_servers.sql notes), so a RelaysChanged event needs to
// reach every connected client regardless of tenant. Routing
// per-tenant through Publish would require enumerating tenants on
// every relay-health flip, an unnecessary DB round-trip when the
// in-memory subscriber map already knows the active set.
//
// Slow-subscriber semantics match Publish: drop rather than block.
func (b *Bus) PublishAll(event *bamboov1.WatchPeersEvent) {
	if event == nil {
		return
	}
	b.mu.Lock()
	subs := make([]chan *bamboov1.WatchPeersEvent, 0)
	for _, perTenant := range b.subscribers {
		for ch := range perTenant {
			subs = append(subs, ch)
		}
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- event:
		default:
			// dropped — slow consumer reconciles by reopening WatchPeers.
		}
	}
}

// SubscriberCount returns the number of active subscribers for tenantID.
// Useful in tests; not part of the hot path.
func (b *Bus) SubscriberCount(tenantID uuid.UUID) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers[tenantID])
}
