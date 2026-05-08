// SPDX-License-Identifier: AGPL-3.0-or-later

package recommend_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hanfour/bamboo/apps/controller/internal/policy"
	"github.com/hanfour/bamboo/apps/controller/internal/policy/recommend"
)

func TestBroadenNeeded_FlagsHighFrequencyDenials(t *testing.T) {
	since := time.Now().Add(-30 * 24 * time.Hour)
	flows := []recommend.DeniedFlow{
		{Source: "tag:dev", Destination: "tag:db", Port: 5432, Hits: 87},
		{Source: "tag:ci", Destination: "tag:registry", Port: 443, Hits: 31},
		{Source: "user:alice@example.com", Destination: "tag:internal", Port: 8080, Hits: 5}, // below threshold
	}
	recs := recommend.BroadenNeeded(nil, flows, since)
	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2 (1 below threshold should be skipped)", len(recs))
	}
	if recs[0].Kind != recommend.KindBroadenNeeded {
		t.Errorf("kind = %v, want KindBroadenNeeded", recs[0].Kind)
	}
	if !strings.Contains(recs[0].Summary, "tag:dev") || !strings.Contains(recs[0].Summary, "5432") {
		t.Errorf("summary missing flow details: %q", recs[0].Summary)
	}
	if !strings.Contains(recs[0].Diff, `+rule "auto-broaden-tag-dev-to-tag-db-5432"`) {
		t.Errorf("diff missing proposed rule id: %q", recs[0].Diff)
	}
	// Higher hits should produce higher confidence.
	if recs[0].Confidence <= recs[1].Confidence {
		t.Errorf("expected confidence to scale with hits: top=%v second=%v",
			recs[0].Confidence, recs[1].Confidence)
	}
}

func TestBroadenNeeded_RespectsMinHitsThreshold(t *testing.T) {
	flows := []recommend.DeniedFlow{
		{Source: "tag:dev", Destination: "tag:db", Port: 5432, Hits: 3},
	}
	recs := recommend.BroadenNeeded(nil, flows, time.Now().Add(-time.Hour))
	if len(recs) != 0 {
		t.Errorf("recs = %d, want 0 (below threshold)", len(recs))
	}
}

func TestBroadenNeeded_EmptyInput(t *testing.T) {
	if got := recommend.BroadenNeeded(nil, nil, time.Now()); got != nil {
		t.Errorf("nil flows returned %v", got)
	}
}

func TestBroadenNeeded_ConfidenceCaps(t *testing.T) {
	flows := []recommend.DeniedFlow{
		{Source: "*", Destination: "*", Port: 1, Hits: 1_000_000},
	}
	recs := recommend.BroadenNeeded(nil, flows, time.Now().Add(-time.Hour))
	if len(recs) != 1 {
		t.Fatalf("recs = %d", len(recs))
	}
	if recs[0].Confidence > 0.85 {
		t.Errorf("confidence = %v, want <= 0.85 (broaden cap)", recs[0].Confidence)
	}
}

func TestBroadenNeeded_SanitizesUnusualMatchers(t *testing.T) {
	flows := []recommend.DeniedFlow{
		{Source: "user:alice@example.com", Destination: "cidr:10.0.0.0/8", Port: 22, Hits: 50},
	}
	recs := recommend.BroadenNeeded(nil, flows, time.Now().Add(-time.Hour))
	if len(recs) != 1 {
		t.Fatalf("recs = %d", len(recs))
	}
	if !strings.Contains(recs[0].Diff, "auto-broaden-user-alice-example-com-to-cidr-10-0-0-0-8-22") {
		t.Errorf("diff missing sanitized rule id, got:\n%s", recs[0].Diff)
	}
	// Original strings should be preserved verbatim in sources/destinations.
	if !strings.Contains(recs[0].Diff, `"user:alice@example.com"`) {
		t.Errorf("diff dropped original source format: %q", recs[0].Diff)
	}
	if !strings.Contains(recs[0].Diff, `"cidr:10.0.0.0/8:22"`) {
		t.Errorf("diff dropped original destination format: %q", recs[0].Diff)
	}
}

// Sanity: ensure the helper sorts inputs that arrive out of order
// (the ClickHouse query already does this, but call sites that
// build the slice directly should not have to worry).
func TestBroadenNeeded_SortsByHitsDescending(t *testing.T) {
	flows := []recommend.DeniedFlow{
		{Source: "a", Destination: "b", Port: 1, Hits: 12},
		{Source: "c", Destination: "d", Port: 2, Hits: 99},
		{Source: "e", Destination: "f", Port: 3, Hits: 50},
	}
	recs := recommend.BroadenNeeded(&policy.Policy{}, flows, time.Now().Add(-time.Hour))
	if len(recs) != 3 {
		t.Fatalf("recs = %d", len(recs))
	}
	if !strings.Contains(recs[0].Summary, ":2") || !strings.Contains(recs[0].Summary, "c") {
		t.Errorf("expected c->d:2 (99 hits) first, got %q", recs[0].Summary)
	}
}
