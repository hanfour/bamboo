// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

import (
	"net/netip"
	"strings"
)

// IngressTag is the peer tag the public services ingress peer carries
// (DSL form "tag:ingress").
const IngressTag = "ingress"

// GeneratedRulePrefix marks rules the controller synthesises. They are
// never stored in the tenant's HCL, so the ACL editor cannot edit them.
const GeneratedRulePrefix = "generated:"

// IngressTarget is one live public service: the ingress peer may reach
// PeerIP on Port.
type IngressTarget struct {
	ServiceID string
	PeerIP    netip.Addr
	Port      uint16
}

// IngressRules returns two allow rules per target: tag:ingress ->
// <peer>/32:<port>, and the return path <peer>/32 -> tag:ingress:<port>
// that WireGuard needs so the target lists the ingress peer in its
// AllowedIPs. Port enforcement beyond L3 stays with the target host.
func IngressRules(targets []IngressTarget) []Rule {
	out := make([]Rule, 0, 2*len(targets))
	for _, t := range targets {
		if !t.PeerIP.IsValid() || t.Port == 0 {
			continue
		}
		host := netip.PrefixFrom(t.PeerIP, t.PeerIP.BitLen())
		ports := PortSpec{Ports: []uint16{t.Port}}
		id := GeneratedRulePrefix + "ingress:" + t.ServiceID
		out = append(out,
			Rule{
				ID:           id,
				Action:       ActionAllow,
				Description:  "generated: public services ingress",
				Sources:      []Matcher{{Kind: MatcherTag, Name: IngressTag}},
				Destinations: []Matcher{{Kind: MatcherCIDR, CIDR: host, Ports: ports}},
			},
			Rule{
				ID:           id + ":return",
				Action:       ActionAllow,
				Description:  "generated: public services ingress return path",
				Sources:      []Matcher{{Kind: MatcherCIDR, CIDR: host}},
				Destinations: []Matcher{{Kind: MatcherTag, Name: IngressTag, Ports: ports}},
			},
		)
	}
	return out
}

// WithIngressRules returns a copy of p with the generated rules first.
// A nil or rule-less policy is returned as is: it already means full
// mesh, and adding a rule would turn on default-deny for the tenant.
func WithIngressRules(p *Policy, targets []IngressTarget) *Policy {
	if p == nil || len(p.Rules) == 0 || len(targets) == 0 {
		return p
	}
	generated := IngressRules(targets)
	rules := make([]Rule, 0, len(generated)+len(p.Rules))
	rules = append(rules, generated...)
	rules = append(rules, p.Rules...)
	return &Policy{Rules: rules, TagOwners: p.TagOwners, Groups: p.Groups}
}

// IsGenerated reports whether r was synthesised by the controller.
func IsGenerated(r Rule) bool { return strings.HasPrefix(r.ID, GeneratedRulePrefix) }
