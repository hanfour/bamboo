// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

import (
	"net/netip"
)

// EvalRequest is the inbound query: "can src reach dst on port p?".
//
// SrcGroups / SrcTags / DstTags are slices because a peer may carry
// multiple of each. Tag/group matchers in a rule are an OR over the
// peer's memberships.
type EvalRequest struct {
	SrcUser   string
	SrcGroups []string
	SrcTags   []string
	SrcIP     netip.Addr

	DstTags []string
	DstIP   netip.Addr
	DstPort uint16
}

// Evaluate scans rules in order and returns the first match. If no
// rule matches, ActionDeny is the default and the returned *Rule is nil.
func (p *Policy) Evaluate(req EvalRequest) (Action, *Rule) {
	for i := range p.Rules {
		r := &p.Rules[i]
		if matchSources(r.Sources, req) && matchDestinations(r.Destinations, req) {
			return r.Action, r
		}
	}
	return ActionDeny, nil
}

func matchSources(sources []Matcher, req EvalRequest) bool {
	for _, m := range sources {
		if matchSource(m, req) {
			return true
		}
	}
	return false
}

func matchSource(m Matcher, req EvalRequest) bool {
	switch m.Kind {
	case MatcherWildcard:
		return true
	case MatcherTag:
		return contains(req.SrcTags, m.Name)
	case MatcherGroup:
		return contains(req.SrcGroups, m.Name)
	case MatcherUser:
		return req.SrcUser != "" && req.SrcUser == m.Name
	case MatcherCIDR:
		return req.SrcIP.IsValid() && m.CIDR.Contains(req.SrcIP)
	default:
		return false
	}
}

func matchDestinations(destinations []Matcher, req EvalRequest) bool {
	for _, m := range destinations {
		if matchDestination(m, req) {
			return true
		}
	}
	return false
}

func matchDestination(m Matcher, req EvalRequest) bool {
	if !m.Ports.Matches(req.DstPort) {
		return false
	}
	switch m.Kind {
	case MatcherWildcard:
		return true
	case MatcherTag:
		return contains(req.DstTags, m.Name)
	case MatcherCIDR:
		return req.DstIP.IsValid() && m.CIDR.Contains(req.DstIP)
	case MatcherGroup, MatcherUser:
		// Groups and users are not meaningful destinations; reject.
		return false
	default:
		return false
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
