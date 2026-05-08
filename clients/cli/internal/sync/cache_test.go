// SPDX-License-Identifier: Apache-2.0

package sync_test

import (
	"testing"

	"github.com/hanfour/bamboo/clients/cli/internal/sync"
	"github.com/hanfour/bamboo/clients/core/wg"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

func peer(id, ip string) *bamboov1.Peer {
	priv, _ := wg.GeneratePrivateKey()
	return &bamboov1.Peer{
		Id:                 id,
		Ip:                 ip,
		Hostname:           "host-" + id,
		WireguardPublicKey: priv.PublicKey().Base64(),
	}
}

func TestNew_DropsSelfFromPeerSet(t *testing.T) {
	self := peer("self", "100.64.0.1")
	other := peer("other", "100.64.0.2")
	c := sync.New(self, []*bamboov1.Peer{self, other, nil})
	_, peers := c.Snapshot()
	if len(peers) != 1 || peers[0].GetId() != "other" {
		t.Errorf("snapshot = %v, want [other]", idsOf(peers))
	}
}

func TestApply_PeerAdded(t *testing.T) {
	c := sync.New(peer("self", "100.64.0.1"), nil)
	ev := &bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_PeerAdded{
			PeerAdded: &bamboov1.PeerAdded{Peer: peer("a", "100.64.0.2")},
		},
	}
	if !c.Apply(ev) {
		t.Fatal("expected Apply to report change")
	}
	_, peers := c.Snapshot()
	if len(peers) != 1 || peers[0].GetId() != "a" {
		t.Errorf("snapshot = %v, want [a]", idsOf(peers))
	}
}

func TestApply_PeerAddedSelfIsIgnored(t *testing.T) {
	self := peer("self", "100.64.0.1")
	c := sync.New(self, nil)
	ev := &bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_PeerAdded{
			PeerAdded: &bamboov1.PeerAdded{Peer: self},
		},
	}
	if c.Apply(ev) {
		t.Errorf("self echo should not report change")
	}
	if _, peers := c.Snapshot(); len(peers) != 0 {
		t.Errorf("self echo polluted the cache: %v", idsOf(peers))
	}
}

func TestApply_PeerUpdatedReplacesEntry(t *testing.T) {
	self := peer("self", "100.64.0.1")
	c := sync.New(self, []*bamboov1.Peer{peer("a", "100.64.0.2")})
	updated := peer("a", "100.64.0.99")
	if !c.Apply(&bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_PeerUpdated{
			PeerUpdated: &bamboov1.PeerUpdated{Peer: updated},
		},
	}) {
		t.Fatal("expected change")
	}
	_, peers := c.Snapshot()
	if len(peers) != 1 || peers[0].GetIp() != "100.64.0.99" {
		t.Errorf("snapshot = %v, want a@100.64.0.99", peers)
	}
}

func TestApply_PeerRemoved(t *testing.T) {
	c := sync.New(peer("self", "100.64.0.1"), []*bamboov1.Peer{peer("a", "100.64.0.2")})
	if !c.Apply(&bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_PeerRemoved{
			PeerRemoved: &bamboov1.PeerRemoved{PeerId: "a"},
		},
	}) {
		t.Fatal("expected change")
	}
	if _, peers := c.Snapshot(); len(peers) != 0 {
		t.Errorf("expected empty after remove, got %v", idsOf(peers))
	}
}

func TestApply_RemoveUnknownIsNoop(t *testing.T) {
	c := sync.New(peer("self", "100.64.0.1"), nil)
	if c.Apply(&bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_PeerRemoved{
			PeerRemoved: &bamboov1.PeerRemoved{PeerId: "ghost"},
		},
	}) {
		t.Errorf("removing an unknown peer should not report change")
	}
}

func TestApply_NilEventIsNoop(t *testing.T) {
	c := sync.New(peer("self", "100.64.0.1"), nil)
	if c.Apply(nil) {
		t.Error("nil event should not report change")
	}
}

func TestApply_PolicyChangedDoesNotMutateDeviceView(t *testing.T) {
	c := sync.New(peer("self", "100.64.0.1"), []*bamboov1.Peer{peer("a", "100.64.0.2")})
	if c.Apply(&bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_PolicyChanged{
			PolicyChanged: &bamboov1.PolicyChanged{PolicyRevision: 7},
		},
	}) {
		t.Errorf("PolicyChanged should not request a device re-apply (Phase 1)")
	}
}

func TestBuildDeviceConfig_RoundTrip(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	other := peer("a", "100.64.0.2")
	c := sync.New(peer("self", "100.64.0.1"), []*bamboov1.Peer{other})

	cfg, err := c.BuildDeviceConfig(priv)
	if err != nil {
		t.Fatalf("BuildDeviceConfig: %v", err)
	}
	if cfg.Address.String() != "100.64.0.1/32" {
		t.Errorf("Address = %v, want 100.64.0.1/32", cfg.Address)
	}
	if len(cfg.Peers) != 1 {
		t.Fatalf("len(Peers) = %d, want 1", len(cfg.Peers))
	}
	if cfg.Peers[0].AllowedIPs[0].String() != "100.64.0.2/32" {
		t.Errorf("peer 0 AllowedIPs[0] = %v, want 100.64.0.2/32", cfg.Peers[0].AllowedIPs[0])
	}
}

func idsOf(peers []*bamboov1.Peer) []string {
	out := make([]string, 0, len(peers))
	for _, p := range peers {
		out = append(out, p.GetId())
	}
	return out
}
