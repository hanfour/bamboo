// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
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

// liftRevisionForIngressTx lifts the tenant to policy revision >= 1 when
// a new peer carries tag:ingress and the tenant is still at revision 0.
// An ingress peer at revision 0 would be invisible to the ingress gate on
// clients: at revision 0 they fall back to a peer's /32 when AllowedIps
// is empty. It runs inside the peer's insert transaction, so a failure
// rolls the peer (and the pre-auth key redemption) back. Returns the new
// revision, or 0 when it did not bump; the caller publishes
// PolicyChanged after commit.
func liftRevisionForIngressTx(ctx context.Context, q db.Querier, tenantID uuid.UUID, p *repo.Peer) (int64, error) {
	if !isIngressPeer(p) {
		return 0, nil
	}
	policies := repo.NewPolicies(q)
	rec, err := policies.Get(ctx, tenantID)
	switch {
	case errors.Is(err, repo.ErrNotFound):
	case err != nil:
		return 0, err
	case rec.Revision > 0:
		return 0, nil
	}
	return policies.Bump(ctx, tenantID)
}

// bumpIfIngressLivenessChangedTx bumps the policy revision, inside the
// status change's transaction, when moving peer into or out of
// "disabled" changes public-service liveness: peer is the ingress peer,
// or it hosts an enabled, approved service. online <-> offline never
// does. Returns the new revision, or 0 when it did not bump; the caller
// publishes PolicyChanged after commit.
func bumpIfIngressLivenessChangedTx(ctx context.Context, q db.Querier, peer *repo.Peer, newStatus string) (int64, error) {
	if (peer.Status == "disabled") == (newStatus == "disabled") {
		return 0, nil
	}
	affects := isIngressPeer(peer)
	if !affects {
		hosts, err := repo.NewIngressServices(q).EnabledApprovedForPeer(ctx, peer.ID)
		if err != nil {
			return 0, err
		}
		affects = hosts
	}
	if !affects {
		return 0, nil
	}
	return repo.NewPolicies(q).Bump(ctx, peer.TenantID)
}

// IngressGate is the public-services view a Register applies, exported so
// the policy simulator renders the same matrix peers receive.
type IngressGate struct {
	view    ingressView
	targets []policy.IngressTarget
}

// NewIngressGate builds the gate from the tenant's live routes.
func NewIngressGate(routes []repo.IngressRoute) IngressGate {
	return IngressGate{view: newIngressView(routes), targets: ingressTargets(routes)}
}

// Policy returns p with the generated ingress rules applied (unchanged
// for a zero-rule policy, see policy.WithIngressRules).
func (g IngressGate) Policy(p *policy.Policy) *policy.Policy {
	return policy.WithIngressRules(p, g.targets)
}

// Permits reports whether the zero-rule ingress gate lets src reach dst
// under p (the policy returned by Policy). Tenants with rules always
// pass here; the generated rules gate them.
func (g IngressGate) Permits(p *policy.Policy, src, dst *repo.Peer) bool {
	return !policy.AllowsAll(p) || g.view.allowAllPermits(src, dst)
}
