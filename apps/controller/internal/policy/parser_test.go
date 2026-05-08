// SPDX-License-Identifier: AGPL-3.0-or-later

package policy_test

import (
	"strings"
	"testing"

	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

const goodSource = `
rule "allow-dev-to-staging" {
  action       = "allow"
  description  = "Developers can reach staging services"
  sources      = ["tag:dev", "group:engineering"]
  destinations = ["tag:staging:443", "tag:staging:5432"]
}

rule "ci-to-internet" {
  action       = "allow"
  sources      = ["tag:ci"]
  destinations = ["cidr:0.0.0.0/0:443"]
}

rule "deny-everyone-from-prod-secrets" {
  action       = "deny"
  sources      = ["*"]
  destinations = ["tag:prod-secrets:*"]
}
`

func TestParse_Happy(t *testing.T) {
	p, err := policy.Parse("test.hcl", goodSource)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := len(p.Rules); got != 3 {
		t.Fatalf("rules = %d, want 3", got)
	}

	r0 := p.Rules[0]
	if r0.ID != "allow-dev-to-staging" {
		t.Errorf("rule 0 ID = %q", r0.ID)
	}
	if r0.Action != policy.ActionAllow {
		t.Errorf("rule 0 Action = %v, want allow", r0.Action)
	}
	if got := len(r0.Sources); got != 2 {
		t.Errorf("rule 0 sources = %d, want 2", got)
	}
	if r0.Sources[0].Kind != policy.MatcherTag || r0.Sources[0].Name != "dev" {
		t.Errorf("rule 0 source 0 = %+v", r0.Sources[0])
	}
	if r0.Sources[1].Kind != policy.MatcherGroup || r0.Sources[1].Name != "engineering" {
		t.Errorf("rule 0 source 1 = %+v", r0.Sources[1])
	}
	if r0.Destinations[0].Kind != policy.MatcherTag || r0.Destinations[0].Name != "staging" {
		t.Errorf("rule 0 dest 0 = %+v", r0.Destinations[0])
	}
	if !r0.Destinations[0].Ports.Matches(443) || r0.Destinations[0].Ports.Matches(80) {
		t.Errorf("rule 0 dest 0 ports unexpected: %s", r0.Destinations[0].Ports)
	}

	r2 := p.Rules[2]
	if r2.Action != policy.ActionDeny {
		t.Errorf("rule 2 Action = %v, want deny", r2.Action)
	}
	if r2.Sources[0].Kind != policy.MatcherWildcard {
		t.Errorf("rule 2 source 0 = %+v", r2.Sources[0])
	}
	if !r2.Destinations[0].Ports.AllPorts {
		t.Errorf("rule 2 dest 0 ports = %s, want *", r2.Destinations[0].Ports)
	}
}

func TestParse_AggregatesDiagnostics(t *testing.T) {
	src := `
rule "bad-action" {
  action       = "permit"
  sources      = ["tag:dev"]
  destinations = ["tag:staging:443"]
}

rule "missing-everything" {
}

rule "bad-source" {
  action       = "allow"
  sources      = ["nope:foo"]
  destinations = ["tag:s:80"]
}
`
	_, err := policy.Parse("bad.hcl", src)
	if err == nil {
		t.Fatal("expected aggregated error")
	}
	// hcl.Diagnostics flattens to a short string; expand to the full list
	// so all diagnostics are visible to assertions.
	type diagsBag interface{ Errs() []error }
	full := err.Error()
	if bag, ok := err.(diagsBag); ok {
		for _, e := range bag.Errs() {
			full += "\n" + e.Error()
		}
	}
	for _, want := range []string{"invalid action", "missing sources", "missing destinations"} {
		if !strings.Contains(full, want) {
			t.Errorf("error message missing %q\nfull message: %s", want, full)
		}
	}
}

func TestParse_DestinationRequiresPortSpec(t *testing.T) {
	src := `
rule "no-port" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:staging"]
}
`
	if _, err := policy.Parse("noport.hcl", src); err == nil {
		t.Error("expected error for destination without port spec")
	}
}

func TestParse_CIDRMatcher(t *testing.T) {
	src := `
rule "vpn-allow" {
  action       = "allow"
  sources      = ["cidr:10.0.0.0/8"]
  destinations = ["cidr:192.168.0.0/16:22,80,443"]
}
`
	p, err := policy.Parse("cidr.hcl", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := p.Rules[0].Sources[0].CIDR.String(); got != "10.0.0.0/8" {
		t.Errorf("CIDR source = %q", got)
	}
	if got := len(p.Rules[0].Destinations[0].Ports.Ports); got != 3 {
		t.Errorf("port list len = %d, want 3", got)
	}
}
