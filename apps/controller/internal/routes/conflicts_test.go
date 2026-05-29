// SPDX-License-Identifier: AGPL-3.0-or-later

package routes_test

import (
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/routes"
)

// helper for clean test setup — turns a CIDR string + hostname into
// an Owner with a deterministic-ish UUID.
func owner(t *testing.T, host, cidr string) routes.Owner {
	t.Helper()
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		t.Fatalf("bad cidr %q: %v", cidr, err)
	}
	return routes.Owner{
		PeerID:   uuid.MustParse("00000000-0000-0000-0000-" + hostSuffix(host)),
		Hostname: host,
		CIDR:     p,
	}
}

// hostSuffix pads the hostname into a 12-char hex string so the
// PeerID stays distinct per hostname. Test-only — production peer
// UUIDs come from the DB.
func hostSuffix(host string) string {
	// Hash by sum of bytes for deterministic distinctness; collisions
	// are fine because tests use small distinct names.
	var n uint64
	for _, b := range []byte(host) {
		n = n*131 + uint64(b)
	}
	const hex = "0123456789abcdef"
	out := make([]byte, 12)
	for i := 11; i >= 0; i-- {
		out[i] = hex[n&0xf]
		n >>= 4
	}
	return string(out)
}

// TestDetect_DisjointCIDRsProduceNoConflicts is the negative-case
// baseline: two peers advertising non-overlapping prefixes produce
// zero conflict rows. Without this the test suite couldn't tell a
// "detector returns nothing" bug from a "detector finds nothing
// because the inputs disagree" pass.
func TestDetect_DisjointCIDRsProduceNoConflicts(t *testing.T) {
	got := routes.Detect([]routes.Owner{
		owner(t, "office-a", "192.168.1.0/24"),
		owner(t, "office-b", "10.0.0.0/24"),
	})
	if len(got) != 0 {
		t.Errorf("disjoint CIDRs: got %d conflicts, want 0; rows=%+v", len(got), got)
	}
}

// TestDetect_DuplicateCIDRSurfacesBothDirections pins the symmetric-
// emit contract: a single duplicate produces TWO Conflicts (one
// per peer's perspective) so a consumer filtering by Self.PeerID
// always sees the conflict that affects it.
func TestDetect_DuplicateCIDRSurfacesBothDirections(t *testing.T) {
	a := owner(t, "office-a", "192.168.1.0/24")
	b := owner(t, "office-b", "192.168.1.0/24")
	got := routes.Detect([]routes.Owner{a, b})
	if len(got) != 2 {
		t.Fatalf("duplicate CIDR: got %d conflicts, want 2; rows=%+v", len(got), got)
	}
	for _, c := range got {
		if c.Kind != routes.KindDuplicate {
			t.Errorf("kind = %s, want duplicate; conflict = %+v", c.Kind, c)
		}
	}
	// One Self should be a, one should be b.
	seenSelves := map[uuid.UUID]bool{got[0].Self.PeerID: true, got[1].Self.PeerID: true}
	if !seenSelves[a.PeerID] || !seenSelves[b.PeerID] {
		t.Errorf("expected both peers to appear as Self at least once; got %v", seenSelves)
	}
}

// TestDetect_ContainsAndContainedBy verifies the asymmetric pair:
// 10.0.0.0/16 contains 10.0.5.0/24 (broader covers narrower). The
// detector emits one Contains row on the /16's perspective and one
// ContainedBy row on the /24's perspective.
func TestDetect_ContainsAndContainedBy(t *testing.T) {
	broad := owner(t, "broad-gw", "10.0.0.0/16")
	narrow := owner(t, "narrow-gw", "10.0.5.0/24")
	got := routes.Detect([]routes.Owner{broad, narrow})
	if len(got) != 2 {
		t.Fatalf("got %d conflicts, want 2; rows=%+v", len(got), got)
	}
	var sawContains, sawContainedBy bool
	for _, c := range got {
		switch c.Self.PeerID {
		case broad.PeerID:
			if c.Kind != routes.KindContains {
				t.Errorf("broad's perspective: kind=%s, want contains", c.Kind)
			}
			if c.Other.PeerID != narrow.PeerID {
				t.Errorf("broad's perspective: other = %s, want narrow", c.Other.Hostname)
			}
			sawContains = true
		case narrow.PeerID:
			if c.Kind != routes.KindContainedBy {
				t.Errorf("narrow's perspective: kind=%s, want contained_by", c.Kind)
			}
			if c.Other.PeerID != broad.PeerID {
				t.Errorf("narrow's perspective: other = %s, want broad", c.Other.Hostname)
			}
			sawContainedBy = true
		}
	}
	if !sawContains || !sawContainedBy {
		t.Errorf("missing perspective rows: contains=%v contained_by=%v", sawContains, sawContainedBy)
	}
}

// TestDetect_CrossFamilyPairsAreNotConflicts: an IPv4 prefix can
// never overlap an IPv6 prefix at the routing layer. The detector
// must not synthesize a conflict between, say, 10.0.0.0/8 and
// fd00::/8 just because the network-byte arithmetic might coincide.
func TestDetect_CrossFamilyPairsAreNotConflicts(t *testing.T) {
	got := routes.Detect([]routes.Owner{
		owner(t, "v4-gw", "10.0.0.0/8"),
		owner(t, "v6-gw", "fd00::/8"),
	})
	if len(got) != 0 {
		t.Errorf("cross-family CIDRs: got %d conflicts, want 0; rows=%+v", len(got), got)
	}
}

// TestDetect_SamePeerOverlapIsSkipped pins the intra-peer carve-out:
// if a peer somehow ends up with two of its own approved CIDRs that
// overlap, that's a different failure mode (the admin approved two
// overlapping prefixes for the same peer at once) and surfacing it
// in the cross-peer warning UI would clutter the common case.
// Documented carve-out — intentionally NOT a conflict.
func TestDetect_SamePeerOverlapIsSkipped(t *testing.T) {
	a1 := owner(t, "office-a", "10.0.0.0/16")
	a2 := routes.Owner{PeerID: a1.PeerID, Hostname: "office-a", CIDR: mustPrefix(t, "10.0.5.0/24")}
	got := routes.Detect([]routes.Owner{a1, a2})
	if len(got) != 0 {
		t.Errorf("same-peer overlap: got %d conflicts, want 0 (intra-peer carve-out); rows=%+v", len(got), got)
	}
}

// TestDetect_InvalidPrefixesSkipped guards the boundary: a zero-
// valued netip.Prefix lands here when an upstream parse failed and
// the caller passed the zero value through. The detector must skip
// it cleanly rather than panic on the Addr() call.
func TestDetect_InvalidPrefixesSkipped(t *testing.T) {
	got := routes.Detect([]routes.Owner{
		owner(t, "good", "10.0.0.0/24"),
		{PeerID: uuid.New(), Hostname: "bad", CIDR: netip.Prefix{}},
	})
	if len(got) != 0 {
		t.Errorf("invalid prefix: got %d conflicts, want 0; rows=%+v", len(got), got)
	}
}

// TestDetect_StableOrdering pins the sort contract: callers depend
// on the output ordering for stable test assertions + stable Web
// rendering across page refreshes. Sort key is Self.PeerID, then
// Self.CIDR, then Other.PeerID.
func TestDetect_StableOrdering(t *testing.T) {
	// Two independent overlap pairs: (X,Y) duplicating /24 and (Y,Z)
	// containing. Output should be deterministic across runs.
	x := owner(t, "x", "192.168.1.0/24")
	y1 := owner(t, "y", "192.168.1.0/24") // duplicate of x
	y2 := routes.Owner{PeerID: y1.PeerID, Hostname: "y", CIDR: mustPrefix(t, "10.0.0.0/16")}
	z := owner(t, "z", "10.0.5.0/24") // contained by y2
	first := routes.Detect([]routes.Owner{x, y1, y2, z})
	second := routes.Detect([]routes.Owner{z, y2, y1, x}) // input order reversed
	if len(first) != len(second) {
		t.Fatalf("len mismatch: first=%d second=%d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("ordering differs at i=%d: %+v vs %+v", i, first[i], second[i])
		}
	}
}

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("ParsePrefix(%q): %v", s, err)
	}
	return p
}

func TestDetect_ExemptsNAT64Prefix(t *testing.T) {
	p := netip.MustParsePrefix("64:ff9b::/96")
	a := uuid.New()
	b := uuid.New()
	owners := []routes.Owner{
		{PeerID: a, Hostname: "egress-a", CIDR: p},
		{PeerID: b, Hostname: "egress-b", CIDR: p},
	}
	// Without exempt: two egresses advertising the same /96 are a duplicate.
	if got := routes.Detect(owners); len(got) == 0 {
		t.Error("expected a duplicate conflict when the NAT64 prefix is not exempt")
	}
	// With exempt: the synthesised NAT64 route produces no conflict.
	if got := routes.Detect(owners, p); len(got) != 0 {
		t.Errorf("expected no conflict for exempt NAT64 prefix, got %v", got)
	}
}
