// SPDX-License-Identifier: AGPL-3.0-or-later

// Package policy parses and evaluates the bamboo ACL DSL.
//
// DSL example:
//
//	rule "allow-dev-to-staging" {
//	  action       = "allow"
//	  description  = "Developers can reach staging services"
//	  sources      = ["tag:dev", "group:engineering"]
//	  destinations = ["tag:staging:443", "tag:staging:5432"]
//	}
//
// Rules are evaluated in source order; the first match wins. The
// implicit default when no rule matches is DENY.
//
// Source matchers:
//   - tag:NAME       peer carries the tag
//   - group:NAME     peer's user is a member of the group
//   - user:EMAIL     specific user
//   - cidr:PREFIX    peer's IP falls within the CIDR
//   - *              wildcard
//
// Destination matchers are a source matcher followed by a port spec:
//
//	tag:staging:443         single port
//	tag:db:5432,5433        comma-separated list
//	cidr:0.0.0.0/0:*        wildcard ports
package policy

import (
	"fmt"
	"net/netip"
	"strings"
)

// Action is the outcome of a matched rule.
type Action int

const (
	// ActionUnknown is the zero value; never returned from a successful
	// match.
	ActionUnknown Action = iota
	// ActionAllow permits the connection.
	ActionAllow
	// ActionDeny rejects the connection.
	ActionDeny
)

// String implements fmt.Stringer.
func (a Action) String() string {
	switch a {
	case ActionAllow:
		return "allow"
	case ActionDeny:
		return "deny"
	default:
		return "unknown"
	}
}

// MatcherKind enumerates the source / destination matcher categories.
type MatcherKind int

const (
	// MatcherUnknown is the zero value; never returned from a successful parse.
	MatcherUnknown MatcherKind = iota
	// MatcherWildcard matches anything (the literal "*").
	MatcherWildcard
	// MatcherTag matches peers carrying a specific tag.
	MatcherTag
	// MatcherGroup matches peers whose user is in a specific group.
	MatcherGroup
	// MatcherUser matches a specific user (by email).
	MatcherUser
	// MatcherCIDR matches IPs falling within a specific prefix.
	MatcherCIDR
)

// Matcher is one entry in a rule's sources or destinations slice. The
// destination form additionally carries a Ports clause; sources leave
// Ports zero-valued (and ignored).
type Matcher struct {
	Kind  MatcherKind
	Name  string       // tag/group/user name
	CIDR  netip.Prefix // populated when Kind == MatcherCIDR
	Ports PortSpec
}

// PortSpec describes which destination ports are covered. Zero value
// (.AllPorts == false, .Ports == nil) matches no port; for sources we
// ignore PortSpec entirely.
type PortSpec struct {
	AllPorts bool
	Ports    []uint16
}

// Matches reports whether port falls within the spec.
func (p PortSpec) Matches(port uint16) bool {
	if p.AllPorts {
		return true
	}
	for _, allowed := range p.Ports {
		if allowed == port {
			return true
		}
	}
	return false
}

// String renders the spec in DSL form.
func (p PortSpec) String() string {
	if p.AllPorts {
		return "*"
	}
	parts := make([]string, len(p.Ports))
	for i, port := range p.Ports {
		parts[i] = fmt.Sprintf("%d", port)
	}
	return strings.Join(parts, ",")
}

// Rule is a single allow/deny statement.
type Rule struct {
	ID           string
	Action       Action
	Description  string
	Sources      []Matcher
	Destinations []Matcher
}

// Policy is the parsed form of a tenant's ACL document. Order of
// .Rules is significant.
type Policy struct {
	Rules []Rule
	// TagOwners maps a tag name (e.g. "tag:dev") to the email
	// addresses authorized to assign/remove it on peers (issue #139).
	// Empty / nil ⇒ no tagOwners block, falling back to admin-only
	// assignment for back-compat. A tag listed with "*" (wildcard)
	// is owned by any tenant member; concrete email lists restrict
	// assignment to those identities.
	TagOwners map[string][]string
}

// CanAssignTag reports whether `email` is allowed to assign tag
// `name` per the policy's tagOwners block (issue #139). Returns
// true when:
//   - the policy has no tagOwners block (legacy / no opt-in);
//   - the tag has no owner row (pre-#139 tag, falls back to admin);
//   - the tag's owner list contains the wildcard "*";
//   - the tag's owner list contains the caller's email.
//
// Returns false only when the policy declares an explicit owner
// list for the tag and the email isn't in it. Case-insensitive on
// email compare because OIDC providers normalize differently.
func (p *Policy) CanAssignTag(name, email string) bool {
	if p == nil || len(p.TagOwners) == 0 {
		return true
	}
	owners, ok := p.TagOwners[name]
	if !ok {
		return true
	}
	lower := strings.ToLower(email)
	for _, o := range owners {
		if o == "*" {
			return true
		}
		if strings.ToLower(o) == lower {
			return true
		}
	}
	return false
}
