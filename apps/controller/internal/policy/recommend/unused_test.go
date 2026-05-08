// SPDX-License-Identifier: AGPL-3.0-or-later

package recommend_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hanfour/bamboo/apps/controller/internal/policy"
	"github.com/hanfour/bamboo/apps/controller/internal/policy/recommend"
)

const fixture = `
rule "active-allow" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:staging:443"]
}

rule "stale-allow" {
  action       = "allow"
  description  = "Legacy rule we are not sure anyone needs"
  sources      = ["tag:legacy"]
  destinations = ["tag:archive:443"]
}

rule "deny-secrets" {
  action       = "deny"
  sources      = ["*"]
  destinations = ["tag:secrets:*"]
}
`

func TestUnusedRules_FlagsZeroHitRulesOnly(t *testing.T) {
	p, err := policy.Parse("test.hcl", fixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	since := time.Now().Add(-7 * 24 * time.Hour)
	hits := map[string]uint64{"active-allow": 142}
	// stale-allow and deny-secrets are absent -> 0 hits each.

	recs := recommend.UnusedRules(p, hits, since)

	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2", len(recs))
	}
	for _, r := range recs {
		if r.Kind != recommend.KindRemoveUnusedRule {
			t.Errorf("kind = %v, want KindRemoveUnusedRule", r.Kind)
		}
		if !strings.Contains(r.Summary, "no recorded hits") {
			t.Errorf("summary missing copy: %q", r.Summary)
		}
		if r.Confidence < 0.5 || r.Confidence > 0.95 {
			t.Errorf("confidence %v out of range", r.Confidence)
		}
		if !strings.Contains(r.Diff, "-rule") {
			t.Errorf("diff fragment missing removal: %q", r.Diff)
		}
	}
}

func TestUnusedRules_AllRulesActiveReturnsEmpty(t *testing.T) {
	p, err := policy.Parse("test.hcl", fixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	since := time.Now().Add(-7 * 24 * time.Hour)
	hits := map[string]uint64{
		"active-allow": 1,
		"stale-allow":  1,
		"deny-secrets": 1,
	}
	recs := recommend.UnusedRules(p, hits, since)
	if len(recs) != 0 {
		t.Errorf("recs = %d, want 0 when every rule has hits", len(recs))
	}
}

func TestUnusedRules_NilPolicyIsEmpty(t *testing.T) {
	if got := recommend.UnusedRules(nil, nil, time.Now()); got != nil {
		t.Errorf("got %v, want nil for nil policy", got)
	}
}

func TestUnusedRules_ConfidenceScalesWithWindow(t *testing.T) {
	p, err := policy.Parse("test.hcl", `
rule "x" {
  action       = "deny"
  sources      = ["*"]
  destinations = ["*:*"]
}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	short := recommend.UnusedRules(p, nil, time.Now().Add(-time.Hour))
	long := recommend.UnusedRules(p, nil, time.Now().Add(-30*24*time.Hour))

	if len(short) != 1 || len(long) != 1 {
		t.Fatalf("expected 1 rec each, got short=%d long=%d", len(short), len(long))
	}
	if short[0].Confidence >= long[0].Confidence {
		t.Errorf("longer window confidence (%v) should exceed shorter (%v)",
			long[0].Confidence, short[0].Confidence)
	}
}
