// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

import "net/netip"

// PeerView is the per-peer projection the L3 enforcer needs. Callers
// build it from their own peer record.
type PeerView struct {
	IP     netip.Addr
	User   string
	Groups []string
	Tags   []string
}

// Allow reports whether src is permitted to reach dst at L3 under p.
//
// A nil policy or one with zero rules returns true — this is the
// full-mesh fallback for tenants that have not yet authored a policy
// and preserves the pre-enforcement default. Once any rule exists, the
// evaluator's implicit default-deny applies to pairs no rule covers.
//
// Port-level restrictions in rules are not visible at the WireGuard
// AllowedIPs layer, so Allow returns true whenever at least one port
// would satisfy an allow rule that is not shadowed by an earlier deny.
// Host-level port enforcement is the caller's responsibility.
func Allow(p *Policy, src, dst PeerView) bool {
	if p == nil || len(p.Rules) == 0 {
		return true
	}
	base := EvalRequest{
		SrcUser:   src.User,
		SrcGroups: src.Groups,
		SrcTags:   src.Tags,
		SrcIP:     src.IP,
		DstTags:   dst.Tags,
		DstIP:     dst.IP,
	}
	for i := range p.Rules {
		r := &p.Rules[i]
		if r.Action != ActionAllow {
			continue
		}
		if !matchSources(r.Sources, base) {
			continue
		}
		for _, m := range r.Destinations {
			if !matchDestinationL3(m, base) {
				continue
			}
			probe := base
			switch {
			case m.Ports.AllPorts:
				probe.DstPort = 1
			case len(m.Ports.Ports) > 0:
				probe.DstPort = m.Ports.Ports[0]
			default:
				continue
			}
			if action, _ := p.Evaluate(probe); action == ActionAllow {
				return true
			}
		}
	}
	return false
}

// matchDestinationL3 mirrors matchDestination but ignores PortSpec —
// "does this matcher cover the dst at the network layer?".
func matchDestinationL3(m Matcher, req EvalRequest) bool {
	switch m.Kind {
	case MatcherWildcard:
		return true
	case MatcherTag:
		return contains(req.DstTags, m.Name)
	case MatcherCIDR:
		return req.DstIP.IsValid() && m.CIDR.Contains(req.DstIP)
	default:
		return false
	}
}
