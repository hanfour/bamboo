// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// nat64EgressHealthInterval is how often the reaper recomputes each
// dns64-on tenant's active NAT64 egress and fails over on a change. 30s
// matches the relay reaper + the client HeartbeatInterval, so a stale
// egress (≥90s = 3 missed heartbeats) is detected within one tick of
// crossing the threshold (NAT64 Phase C3).
var nat64EgressHealthInterval = 30 * time.Second

// StartNAT64EgressHealthReaper launches the C3 egress-failover goroutine:
// every nat64EgressHealthInterval it marks stale egresses unhealthy and
// bumps the tenant policy revision when the selected egress changes, so
// failover does not wait for an unrelated re-register. Mirrors
// StartRelayHealthReaper (immediate sweep + ticker; exits on ctx cancel).
// The lastSelected map is owned by this single goroutine — no lock needed.
func (h *HTTPServer) StartNAT64EgressHealthReaper(ctx context.Context) {
	if h == nil || h.peers == nil || h.coord == nil {
		return
	}
	go func() {
		lastSelected := map[uuid.UUID]uuid.UUID{}
		h.runNAT64EgressHealthSweep(ctx, lastSelected)
		t := time.NewTicker(nat64EgressHealthInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.runNAT64EgressHealthSweep(ctx, lastSelected)
			}
		}
	}()
}

// runNAT64EgressHealthSweep reconciles every dns64-on tenant that has an
// approved egress. Per tenant the work (staleness write + recompute +
// selection-change bump) lives in ReconcileNAT64Egress; here we only
// enumerate + thread the lastSelected map. A reconcile error leaves
// lastSelected[tenant] unchanged so the next sweep retries (self-heal).
func (h *HTTPServer) runNAT64EgressHealthSweep(ctx context.Context, lastSelected map[uuid.UUID]uuid.UUID) {
	tenants, err := h.peers.ListNAT64EgressActiveTenants(ctx)
	if err != nil {
		slog.Warn("nat64 egress reaper: list tenants", "err", err)
		return
	}
	for _, tid := range tenants {
		selected, _, err := h.coord.ReconcileNAT64Egress(ctx, tid, lastSelected[tid])
		if err != nil {
			slog.Warn("nat64 egress reaper: reconcile", "tenant_id", tid, "err", err)
			continue
		}
		lastSelected[tid] = selected
	}
}
