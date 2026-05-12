// SPDX-License-Identifier: AGPL-3.0-or-later

package policy_test

import (
	"testing"

	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

func TestAllow_NilPolicyIsFullMesh(t *testing.T) {
	src := policy.PeerView{Tags: []string{"dev"}}
	dst := policy.PeerView{IP: addr(t, "100.64.0.2"), Tags: []string{"prod"}}
	if !policy.Allow(nil, src, dst) {
		t.Fatalf("nil policy should permit traffic (full mesh)")
	}
}

func TestAllow_EmptyRulesIsFullMesh(t *testing.T) {
	p := &policy.Policy{}
	src := policy.PeerView{Tags: []string{"dev"}}
	dst := policy.PeerView{IP: addr(t, "100.64.0.2"), Tags: []string{"prod"}}
	if !policy.Allow(p, src, dst) {
		t.Fatalf("empty policy should permit traffic (full mesh)")
	}
}

func TestAllow_TagToTagAllow(t *testing.T) {
	p := mustParse(t, `
rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	src := policy.PeerView{Tags: []string{"dev", "laptop"}}
	dst := policy.PeerView{IP: addr(t, "100.64.0.5"), Tags: []string{"db"}}
	if !policy.Allow(p, src, dst) {
		t.Fatalf("dev → db should be allowed")
	}
}

func TestAllow_TagMismatchIsDeny(t *testing.T) {
	p := mustParse(t, `
rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	// dst lacks the "db" tag.
	src := policy.PeerView{Tags: []string{"dev"}}
	dst := policy.PeerView{IP: addr(t, "100.64.0.5"), Tags: []string{"web"}}
	if policy.Allow(p, src, dst) {
		t.Fatalf("dev → web should be denied (no matching dst tag)")
	}
}

func TestAllow_SourceTagMismatchIsDeny(t *testing.T) {
	p := mustParse(t, `
rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	src := policy.PeerView{Tags: []string{"prod"}}
	dst := policy.PeerView{IP: addr(t, "100.64.0.5"), Tags: []string{"db"}}
	if policy.Allow(p, src, dst) {
		t.Fatalf("prod → db should be denied (no matching src tag)")
	}
}

func TestAllow_PortSpecificDenyDoesNotShadowAllowOnAllPorts(t *testing.T) {
	// A port-specific deny must not shadow a wildcard-port allow that
	// follows it. At L3 we want "dev can reach prod" because the allow
	// rule covers ports other than 80.
	p := mustParse(t, `
rule "deny-prod-http" {
  action       = "deny"
  sources      = ["tag:dev"]
  destinations = ["tag:prod:80"]
}

rule "allow-prod-all" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:prod:*"]
}`)
	src := policy.PeerView{Tags: []string{"dev"}}
	dst := policy.PeerView{IP: addr(t, "100.64.0.5"), Tags: []string{"prod"}}
	if !policy.Allow(p, src, dst) {
		t.Fatalf("dev → prod should be allowed at L3 (deny is port-specific, allow covers the rest)")
	}
}

func TestAllow_BroadDenyShadowsAllowOnSamePort(t *testing.T) {
	// A wildcard-port deny earlier should shadow a narrower allow.
	p := mustParse(t, `
rule "deny-prod-all" {
  action       = "deny"
  sources      = ["tag:dev"]
  destinations = ["tag:prod:*"]
}

rule "allow-prod-https" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:prod:443"]
}`)
	src := policy.PeerView{Tags: []string{"dev"}}
	dst := policy.PeerView{IP: addr(t, "100.64.0.5"), Tags: []string{"prod"}}
	if policy.Allow(p, src, dst) {
		t.Fatalf("dev → prod should be denied (broad deny shadows narrower allow)")
	}
}

func TestAllow_WildcardSrcAndDst(t *testing.T) {
	p := mustParse(t, `
rule "allow-all" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`)
	src := policy.PeerView{Tags: []string{"anything"}}
	dst := policy.PeerView{IP: addr(t, "100.64.0.7"), Tags: []string{"other"}}
	if !policy.Allow(p, src, dst) {
		t.Fatalf("wildcard rule should permit everything")
	}
}

func TestAllow_CIDRSourceAndDestination(t *testing.T) {
	p := mustParse(t, `
rule "intranet-ssh" {
  action       = "allow"
  sources      = ["cidr:10.0.0.0/8"]
  destinations = ["cidr:192.168.0.0/16:22"]
}`)
	src := policy.PeerView{IP: addr(t, "10.5.5.5")}
	dst := policy.PeerView{IP: addr(t, "192.168.1.10")}
	if !policy.Allow(p, src, dst) {
		t.Fatalf("CIDR-matched pair should be allowed")
	}
	// Source outside the CIDR — denied.
	other := policy.PeerView{IP: addr(t, "172.16.0.1")}
	if policy.Allow(p, other, dst) {
		t.Fatalf("non-matching src CIDR should be denied")
	}
}

func TestAllow_GroupSourceMatcher(t *testing.T) {
	p := mustParse(t, `
rule "eng-to-staging" {
  action       = "allow"
  sources      = ["group:engineering"]
  destinations = ["tag:staging:*"]
}`)
	src := policy.PeerView{Groups: []string{"engineering"}}
	dst := policy.PeerView{IP: addr(t, "100.64.0.9"), Tags: []string{"staging"}}
	if !policy.Allow(p, src, dst) {
		t.Fatalf("group-based src should be allowed")
	}
	// Different group — denied.
	other := policy.PeerView{Groups: []string{"sales"}}
	if policy.Allow(p, other, dst) {
		t.Fatalf("non-matching group should be denied")
	}
}

func TestAllow_NoRuleCoversPairIsDeny(t *testing.T) {
	p := mustParse(t, `
rule "narrow" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	src := policy.PeerView{Tags: []string{"prod"}}
	dst := policy.PeerView{IP: addr(t, "100.64.0.5"), Tags: []string{"web"}}
	if policy.Allow(p, src, dst) {
		t.Fatalf("policy with rules but no matching rule must deny")
	}
}

func TestAllow_DenyOnlyPolicyIsAlwaysDeny(t *testing.T) {
	p := mustParse(t, `
rule "deny-secrets" {
  action       = "deny"
  sources      = ["*"]
  destinations = ["tag:secret:*"]
}`)
	src := policy.PeerView{Tags: []string{"dev"}}
	dst := policy.PeerView{IP: addr(t, "100.64.0.5"), Tags: []string{"secret"}}
	if policy.Allow(p, src, dst) {
		t.Fatalf("policy with only deny rules must not permit traffic")
	}
}
