// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"log/slog"
	"net/netip"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

// withIngressRules adds the generated public-services rules for the
// tenant's live services (docs/design/2026-11-02-public-services-ingress.md
// §4.4). On a lookup error it logs and returns p unchanged: a failed
// lookup must not black-hole the tenant.
func (h *CoordinatorHandler) withIngressRules(ctx context.Context, tenantID uuid.UUID, p *policy.Policy) *policy.Policy {
	if p == nil || len(p.Rules) == 0 {
		return p
	}
	var routes []repo.IngressRoute
	err := db.WithTenant(ctx, h.pool, tenantID, func(q db.Querier) error {
		r, lerr := repo.NewIngressServices(q).ListLiveRoutes(ctx, tenantID)
		routes = r
		return lerr
	})
	if err != nil {
		slog.Warn("load live ingress routes", "tenant_id", tenantID, "err", err)
		return p
	}
	return policy.WithIngressRules(p, ingressTargets(routes))
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
