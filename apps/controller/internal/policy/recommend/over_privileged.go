// SPDX-License-Identifier: AGPL-3.0-or-later

package recommend

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

// RuleObservation mirrors the clickhouse-side RuleObservation but lives
// in this package so the recommender does not depend on the ClickHouse
// driver. Callers (gRPC handler, REST handler) translate.
type RuleObservation struct {
	RuleID    string
	Ports     map[uint16]uint64
	TotalHits uint64
}

// Heuristic thresholds tuned for Phase 1; revisit once we have real
// production data:
//
//   - minHitsForTightening: don't propose a tightening on a tiny sample,
//     because the absence of a port may just be a long tail.
//   - maxObservedPorts:     if a rule routes traffic to a wide variety
//     of ports (e.g., random ephemeral ports, an outbound-internet
//     allow), tightening to that set is unhelpful.
const (
	minHitsForTightening = 50
	maxObservedPorts     = 10
)

// KindTightenOverPrivileged is exported here (extending the Kind enum
// declared in unused.go) so callers can switch on it.
const KindTightenOverPrivileged Kind = 2

// OverPrivilegedRules emits a recommendation per rule whose destination
// uses wildcard ports but whose observed traffic landed on a small,
// distinct set. The proposed diff narrows the destination port spec to
// the observed ports.
//
// Returns nothing for rules with no wildcard destinations, no
// observations, too few hits, or too many distinct observed ports.
func OverPrivilegedRules(p *policy.Policy, observations map[string]*RuleObservation, since time.Time) []Recommendation {
	if p == nil {
		return nil
	}
	now := time.Now().UTC()
	window := now.Sub(since).Round(time.Hour)

	out := make([]Recommendation, 0)
	for _, r := range p.Rules {
		if !hasWildcardPorts(r.Destinations) {
			continue
		}
		obs, ok := observations[r.ID]
		if !ok || obs.TotalHits < minHitsForTightening {
			continue
		}
		if len(obs.Ports) == 0 || len(obs.Ports) > maxObservedPorts {
			continue
		}

		ports := sortedPorts(obs.Ports)
		out = append(out, Recommendation{
			ID:   uuid.New(),
			Kind: KindTightenOverPrivileged,
			Summary: fmt.Sprintf(
				"Rule %q allows any port but only used %d distinct port(s) over the last %s.",
				r.ID, len(ports), window,
			),
			Diff: makeTightenDiff(r, ports),
			Evidence: []string{
				fmt.Sprintf("%d allow traces matched rule %q since %s",
					obs.TotalHits, r.ID, since.Format(time.RFC3339)),
				fmt.Sprintf("ports observed: %s", joinPortsForEvidence(obs.Ports)),
			},
			Confidence:  overPrivilegedConfidence(obs.TotalHits, len(obs.Ports), window),
			GeneratedAt: now,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Summary < out[j].Summary
	})
	return out
}

// hasWildcardPorts is true if any destination matcher uses AllPorts.
func hasWildcardPorts(dests []policy.Matcher) bool {
	for _, d := range dests {
		if d.Ports.AllPorts {
			return true
		}
	}
	return false
}

// sortedPorts returns observation ports in ascending order.
func sortedPorts(m map[uint16]uint64) []uint16 {
	out := make([]uint16, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// joinPortsForEvidence renders the histogram in deterministic order.
func joinPortsForEvidence(m map[uint16]uint64) string {
	ports := sortedPorts(m)
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = fmt.Sprintf("%d (×%d)", p, m[p])
	}
	return strings.Join(parts, ", ")
}

// makeTightenDiff renders a unified-diff fragment showing the
// destination port spec replaced. Wildcards in non-port positions are
// preserved; only the port half flips from "*" to the observed list.
func makeTightenDiff(r policy.Rule, observed []uint16) string {
	portList := make([]string, len(observed))
	for i, p := range observed {
		portList[i] = fmt.Sprintf("%d", p)
	}
	newPortSpec := strings.Join(portList, ",")

	oldDests := make([]string, len(r.Destinations))
	newDests := make([]string, len(r.Destinations))
	for i, d := range r.Destinations {
		base := destinationBase(d)
		if d.Ports.AllPorts {
			oldDests[i] = base + ":*"
			newDests[i] = base + ":" + newPortSpec
		} else {
			oldDests[i] = base + ":" + d.Ports.String()
			newDests[i] = oldDests[i]
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/policy.hcl\n+++ b/policy.hcl\n")
	fmt.Fprintf(&sb, " rule %q {\n", r.ID)
	fmt.Fprintf(&sb, "   action       = %q\n", strings.ToLower(r.Action.String()))
	if len(r.Sources) > 0 {
		fmt.Fprintf(&sb, "   sources      = [%s]\n", quoteAll(formatSources(r.Sources)))
	}
	fmt.Fprintf(&sb, "-  destinations = [%s]\n", quoteAll(oldDests))
	fmt.Fprintf(&sb, "+  destinations = [%s]\n", quoteAll(newDests))
	if r.Description != "" {
		fmt.Fprintf(&sb, "   description  = %q\n", r.Description)
	}
	sb.WriteString(" }\n")
	return sb.String()
}

// destinationBase returns the matcher portion of a destination string,
// stripping the port spec.
func destinationBase(m policy.Matcher) string {
	switch m.Kind {
	case policy.MatcherWildcard:
		return "*"
	case policy.MatcherTag:
		return "tag:" + m.Name
	case policy.MatcherCIDR:
		return "cidr:" + m.CIDR.String()
	case policy.MatcherGroup:
		return "group:" + m.Name
	case policy.MatcherUser:
		return "user:" + m.Name
	default:
		return "?"
	}
}

// overPrivilegedConfidence blends three signals:
//
//   - sample size: more hits → more confidence (saturates at 10k).
//   - port concentration: fewer distinct ports → more confidence.
//   - window length: longer observation → more confidence.
//
// Result is clamped to [0.5, 0.95].
func overPrivilegedConfidence(hits uint64, distinctPorts int, window time.Duration) float32 {
	hitFactor := float64(hits) / 10000.0
	if hitFactor > 1 {
		hitFactor = 1
	}
	concFactor := 1.0 - float64(distinctPorts-1)/float64(maxObservedPorts)
	if concFactor < 0 {
		concFactor = 0
	}
	winFactor := float64(window) / float64(30*24*time.Hour)
	if winFactor > 1 {
		winFactor = 1
	}

	score := 0.5 + 0.45*(0.5*hitFactor+0.3*concFactor+0.2*winFactor)
	if score < 0.5 {
		score = 0.5
	}
	if score > 0.95 {
		score = 0.95
	}
	return float32(score)
}
