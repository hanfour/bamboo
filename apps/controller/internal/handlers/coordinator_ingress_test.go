// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
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
