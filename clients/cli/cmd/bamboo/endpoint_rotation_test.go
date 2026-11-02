// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/netip"
	"testing"
	"time"

	clientsync "github.com/hanfour/bamboo/clients/cli/internal/sync"
	"github.com/hanfour/bamboo/clients/core/device"
	"github.com/hanfour/bamboo/clients/core/wg"
)

type rotationDevice struct {
	compositionDevice
	stats []device.PeerStats
}

func (d *rotationDevice) Stats() ([]device.PeerStats, error) { return d.stats, nil }

func rotatingReapplier(clock *time.Time) (*deviceReapplier, *rotationDevice) {
	d := &rotationDevice{}
	local := func() []netip.Prefix { return []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")} }
	rot := clientsync.NewCandidateRotator(local, func() time.Time { return *clock })
	return &deviceReapplier{dev: d, rotator: rot}, d
}

func TestReapplierSelectsAndRotatesCandidates(t *testing.T) {
	ctx := context.Background()
	clock := time.Unix(1_700_000_000, 0)
	r, d := rotatingReapplier(&clock)
	key := wg.PublicKey{3}
	cands := []string{"192.168.1.108:51820", "198.51.100.1:51820", "198.51.100.2:51820"}
	cfg := &wg.DeviceConfig{Peers: []wg.PeerConfig{{PublicKey: key, Endpoint: cands[0], Candidates: cands}}}
	if err := r.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if got := d.cfg.Peers[0].Endpoint; got != cands[1] {
		t.Fatalf("restart picked %s, want first public", got)
	}
	d.stats = []device.PeerStats{{PublicKey: key.Base64(), Endpoint: cands[1]}}
	clock = clock.Add(clientsync.CandidateProbeTimeout)
	if err := r.rotate(ctx); err != nil {
		t.Fatal(err)
	}
	if got := d.cfg.Peers[0].Endpoint; got != cands[2] {
		t.Fatalf("rotation applied %s", got)
	}
	if err := r.Reapply(ctx, nil); err != nil || d.cfg.Peers[0].Endpoint != cands[2] {
		t.Fatal("relay reapply lost rotated endpoint")
	}
	if err := r.Apply(ctx, cfg); err != nil || d.cfg.Peers[0].Endpoint != cands[2] {
		t.Fatal("unchanged controller config reset rotation")
	}
}

func TestReapplierRelayOverrideStopsRotation(t *testing.T) {
	ctx := context.Background()
	clock := time.Unix(1_700_000_000, 0)
	r, d := rotatingReapplier(&clock)
	key := wg.PublicKey{3}
	cands := []string{"198.51.100.1:51820", "198.51.100.2:51820"}
	cfg := &wg.DeviceConfig{Peers: []wg.PeerConfig{{PublicKey: key, Endpoint: cands[0], Candidates: cands}}}
	if err := r.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := r.Reapply(ctx, map[string]string{key.Base64(): "127.0.0.1:40000"}); err != nil {
		t.Fatal(err)
	}
	d.stats = []device.PeerStats{{PublicKey: key.Base64(), Endpoint: "127.0.0.1:40000"}}
	for range 5 {
		clock = clock.Add(clientsync.CandidateProbeTimeout)
		if err := r.rotate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := d.cfg.Peers[0].Endpoint; got != "127.0.0.1:40000" {
		t.Fatalf("rotation fought relay: %s", got)
	}
	if err := r.restoreCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.rotator.Select(r.base).Peers[0].Endpoint; got != cands[0] {
		t.Fatalf("rotation state moved while relayed: %s", got)
	}
}

func TestReapplierWithoutRotatorKeepsControllerEndpoint(t *testing.T) {
	ctx := context.Background()
	d := &compositionDevice{}
	r := &deviceReapplier{dev: d}
	cands := []string{"192.168.1.108:51820", "198.51.100.1:51820"}
	cfg := &wg.DeviceConfig{Peers: []wg.PeerConfig{{PublicKey: wg.PublicKey{3}, Endpoint: cands[0], Candidates: cands}}}
	if err := r.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := r.rotate(ctx); err != nil {
		t.Fatal(err)
	}
	if d.cfg.Peers[0].Endpoint != cands[0] {
		t.Fatal("nil rotator changed endpoint")
	}
}
