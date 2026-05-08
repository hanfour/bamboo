// SPDX-License-Identifier: AGPL-3.0-or-later

// Package recommend computes Tier-1 ACL recommendations from a policy
// document plus observed usage data. "Tier 1" means rule-based and
// fully explainable; ML-driven recommendations live in a sibling
// package once the data plane lands. See ADR 0010.
package recommend

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

// Kind enumerates the categories of recommendation we currently emit.
// Mirrors the proto enum but kept here so the policy package does not
// depend on bamboo.v1 generated code.
type Kind int

const (
	// KindUnknown is the zero value; never returned from a successful run.
	KindUnknown Kind = iota
	// KindRemoveUnusedRule flags a rule with no hits in the observation window.
	KindRemoveUnusedRule
)

// Recommendation is a single proposed change to a tenant's policy.
type Recommendation struct {
	ID          uuid.UUID
	Kind        Kind
	Summary     string
	Diff        string   // unified-diff fragment
	Evidence    []string // human-readable observations supporting the proposal
	Confidence  float32
	GeneratedAt time.Time
}

// UnusedRules returns one recommendation per rule that recorded zero
// hits in `since`. The hits map is the per-rule count from the
// telemetry pipeline (see clickhouse.Traces.RuleHitCounts).
//
// Wildcard "deny by default" rules and rules that exist purely as
// safety-nets generally accumulate zero hits, so the caller is
// expected to review each suggestion rather than auto-apply.
func UnusedRules(p *policy.Policy, hits map[string]uint64, since time.Time) []Recommendation {
	if p == nil {
		return nil
	}
	now := time.Now().UTC()
	window := now.Sub(since).Round(time.Hour)

	out := make([]Recommendation, 0)
	for _, r := range p.Rules {
		if hits[r.ID] != 0 {
			continue
		}
		out = append(out, Recommendation{
			ID:   uuid.New(),
			Kind: KindRemoveUnusedRule,
			Summary: fmt.Sprintf(
				"Rule %q has no recorded hits in the last %s.",
				r.ID, window,
			),
			Diff: makeRemovalDiff(r),
			Evidence: []string{
				fmt.Sprintf("0 evaluation traces matched rule %q since %s", r.ID, since.Format(time.RFC3339)),
				fmt.Sprintf("rule action: %s, sources: %s, destinations: %s",
					r.Action, strings.Join(formatSources(r.Sources), ", "),
					strings.Join(formatDestinations(r.Destinations), ", ")),
			},
			// Confidence is heuristic: longer windows -> higher confidence.
			Confidence:  windowConfidence(window),
			GeneratedAt: now,
		})
	}

	// Stable order by rule id for deterministic output.
	sort.Slice(out, func(i, j int) bool {
		return out[i].Summary < out[j].Summary
	})
	return out
}

// windowConfidence is a smooth curve from 0.5 at 1h to 0.95 at 30d.
// Keeps explainability simple: linear blend in between.
func windowConfidence(window time.Duration) float32 {
	const minConf, maxConf = 0.5, 0.95
	const minWin, maxWin = float64(time.Hour), float64(30 * 24 * time.Hour)
	w := float64(window)
	switch {
	case w <= minWin:
		return minConf
	case w >= maxWin:
		return maxConf
	default:
		t := (w - minWin) / (maxWin - minWin)
		return float32(minConf + t*(maxConf-minConf))
	}
}

// makeRemovalDiff renders a unified-diff fragment that deletes the rule
// block. Real round-tripped HCL deletion is non-trivial; this is the
// human-readable preview, not a machine-applied patch (the UI shows
// the diff and the user clicks "open PR").
func makeRemovalDiff(r policy.Rule) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/policy.hcl\n+++ b/policy.hcl\n")
	fmt.Fprintf(&sb, "-rule %q {\n", r.ID)
	fmt.Fprintf(&sb, "-  action       = %q\n", strings.ToLower(r.Action.String()))
	if len(r.Sources) > 0 {
		fmt.Fprintf(&sb, "-  sources      = [%s]\n", quoteAll(formatSources(r.Sources)))
	}
	if len(r.Destinations) > 0 {
		fmt.Fprintf(&sb, "-  destinations = [%s]\n", quoteAll(formatDestinations(r.Destinations)))
	}
	if r.Description != "" {
		fmt.Fprintf(&sb, "-  description  = %q\n", r.Description)
	}
	sb.WriteString("-}\n")
	return sb.String()
}

func formatSources(ms []policy.Matcher) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, formatMatcher(m))
	}
	return out
}

func formatDestinations(ms []policy.Matcher) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, formatMatcher(m)+":"+m.Ports.String())
	}
	return out
}

func formatMatcher(m policy.Matcher) string {
	switch m.Kind {
	case policy.MatcherWildcard:
		return "*"
	case policy.MatcherTag:
		return "tag:" + m.Name
	case policy.MatcherGroup:
		return "group:" + m.Name
	case policy.MatcherUser:
		return "user:" + m.Name
	case policy.MatcherCIDR:
		return "cidr:" + m.CIDR.String()
	default:
		return "?"
	}
}

func quoteAll(items []string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(out, ", ")
}
