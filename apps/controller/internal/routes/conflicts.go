// SPDX-License-Identifier: AGPL-3.0-or-later

// Package routes holds helpers shared by the subnet-router code path
// (#136). Right now it carries the conflict detector that surfaces
// overlapping admin-approved CIDRs to the operator (§3a follow-up).
//
// The conflict signal is informational only — the controller does not
// reject an overlap. WireGuard's longest-prefix-match means an overlap
// is well-defined behaviour (traffic to 10.0.5.5 routes through the
// /24, traffic to 10.0.0.5 routes through the /16); operators
// sometimes want this for traffic engineering. But far more often an
// overlap is a typo (two field offices both stuck with default
// 192.168.1.0/24 subnets), so the Web UI surfaces a warning and lets
// the admin decide.
package routes

import (
	"net/netip"
	"sort"

	"github.com/google/uuid"
)

// Kind enumerates the conflict relationships between two approved
// CIDRs. Defined relative to the row the conflict is reported on
// (call it "self"):
//
//   - KindDuplicate: another peer carries the exact same CIDR.
//     Highest severity — WireGuard picks one of the two arbitrarily;
//     the other's traffic is dropped on the floor.
//   - KindContains: self is broader than the other peer's CIDR
//     (e.g. self is 10.0.0.0/16, other is 10.0.5.0/24). Traffic to
//     10.0.5.x routes through the OTHER peer, not self.
//   - KindContainedBy: self is narrower than the other peer's CIDR
//     (e.g. self is 10.0.5.0/24, other is 10.0.0.0/16). Traffic to
//     self's range routes through self (longest prefix wins), but
//     anything outside self's range still goes via other.
//
// "Disjoint" is not a Kind — disjoint CIDRs produce no conflict row.
type Kind int

const (
	// KindUnknown is the zero value; never emitted by Detect.
	KindUnknown Kind = iota
	// KindDuplicate is exact-equal CIDRs across two peers.
	KindDuplicate
	// KindContains marks self as broader than (covering) the other
	// peer's prefix — traffic to the narrower range routes through
	// the other peer, not self.
	KindContains
	// KindContainedBy is the inverse of KindContains: self is the
	// narrower side, covered by the other peer's broader prefix.
	KindContainedBy
)

// String implements fmt.Stringer with the kebab-case form the REST
// wire shape uses ("duplicate", "contains", "contained_by").
func (k Kind) String() string {
	switch k {
	case KindDuplicate:
		return "duplicate"
	case KindContains:
		return "contains"
	case KindContainedBy:
		return "contained_by"
	default:
		return "unknown"
	}
}

// Owner is one (peer, CIDR) row that the detector evaluates. Carries
// the peer's hostname so the renderer can show "conflicts with
// office-gw" instead of a bare UUID in the Web UI.
type Owner struct {
	PeerID   uuid.UUID
	Hostname string
	CIDR     netip.Prefix
}

// Conflict is one (self, other) row emitted by Detect. The detector
// emits one Conflict per affected direction — i.e. for a single
// overlap between peer A and peer B, the output contains TWO
// Conflicts: one with Self=A,Kind=Contains,Other=B and one with
// Self=B,Kind=ContainedBy,Other=A. That symmetry keeps the consumer
// simple: filter on Self.PeerID for the peer you're rendering and
// you have everything you need.
type Conflict struct {
	Self  Owner
	Other Owner
	Kind  Kind
}

// Detect compares every Owner against every other and returns the
// list of conflicts in a stable order (sorted by Self.PeerID +
// Self.CIDR + Other.PeerID for deterministic test assertions and
// stable Web rendering across requests).
//
// Cost is O(n²) in the number of approved CIDRs across the tenant.
// At realistic tenant sizes (hundreds of peers, a handful of routes
// each) the constant factor dwarfs the algorithmic complexity; if
// this becomes a hotspot we can index by network address but that
// optimization is premature today.
//
// Zero-valued prefixes (netip.Prefix{}) are silently skipped — they
// occur when the caller hands us an unparsable CIDR string that we
// chose not to error on at the boundary.
//
// Owners whose CIDR matches an exempt prefix are skipped entirely —
// synthesised routes such as the NAT64 translation prefix where
// multiple egresses advertising the same /96 is intended, not a
// conflict (NAT64 umbrella §5).
func Detect(owners []Owner, exempt ...netip.Prefix) []Conflict {
	isExempt := func(p netip.Prefix) bool {
		for _, e := range exempt {
			if p == e {
				return true
			}
		}
		return false
	}
	var out []Conflict
	for i, a := range owners {
		if !a.CIDR.IsValid() || isExempt(a.CIDR) {
			continue
		}
		for j, b := range owners {
			if i == j {
				continue
			}
			if !b.CIDR.IsValid() || isExempt(b.CIDR) {
				continue
			}
			// Same peer with two of its own CIDRs would also conflict
			// against itself by these rules, but that's a different
			// failure mode (peer advertised + admin approved two
			// overlapping prefixes for the same peer) and surfacing it
			// would clutter the cross-peer warning UX. Skip same-peer
			// pairs; a follow-up can report intra-peer overlaps under
			// its own UI affordance if it turns out to be needed.
			if a.PeerID == b.PeerID {
				continue
			}
			k := classify(a.CIDR, b.CIDR)
			if k == KindUnknown {
				continue
			}
			out = append(out, Conflict{Self: a, Other: b, Kind: k})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Self.PeerID != out[j].Self.PeerID {
			return out[i].Self.PeerID.String() < out[j].Self.PeerID.String()
		}
		if out[i].Self.CIDR.String() != out[j].Self.CIDR.String() {
			return out[i].Self.CIDR.String() < out[j].Self.CIDR.String()
		}
		return out[i].Other.PeerID.String() < out[j].Other.PeerID.String()
	})
	return out
}

// classify maps a pair of prefixes onto a Kind from self's
// perspective. netip.Prefix doesn't expose an "overlaps" predicate
// directly, but for CIDRs the relationship is exhaustive: two
// prefixes are either disjoint, equal, or one strictly contains the
// other. Containment-by-network-address checks both directions.
func classify(self, other netip.Prefix) Kind {
	if self == other {
		return KindDuplicate
	}
	// Different prefixes that share an address family can still be
	// disjoint (10.0.0.0/8 vs 192.168.0.0/16); cross-family pairs
	// (IPv4 vs IPv6) are always disjoint.
	if self.Addr().Is4() != other.Addr().Is4() {
		return KindUnknown
	}
	// Prefix containment: self contains other iff self.Bits() <=
	// other.Bits() AND self.Masked() covers other.Addr() at self's
	// bit count. The standard library helper takes a single Addr,
	// so we masked-compare both sides.
	if self.Bits() < other.Bits() && self.Contains(other.Addr()) {
		return KindContains
	}
	if other.Bits() < self.Bits() && other.Contains(self.Addr()) {
		return KindContainedBy
	}
	return KindUnknown
}
