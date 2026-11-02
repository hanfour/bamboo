// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

// isIngressPeer reports whether p is the tenant's public services
// ingress peer (docs/design/2026-11-02-public-services-ingress.md §4.4).
func isIngressPeer(p *repo.Peer) bool { return slices.Contains(p.Tags, policy.IngressTag) }

// liveIngressRoutes returns the tenant's live public services. It skips
// the query when no peer carries tag:ingress: without an ingress peer the
// generated rules match nothing. On a lookup error it logs and returns
// nil, which fails closed for the ingress peer and leaves every other
// pair untouched.
func (h *CoordinatorHandler) liveIngressRoutes(ctx context.Context, tenantID uuid.UUID, peers []*repo.Peer) []repo.IngressRoute {
	if !slices.ContainsFunc(peers, isIngressPeer) {
		return nil
	}
	var routes []repo.IngressRoute
	err := db.WithTenant(ctx, h.pool, tenantID, func(q db.Querier) error {
		r, lerr := repo.NewIngressServices(q).ListLiveRoutes(ctx, tenantID)
		routes = r
		return lerr
	})
	if err != nil {
		slog.Warn("load live ingress routes", "tenant_id", tenantID, "err", err)
		return nil
	}
	return routes
}

// ingressTargets converts live routes to policy targets, skipping rows
// whose IP does not parse.
func ingressTargets(routes []repo.IngressRoute) []policy.IngressTarget {
	out := make([]policy.IngressTarget, 0, len(routes))
	for _, rt := range routes {
		addr, err := netip.ParseAddr(rt.PeerIP)
		if err != nil || rt.TargetPort < 1 || rt.TargetPort > 65535 {
			continue
		}
		out = append(out, policy.IngressTarget{ServiceID: rt.ServiceID.String(), PeerIP: addr, Port: uint16(rt.TargetPort)})
	}
	return out
}

// ingressView is the per-register public-services state of a tenant:
// the peers that host at least one live service.
type ingressView struct {
	liveTargets map[uuid.UUID]struct{}
}

func newIngressView(routes []repo.IngressRoute) ingressView {
	v := ingressView{liveTargets: make(map[uuid.UUID]struct{}, len(routes))}
	for _, rt := range routes {
		v.liveTargets[rt.PeerID] = struct{}{}
	}
	return v
}

func (v ingressView) isLiveTarget(p *repo.Peer) bool {
	_, ok := v.liveTargets[p.ID]
	return ok
}

// allowedIPs is allowedIPsFor plus the ingress gate for allow-all
// tenants. A zero-rule policy lets every pair through, which would hand
// the internet-facing ingress peer the whole mesh; there, a pair with
// the ingress peer on either side passes only when the other side hosts
// a live service. Tenants with authored rules are gated by the policy,
// which carries the generated ingress rules.
func (v ingressView) allowedIPs(p *policy.Policy, src, dst *repo.Peer, egress nat64EgressRoute) []string {
	if policy.AllowsAll(p) && !v.allowAllPermits(src, dst) {
		return nil
	}
	return allowedIPsFor(p, src, dst, egress)
}

func (v ingressView) allowAllPermits(src, dst *repo.Peer) bool {
	return (!isIngressPeer(src) || v.isLiveTarget(dst)) && (!isIngressPeer(dst) || v.isLiveTarget(src))
}
