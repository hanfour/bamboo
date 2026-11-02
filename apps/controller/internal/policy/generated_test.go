// SPDX-License-Identifier: AGPL-3.0-or-later

package policy_test

import (
	"testing"

	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

func ingressTarget(t *testing.T) policy.IngressTarget {
	return policy.IngressTarget{ServiceID: "svc-1", PeerIP: addr(t, "100.64.0.5"), Port: 47800}
}

func TestWithIngressRules_NilAndEmptyPolicyUnchanged(t *testing.T) {
	targets := []policy.IngressTarget{ingressTarget(t)}
	if got := policy.WithIngressRules(nil, targets); got != nil {
		t.Errorf("nil policy must stay nil (full mesh), got %+v", got)
	}
	empty := &policy.Policy{}
	if got := policy.WithIngressRules(empty, targets); len(got.Rules) != 0 {
		t.Errorf("empty policy must stay rule-less (full mesh), got %d rules", len(got.Rules))
	}
}

func TestWithIngressRules_PrependsAndDoesNotMutate(t *testing.T) {
	p := mustParse(t, `
rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	got := policy.WithIngressRules(p, []policy.IngressTarget{ingressTarget(t)})
	if len(p.Rules) != 1 {
		t.Fatalf("input policy mutated: %d rules", len(p.Rules))
	}
	if len(got.Rules) != 3 {
		t.Fatalf("want 2 generated + 1 authored rules, got %d", len(got.Rules))
	}
	if !policy.IsGenerated(got.Rules[0]) || !policy.IsGenerated(got.Rules[1]) || policy.IsGenerated(got.Rules[2]) {
		t.Errorf("generated rules must come first and be marked: %+v", got.Rules)
	}
	if got.Rules[0].ID != "generated:ingress:svc-1" || got.Rules[1].ID != "generated:ingress:svc-1:return" {
		t.Errorf("rule ids = %q, %q", got.Rules[0].ID, got.Rules[1].ID)
	}
}

func TestWithIngressRules_AllowsBothDirectionsOnlyForTarget(t *testing.T) {
	p := mustParse(t, `
rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	got := policy.WithIngressRules(p, []policy.IngressTarget{ingressTarget(t)})
	ingress := policy.PeerView{IP: addr(t, "100.64.0.9"), Tags: []string{policy.IngressTag}}
	target := policy.PeerView{IP: addr(t, "100.64.0.5")}
	other := policy.PeerView{IP: addr(t, "100.64.0.6")}

	if !policy.Allow(got, ingress, target) {
		t.Error("ingress -> target must be allowed")
	}
	if !policy.Allow(got, target, ingress) {
		t.Error("target -> ingress (return path) must be allowed")
	}
	if policy.Allow(got, ingress, other) {
		t.Error("ingress must not reach a peer without a live service")
	}
	if policy.Allow(p, ingress, target) {
		t.Error("without generated rules the authored policy denies ingress -> target")
	}
	if ok, _ := got.Evaluate(policy.EvalRequest{SrcTags: []string{policy.IngressTag}, DstIP: addr(t, "100.64.0.5"), DstPort: 22}); ok == policy.ActionAllow {
		t.Error("generated rule must cover only the target port")
	}
}
