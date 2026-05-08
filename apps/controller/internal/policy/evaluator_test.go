// SPDX-License-Identifier: AGPL-3.0-or-later

package policy_test

import (
	"net/netip"
	"testing"

	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

func mustParse(t *testing.T, src string) *policy.Policy {
	t.Helper()
	p, err := policy.Parse("test.hcl", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

func addr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr: %v", err)
	}
	return a
}

func TestEvaluate_AllowMatchingTag(t *testing.T) {
	p := mustParse(t, `
rule "dev-to-staging" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:staging:443"]
}`)

	got, rule := p.Evaluate(policy.EvalRequest{
		SrcTags: []string{"dev", "laptop"},
		DstTags: []string{"staging"},
		DstPort: 443,
	})
	if got != policy.ActionAllow {
		t.Errorf("action = %v, want allow", got)
	}
	if rule == nil || rule.ID != "dev-to-staging" {
		t.Errorf("matched rule = %+v", rule)
	}
}

func TestEvaluate_DenyByDefault(t *testing.T) {
	p := mustParse(t, `
rule "narrow" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:staging:443"]
}`)

	got, rule := p.Evaluate(policy.EvalRequest{
		SrcTags: []string{"prod"},
		DstTags: []string{"staging"},
		DstPort: 443,
	})
	if got != policy.ActionDeny {
		t.Errorf("action = %v, want deny", got)
	}
	if rule != nil {
		t.Errorf("expected nil rule on default deny, got %+v", rule)
	}
}

func TestEvaluate_FirstMatchWins(t *testing.T) {
	p := mustParse(t, `
rule "deny-secrets" {
  action       = "deny"
  sources      = ["*"]
  destinations = ["tag:secret:*"]
}

rule "allow-internal" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:443"]
}`)

	got, rule := p.Evaluate(policy.EvalRequest{
		SrcTags: []string{"laptop"},
		DstTags: []string{"secret"},
		DstPort: 443,
	})
	if got != policy.ActionDeny || rule == nil || rule.ID != "deny-secrets" {
		t.Errorf("expected deny by deny-secrets, got action=%v rule=%+v", got, rule)
	}
}

func TestEvaluate_UserMatcher(t *testing.T) {
	p := mustParse(t, `
rule "alice-only" {
  action       = "allow"
  sources      = ["user:alice@example.com"]
  destinations = ["tag:db:5432"]
}`)

	got, _ := p.Evaluate(policy.EvalRequest{
		SrcUser: "alice@example.com",
		DstTags: []string{"db"},
		DstPort: 5432,
	})
	if got != policy.ActionAllow {
		t.Errorf("action = %v, want allow", got)
	}

	got, _ = p.Evaluate(policy.EvalRequest{
		SrcUser: "bob@example.com",
		DstTags: []string{"db"},
		DstPort: 5432,
	})
	if got != policy.ActionDeny {
		t.Errorf("action = %v, want deny for bob", got)
	}
}

func TestEvaluate_CIDRMatcher(t *testing.T) {
	p := mustParse(t, `
rule "intranet-access" {
  action       = "allow"
  sources      = ["cidr:10.0.0.0/8"]
  destinations = ["cidr:192.168.0.0/16:22"]
}`)

	got, _ := p.Evaluate(policy.EvalRequest{
		SrcIP:   addr(t, "10.5.5.5"),
		DstIP:   addr(t, "192.168.1.10"),
		DstPort: 22,
	})
	if got != policy.ActionAllow {
		t.Errorf("action = %v, want allow", got)
	}

	got, _ = p.Evaluate(policy.EvalRequest{
		SrcIP:   addr(t, "172.16.0.1"),
		DstIP:   addr(t, "192.168.1.10"),
		DstPort: 22,
	})
	if got != policy.ActionDeny {
		t.Errorf("action = %v, want deny for non-matching CIDR", got)
	}
}

func TestEvaluate_PortMismatch(t *testing.T) {
	p := mustParse(t, `
rule "narrow-port" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:staging:443"]
}`)
	got, _ := p.Evaluate(policy.EvalRequest{
		SrcTags: []string{"dev"},
		DstTags: []string{"staging"},
		DstPort: 80,
	})
	if got != policy.ActionDeny {
		t.Errorf("action = %v, want deny on port mismatch", got)
	}
}

func TestPortSpec_All(t *testing.T) {
	p := mustParse(t, `
rule "any-port" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:staging:*"]
}`)
	for _, port := range []uint16{1, 22, 443, 65535} {
		got, _ := p.Evaluate(policy.EvalRequest{
			SrcTags: []string{"dev"},
			DstTags: []string{"staging"},
			DstPort: port,
		})
		if got != policy.ActionAllow {
			t.Errorf("port %d: action = %v, want allow", port, got)
		}
	}
}
