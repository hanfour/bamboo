// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/netip"
	"sync"

	clientsync "github.com/hanfour/bamboo/clients/cli/internal/sync"
	"github.com/hanfour/bamboo/clients/core/device"
	"github.com/hanfour/bamboo/clients/core/wg"
)

type deviceReapplier struct {
	mu         sync.Mutex
	dev        device.Device
	base       *wg.DeviceConfig
	overrides  map[string]string
	generation uint64
	// rotator picks each peer's direct endpoint from its candidates
	// (see clientsync.CandidateRotator). Nil keeps the controller's
	// first endpoint. Relay overrides are layered on top of its choice.
	rotator *clientsync.CandidateRotator
}

func cloneDesired(cfg *wg.DeviceConfig) *wg.DeviceConfig {
	out := *cfg
	out.Peers = append([]wg.PeerConfig(nil), cfg.Peers...)
	for i := range out.Peers {
		out.Peers[i].AllowedIPs = append([]netip.Prefix(nil), cfg.Peers[i].AllowedIPs...)
		out.Peers[i].Candidates = append([]string(nil), cfg.Peers[i].Candidates...)
	}
	return &out
}

func (r *deviceReapplier) Apply(ctx context.Context, cfg *wg.DeviceConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.base = cloneDesired(cfg)
	r.overrides = r.filter(r.overrides)
	return r.dev.Apply(ctx, r.desired(r.overrides))
}

// desired layers relay overrides on the rotator's direct endpoints.
// Select is idempotent for an unchanged base, so every apply path can
// call it.
//
// The rotator's state is not rolled back when dev.Apply fails: after a
// failed rotate() it is one candidate ahead of the kernel. That is
// harmless. Every later apply (the next rotation, a controller update,
// a relay change) sends Select's current choice, so the kernel catches
// up, and the probe window for that candidate started slightly early.
func (r *deviceReapplier) desired(overrides map[string]string) *wg.DeviceConfig {
	cfg := r.base
	if r.rotator != nil {
		cfg = r.rotator.Select(cfg)
	}
	return clientsync.ApplyEndpointOverrides(cfg, overrides)
}

// rotate feeds device stats to the rotator and re-applies when a peer
// moved to its next candidate. Peers on a relay override are passed as
// relayed so the rotator leaves them alone.
func (r *deviceReapplier) rotate(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rotator == nil || r.base == nil {
		return nil
	}
	stats, err := r.dev.Stats()
	if err != nil {
		return fmt.Errorf("wg stats: %w", err)
	}
	if !r.rotator.Observe(stats, r.overrides) {
		return nil
	}
	return r.reapply(ctx, r.overrides)
}

func (r *deviceReapplier) filter(overrides map[string]string) map[string]string {
	out := make(map[string]string)
	if r.base == nil {
		return out
	}
	for _, p := range r.base.Peers {
		key := p.PublicKey.Base64()
		if ep := overrides[key]; ep != "" {
			out[key] = ep
		}
	}
	return out
}

func (r *deviceReapplier) reapply(ctx context.Context, overrides map[string]string) error {
	if r.base == nil {
		return fmt.Errorf("device configuration unavailable")
	}
	next := r.filter(overrides)
	if err := r.dev.Apply(ctx, r.desired(next)); err != nil {
		return err
	}
	r.overrides = next
	return nil
}

func (r *deviceReapplier) Reapply(ctx context.Context, overrides map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	merged := r.filter(r.overrides)
	for key, endpoint := range overrides {
		merged[key] = endpoint
	}
	return r.reapply(ctx, merged)
}

func (r *deviceReapplier) replaceRelay(ctx context.Context, overrides map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.reapply(ctx, overrides); err != nil {
		return err
	}
	r.generation++
	return nil
}

type relayGeneration struct {
	owner      *deviceReapplier
	generation uint64
}

func (r *deviceReapplier) bound() clientsync.Reapplier {
	r.mu.Lock()
	defer r.mu.Unlock()
	return relayGeneration{r, r.generation}
}

func (g relayGeneration) Reapply(ctx context.Context, overrides map[string]string) error {
	r := g.owner
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.generation != g.generation {
		return nil
	}
	merged := r.filter(r.overrides)
	for key, endpoint := range overrides {
		merged[key] = endpoint
	}
	return r.reapply(ctx, merged)
}

func (r *deviceReapplier) restoreCurrent(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reapply(ctx, r.overrides)
}
