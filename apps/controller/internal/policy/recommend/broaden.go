// SPDX-License-Identifier: AGPL-3.0-or-later

package recommend

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

// KindBroadenNeeded extends the Kind enum (declared in unused.go).
// Mirrors bamboov1.Recommendation_KIND_BROADEN_NEEDED.
const KindBroadenNeeded Kind = 3

// DeniedFlow mirrors clickhouse.DeniedFlow in this package so the
// recommender stays driver-agnostic. Callers translate.
type DeniedFlow struct {
	Source      string
	Destination string
	Port        uint16
	Hits        uint64
}

// Heuristic constants for the broaden-needed signal. Broaden-needed
// proposes opening access; we hold it to a higher evidence bar than
// removal / tightening recommendations.
const (
	broadenMinHits        uint64  = 10
	broadenMaxConfidence  float64 = 0.85
	broadenSaturationHits uint64  = 200 // hits above this saturate confidence
)

// BroadenNeeded emits one recommendation per (source, destination, port)
// triple that has been default-denied above the minimum-hit threshold.
//
// The recommendation is *suggestive*: adding an allow rule is a security
// decision. The diff is a starting point — operators must review the
// evidence and decide. Confidence is intentionally capped lower than
// removal / tightening kinds.
//
// flows is expected to be pre-sorted by hits descending (the
// ClickHouse query orders that way), but we sort defensively too.
func BroadenNeeded(_ *policy.Policy, flows []DeniedFlow, since time.Time) []Recommendation {
	if len(flows) == 0 {
		return nil
	}
	sort.SliceStable(flows, func(i, j int) bool {
		return flows[i].Hits > flows[j].Hits
	})

	now := time.Now().UTC()
	window := now.Sub(since).Round(time.Hour)

	out := make([]Recommendation, 0, len(flows))
	for _, f := range flows {
		if f.Hits < broadenMinHits {
			continue
		}
		ruleID := proposedRuleID(f)
		out = append(out, Recommendation{
			ID:   uuid.New(),
			Kind: KindBroadenNeeded,
			Summary: fmt.Sprintf(
				"Default-deny blocked %d attempt(s) from %s to %s:%d in the last %s. "+
					"Add an allow rule if this traffic is intentional.",
				f.Hits, f.Source, f.Destination, f.Port, window,
			),
			Diff: makeBroadenDiff(ruleID, f),
			Evidence: []string{
				fmt.Sprintf("%d denied traces with empty matched_rule_id since %s",
					f.Hits, since.Format(time.RFC3339)),
				fmt.Sprintf("source=%s destination=%s port=%d", f.Source, f.Destination, f.Port),
			},
			Confidence:  broadenConfidence(f.Hits),
			GeneratedAt: now,
		})
	}
	return out
}

// proposedRuleID builds a stable identifier from the flow signature so
// repeated runs produce diffs the operator can review consistently.
func proposedRuleID(f DeniedFlow) string {
	return fmt.Sprintf("auto-broaden-%s-to-%s-%d",
		sanitizeForRuleID(f.Source),
		sanitizeForRuleID(f.Destination),
		f.Port,
	)
}

var ruleIDSafe = regexp.MustCompile(`[^a-zA-Z0-9-]+`)

func sanitizeForRuleID(s string) string {
	cleaned := ruleIDSafe.ReplaceAllString(s, "-")
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "any"
	}
	return cleaned
}

// makeBroadenDiff renders an addition-style diff: the proposed rule
// block is appended to the policy with leading "+" markers.
func makeBroadenDiff(ruleID string, f DeniedFlow) string {
	dst := fmt.Sprintf("%s:%d", f.Destination, f.Port)

	var sb strings.Builder
	sb.WriteString("--- a/policy.hcl\n+++ b/policy.hcl\n")
	fmt.Fprintf(&sb, "+rule %q {\n", ruleID)
	sb.WriteString("+  action       = \"allow\"\n")
	fmt.Fprintf(&sb, "+  description  = \"AI: %d denied attempts; verify before applying.\"\n", f.Hits)
	fmt.Fprintf(&sb, "+  sources      = [%q]\n", f.Source)
	fmt.Fprintf(&sb, "+  destinations = [%q]\n", dst)
	sb.WriteString("+}\n")
	return sb.String()
}

// broadenConfidence saturates at broadenSaturationHits, clamped to
// broadenMaxConfidence. We never claim certainty here; operator review
// is the final arbiter.
func broadenConfidence(hits uint64) float32 {
	const minConf = 0.5
	scale := float64(hits) / float64(broadenSaturationHits)
	if scale > 1 {
		scale = 1
	}
	return float32(minConf + (broadenMaxConfidence-minConf)*scale)
}
