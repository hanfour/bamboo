// SPDX-License-Identifier: AGPL-3.0-or-later

package recommend_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hanfour/bamboo/apps/controller/internal/policy"
	"github.com/hanfour/bamboo/apps/controller/internal/policy/recommend"
)

const wildcardPortFixture = `
rule "wide-open-staging" {
  action       = "allow"
  description  = "Anyone tagged staging can talk to anyone, any port"
  sources      = ["tag:dev"]
  destinations = ["tag:staging:*"]
}

rule "narrow-already" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}

rule "ci-everywhere" {
  action       = "allow"
  sources      = ["tag:ci"]
  destinations = ["cidr:0.0.0.0/0:*"]
}
`

func TestOverPrivilegedRules_FlagsConcentratedTraffic(t *testing.T) {
	p, err := policy.Parse("test.hcl", wildcardPortFixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	since := time.Now().Add(-7 * 24 * time.Hour)

	// wide-open-staging: 200 hits across 3 ports → strong tighten signal.
	// narrow-already:   40 hits but no wildcard → ignored.
	// ci-everywhere:    60 hits across 50 ports → too many to tighten.
	wide := map[uint16]uint64{443: 100, 5432: 50, 8080: 50}
	wideObs := &recommend.RuleObservation{RuleID: "wide-open-staging", Ports: wide}
	for _, h := range wide {
		wideObs.TotalHits += h
	}

	noisy := map[uint16]uint64{}
	for p := uint16(40000); p < 40050; p++ {
		noisy[p] = 5
	}
	noisyObs := &recommend.RuleObservation{RuleID: "ci-everywhere", Ports: noisy}
	for _, h := range noisy {
		noisyObs.TotalHits += h
	}

	obs := map[string]*recommend.RuleObservation{
		"wide-open-staging": wideObs,
		"ci-everywhere":     noisyObs,
	}

	recs := recommend.OverPrivilegedRules(p, obs, since)
	if len(recs) != 1 {
		t.Fatalf("recs = %d, want 1 (only wide-open-staging should fire)", len(recs))
	}
	if recs[0].Kind != recommend.KindTightenOverPrivileged {
		t.Errorf("kind = %v, want KindTightenOverPrivileged", recs[0].Kind)
	}
	if !strings.Contains(recs[0].Summary, "wide-open-staging") {
		t.Errorf("summary missing rule id: %q", recs[0].Summary)
	}
	if !strings.Contains(recs[0].Diff, "443,5432,8080") {
		t.Errorf("diff missing concrete port list: %q", recs[0].Diff)
	}
	if recs[0].Confidence < 0.5 || recs[0].Confidence > 0.95 {
		t.Errorf("confidence %v out of range", recs[0].Confidence)
	}
}

func TestOverPrivilegedRules_RespectsMinHitThreshold(t *testing.T) {
	p, err := policy.Parse("test.hcl", `
rule "rare-allow" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:rare:*"]
}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	obs := map[string]*recommend.RuleObservation{
		"rare-allow": {
			RuleID:    "rare-allow",
			Ports:     map[uint16]uint64{443: 10},
			TotalHits: 10,
		},
	}
	recs := recommend.OverPrivilegedRules(p, obs, time.Now().Add(-time.Hour))
	if len(recs) != 0 {
		t.Errorf("recs = %d, want 0 below threshold", len(recs))
	}
}

func TestOverPrivilegedRules_SkipsRulesWithoutWildcard(t *testing.T) {
	p, err := policy.Parse("test.hcl", `
rule "explicit" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	obs := map[string]*recommend.RuleObservation{
		"explicit": {
			RuleID:    "explicit",
			Ports:     map[uint16]uint64{5432: 1000},
			TotalHits: 1000,
		},
	}
	recs := recommend.OverPrivilegedRules(p, obs, time.Now().Add(-30*24*time.Hour))
	if len(recs) != 0 {
		t.Errorf("recs = %d, want 0 (no wildcard to tighten)", len(recs))
	}
}

func TestOverPrivilegedRules_NilPolicyAndEmptyObs(t *testing.T) {
	if got := recommend.OverPrivilegedRules(nil, nil, time.Now()); got != nil {
		t.Errorf("nil policy returned %v", got)
	}
	p, _ := policy.Parse("test.hcl", `rule "x" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`)
	if got := recommend.OverPrivilegedRules(p, nil, time.Now()); len(got) != 0 {
		t.Errorf("empty obs map produced %d recs", len(got))
	}
}
