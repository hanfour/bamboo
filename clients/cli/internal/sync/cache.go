// SPDX-License-Identifier: Apache-2.0

// Package sync owns the client-side peer state machine. It mediates
// between the server-streamed WatchPeersEvent feed and the local
// WireGuard device: every event mutates an in-memory cache, the cache
// gets reduced to a wg.DeviceConfig, and the configured Device is
// re-applied (Apply is idempotent on the device side).
package sync

import (
	stdsync "sync"

	"github.com/hanfour/bamboo/clients/core/wg"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// PeerCache holds the local view of peer set + the immutable "self"
// peer learned at register time. Methods are goroutine-safe.
type PeerCache struct {
	mu    stdsync.Mutex
	self  *bamboov1.Peer
	peers map[string]*bamboov1.Peer
}

// New constructs a PeerCache seeded with the initial peer set returned
// by Coordinator.Register.
func New(self *bamboov1.Peer, initial []*bamboov1.Peer) *PeerCache {
	c := &PeerCache{
		self:  self,
		peers: make(map[string]*bamboov1.Peer, len(initial)),
	}
	for _, p := range initial {
		if p == nil || p.GetId() == "" || p.GetId() == self.GetId() {
			continue
		}
		c.peers[p.GetId()] = p
	}
	return c
}

// Apply mutates the cache in response to one watch event. Returns true
// when the on-the-wire view changed (i.e., the device should be
// re-applied). PolicyChanged and RelaysChanged are accepted but produce
// no device-level diff in Phase 1.
func (c *PeerCache) Apply(event *bamboov1.WatchPeersEvent) bool {
	if event == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	switch e := event.GetEvent().(type) {
	case *bamboov1.WatchPeersEvent_PeerAdded:
		p := e.PeerAdded.GetPeer()
		if p == nil || p.GetId() == "" || p.GetId() == c.self.GetId() {
			return false
		}
		c.peers[p.GetId()] = p
		return true

	case *bamboov1.WatchPeersEvent_PeerUpdated:
		p := e.PeerUpdated.GetPeer()
		if p == nil || p.GetId() == "" || p.GetId() == c.self.GetId() {
			return false
		}
		c.peers[p.GetId()] = p
		return true

	case *bamboov1.WatchPeersEvent_PeerRemoved:
		id := e.PeerRemoved.GetPeerId()
		if _, ok := c.peers[id]; !ok {
			return false
		}
		delete(c.peers, id)
		return true

	default:
		return false
	}
}

// Snapshot returns the cached self + a copy of the others. The
// returned slice is independent so the caller can iterate safely while
// new events arrive.
func (c *PeerCache) Snapshot() (*bamboov1.Peer, []*bamboov1.Peer) {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]*bamboov1.Peer, 0, len(c.peers))
	for _, p := range c.peers {
		out = append(out, p)
	}
	return c.self, out
}

// Self returns the immutable self peer.
func (c *PeerCache) Self() *bamboov1.Peer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.self
}

// BuildDeviceConfig is a convenience that reduces the current cache
// snapshot into a wg.DeviceConfig. Equivalent to constructing a
// RegisterResponse from the cache and calling wg.BuildDeviceConfig.
func (c *PeerCache) BuildDeviceConfig(priv wg.PrivateKey) (*wg.DeviceConfig, error) {
	self, peers := c.Snapshot()
	resp := &bamboov1.RegisterResponse{
		Self:  self,
		Peers: peers,
	}
	return wg.BuildDeviceConfig(priv, resp)
}
