// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/hanfour/bamboo/clients/core/device"
	"github.com/hanfour/bamboo/clients/core/wg"
)

type compositionDevice struct {
	cfg  *wg.DeviceConfig
	fail bool
}

func (d *compositionDevice) Apply(_ context.Context, cfg *wg.DeviceConfig) error {
	d.cfg = cloneDesired(cfg)
	if d.fail {
		return errors.New("injected apply failure")
	}
	return nil
}
func (*compositionDevice) Stats() ([]device.PeerStats, error) { return nil, nil }
func (*compositionDevice) Close() error                       { return nil }

func TestCompositionRetainsAuthorityAfterFailure(t *testing.T) {
	ctx := context.Background()
	d := &compositionDevice{}
	r := &deviceReapplier{dev: d}
	key := wg.PublicKey{1}
	cfg := &wg.DeviceConfig{Peers: []wg.PeerConfig{{PublicKey: key, Endpoint: "direct"}}}
	if err := r.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Peers[0].Endpoint = "caller mutation"
	if err := r.Reapply(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if d.cfg.Peers[0].Endpoint != "direct" {
		t.Fatal("caller mutated authority")
	}
	d.fail = true
	if err := r.Apply(ctx, &wg.DeviceConfig{}); err == nil {
		t.Fatal("expected failure")
	}
	d.fail = false
	if err := r.Reapply(ctx, map[string]string{key.Base64(): "relay"}); err != nil {
		t.Fatal(err)
	}
	if len(d.cfg.Peers) != 0 {
		t.Fatal("relay resurrected removed peer")
	}
}

func TestCompositionRejectsStaleRelayGeneration(t *testing.T) {
	ctx := context.Background()
	d := &compositionDevice{}
	r := &deviceReapplier{dev: d}
	key := wg.PublicKey{1}
	if err := r.Apply(ctx, &wg.DeviceConfig{Peers: []wg.PeerConfig{{PublicKey: key}}}); err != nil {
		t.Fatal(err)
	}
	old := r.bound()
	if err := r.replaceRelay(ctx, map[string]string{key.Base64(): "new"}); err != nil {
		t.Fatal(err)
	}
	if err := old.Reapply(ctx, map[string]string{key.Base64(): "old"}); err != nil {
		t.Fatal(err)
	}
	if d.cfg.Peers[0].Endpoint != "new" {
		t.Fatal("stale worker overwrote relay")
	}
	d.fail = true
	if err := r.Reapply(ctx, map[string]string{key.Base64(): "failed"}); err == nil {
		t.Fatal("expected failure")
	}
	d.fail = false
	if err := r.Apply(ctx, r.base); err != nil {
		t.Fatal(err)
	}
	if d.cfg.Peers[0].Endpoint != "new" {
		t.Fatal("failed overlay committed")
	}
}

func TestCompositionMergesFallbackAndCopiesOverlay(t *testing.T) {
	ctx := context.Background()
	d := &compositionDevice{}
	a, b := wg.PublicKey{1}, wg.PublicKey{2}
	r := &deviceReapplier{dev: d, overrides: map[string]string{a.Base64(): "initial"}}
	cfg := &wg.DeviceConfig{Peers: []wg.PeerConfig{{PublicKey: a}, {PublicKey: b, Endpoint: "direct"}}}
	if err := r.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	next := map[string]string{b.Base64(): "fallback"}
	if err := r.bound().Reapply(ctx, next); err != nil {
		t.Fatal(err)
	}
	next[b.Base64()] = "mutated"
	if err := r.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if d.cfg.Peers[0].Endpoint != "initial" || d.cfg.Peers[1].Endpoint != "fallback" {
		t.Fatal("fallback lost or aliased overlays")
	}
	if err := r.replaceRelay(ctx, map[string]string{b.Base64(): "replacement"}); err != nil {
		t.Fatal(err)
	}
	if d.cfg.Peers[0].Endpoint != "" || d.cfg.Peers[1].Endpoint != "replacement" {
		t.Fatal("session replacement retained old overlays")
	}
}

func TestCompositionConcurrentAuthorityAndRelay(t *testing.T) {
	ctx := context.Background()
	d := &compositionDevice{}
	key := wg.PublicKey{1}
	r := &deviceReapplier{dev: d}
	if err := r.Apply(ctx, &wg.DeviceConfig{Peers: []wg.PeerConfig{{PublicKey: key}}}); err != nil {
		t.Fatal(err)
	}
	worker := r.bound()
	done := make(chan error, 2)
	go func() { done <- r.Apply(ctx, &wg.DeviceConfig{}) }()
	go func() { done <- worker.Reapply(ctx, map[string]string{key.Base64(): "relay"}) }()
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if len(d.cfg.Peers) != 0 {
		t.Fatal("concurrent relay resurrected peer")
	}
}
