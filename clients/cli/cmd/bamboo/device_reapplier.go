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
}

func cloneDesired(cfg *wg.DeviceConfig) *wg.DeviceConfig {
	out := *cfg
	out.Peers = append([]wg.PeerConfig(nil), cfg.Peers...)
	for i := range out.Peers {
		out.Peers[i].AllowedIPs = append([]netip.Prefix(nil), cfg.Peers[i].AllowedIPs...)
	}
	return &out
}

func (r *deviceReapplier) Apply(ctx context.Context, cfg *wg.DeviceConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.base = cloneDesired(cfg)
	r.overrides = r.filter(r.overrides)
	return r.dev.Apply(ctx, clientsync.ApplyEndpointOverrides(r.base, r.overrides))
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
	if err := r.dev.Apply(ctx, clientsync.ApplyEndpointOverrides(r.base, next)); err != nil {
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
