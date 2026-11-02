// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

func TestIngressTargets_SkipsBadIPs(t *testing.T) {
	sid := uuid.New()
	got := ingressTargets([]repo.IngressRoute{
		{ServiceID: sid, PeerIP: "100.64.0.5", TargetPort: 47800},
		{ServiceID: uuid.New(), PeerIP: "not-an-ip", TargetPort: 47800},
	})
	if len(got) != 1 || got[0].ServiceID != sid.String() || got[0].Port != 47800 || got[0].PeerIP.String() != "100.64.0.5" {
		t.Errorf("ingressTargets = %+v", got)
	}
}

func TestAllowedIPsFor_GeneratedIngressRulesBothDirections(t *testing.T) {
	p := mustParsePolicy(t, `
rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	ingress := &repo.Peer{ID: uuid.New(), IP: "100.64.0.9", Tags: []string{policy.IngressTag}}
	target := &repo.Peer{ID: uuid.New(), IP: "100.64.0.5"}
	if got := allowedIPsFor(p, ingress, target, nat64EgressRoute{}); got != nil {
		t.Fatalf("before liveness: got %v, want nil", got)
	}
	live := policy.WithIngressRules(p, ingressTargets([]repo.IngressRoute{{ServiceID: uuid.New(), PeerIP: "100.64.0.5", TargetPort: 47800}}))
	if got := allowedIPsFor(live, ingress, target, nat64EgressRoute{}); len(got) == 0 || got[0] != "100.64.0.5/32" {
		t.Errorf("ingress view of target = %v, want [100.64.0.5/32]", got)
	}
	if got := allowedIPsFor(live, target, ingress, nat64EgressRoute{}); len(got) == 0 || got[0] != "100.64.0.9/32" {
		t.Errorf("target view of ingress = %v, want [100.64.0.9/32]", got)
	}
}

// ingressFixture is an ingress peer, a peer with a live service, and a
// bystander peer, all in an allow-all (zero-rule) tenant. Each non-ingress
// peer carries routes and roles that allowedIPsFor would normally emit, so
// the tests can see that the ingress pair only ever gets bare addresses.
func ingressFixture() (ingress, target, bystander *repo.Peer, view ingressView) {
	ingress = &repo.Peer{ID: uuid.New(), IP: "100.64.0.9", IP6: "fd7a:115c:a1e0::9", Tags: []string{policy.IngressTag}}
	target = &repo.Peer{
		ID: uuid.New(), IP: "100.64.0.5", IP6: "fd7a:115c:a1e0::5",
		ApprovedRoutes: []string{"10.0.0.0/24"}, ExitNodeApproved: true,
	}
	bystander = &repo.Peer{ID: uuid.New(), IP: "100.64.0.7", ApprovedRoutes: []string{"10.1.0.0/24"}}
	view = newIngressView([]repo.IngressRoute{{ServiceID: uuid.New(), PeerID: target.ID, PeerIP: target.IP, TargetPort: 47800}})
	return ingress, target, bystander, view
}

func TestIngressView_AllowAllTenantLimitsIngressToLiveTargets(t *testing.T) {
	ingress, target, bystander, view := ingressFixture()
	var allowAll *policy.Policy
	if got := view.allowedIPs(allowAll, ingress, target, nat64EgressRoute{}); !slices.Equal(got, []string{"100.64.0.5/32", "fd7a:115c:a1e0::5/128"}) {
		t.Errorf("ingress view of live target = %v, want its own addresses only", got)
	}
	if got := view.allowedIPs(allowAll, target, ingress, nat64EgressRoute{}); !slices.Equal(got, []string{"100.64.0.9/32", "fd7a:115c:a1e0::9/128"}) {
		t.Errorf("live target view of ingress = %v, want ingress addresses", got)
	}
	if got := view.allowedIPs(allowAll, ingress, bystander, nat64EgressRoute{}); got != nil {
		t.Errorf("ingress view of a non-target = %v, want nil", got)
	}
	if got := view.allowedIPs(allowAll, bystander, ingress, nat64EgressRoute{}); got != nil {
		t.Errorf("non-target view of ingress = %v, want nil", got)
	}
	empty := &policy.Policy{}
	if got := view.allowedIPs(empty, ingress, bystander, nat64EgressRoute{}); got != nil {
		t.Errorf("zero-rule policy: ingress view of a non-target = %v, want nil", got)
	}
}

func TestIngressView_AllowAllTenantWithoutLiveServices(t *testing.T) {
	ingress, target, _, _ := ingressFixture()
	if got := newIngressView(nil).allowedIPs(nil, ingress, target, nat64EgressRoute{}); got != nil {
		t.Errorf("no live services: ingress view = %v, want nil", got)
	}
}

func TestIngressView_NonIngressPeersKeepAllowAll(t *testing.T) {
	_, target, bystander, view := ingressFixture()
	src := &repo.Peer{ID: uuid.New(), IP: "100.64.0.3", UsingExitNodePeerID: &target.ID}
	for _, dst := range []*repo.Peer{target, bystander} {
		want := allowedIPsFor(nil, src, dst, nat64EgressRoute{})
		if got := view.allowedIPs(nil, src, dst, nat64EgressRoute{}); !slices.Equal(got, want) {
			t.Errorf("non-ingress pair changed: got %v, want %v", got, want)
		}
	}
	if got := view.allowedIPs(nil, src, target, nat64EgressRoute{}); !slices.Contains(got, "0.0.0.0/0") || !slices.Contains(got, "10.0.0.0/24") {
		t.Errorf("routes and exit node must survive for non-ingress peers: %v", got)
	}
}

func TestIngressView_RuleTenantDefersToPolicy(t *testing.T) {
	ingress, _, bystander, view := ingressFixture()
	p := mustParsePolicy(t, `
rule "all" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`)
	if got := view.allowedIPs(p, ingress, bystander, nat64EgressRoute{}); !slices.Equal(got, []string{"100.64.0.7/32"}) {
		t.Errorf("authored allow-all rule: ingress view of bystander = %v, want its address only", got)
	}
}

func TestAllowedIPsFor_IngressPairGetsOnlyPeerAddresses(t *testing.T) {
	ingress, target, _, _ := ingressFixture()
	egress := nat64EgressRoute{enabled: true, prefix: "64:ff9b::/96", egressID: target.ID}
	ingress.UsingExitNodePeerID = &target.ID
	if got := allowedIPsFor(nil, ingress, target, egress); !slices.Equal(got, []string{"100.64.0.5/32", "fd7a:115c:a1e0::5/128"}) {
		t.Errorf("ingress -> target = %v; routes, exit and NAT64 must not leak", got)
	}
	ingress.ApprovedRoutes = []string{"192.168.9.0/24"}
	ingress.ExitNodeApproved = true
	target.UsingExitNodePeerID = &ingress.ID
	egress.egressID = ingress.ID
	if got := allowedIPsFor(nil, target, ingress, egress); !slices.Equal(got, []string{"100.64.0.9/32", "fd7a:115c:a1e0::9/128"}) {
		t.Errorf("target -> ingress = %v; routes, exit and NAT64 must not leak", got)
	}
}
