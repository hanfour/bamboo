// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"net/netip"
	"testing"
)

// TestParseRegionMap_HappyPath pins the canonical env-var shapes the
// operator docs will show. Single + multi entries + same-region
// repeated CIDRs must all parse.
func TestParseRegionMap_HappyPath(t *testing.T) {
	cases := []struct {
		name, raw   string
		wantEntries int
	}{
		{"empty", "", 0},
		{"whitespace only", "   ", 0},
		{"single", "us-east-1=10.0.0.0/8", 1},
		{"multi", "us-east-1=10.0.0.0/8,ap-northeast-1=172.16.0.0/12", 2},
		{"same region repeated", "us-east-1=10.0.0.0/8,us-east-1=192.168.0.0/16", 2},
		{"whitespace around tokens", " us-east-1 = 10.0.0.0/8 , ap-northeast-1=172.16.0.0/12 ", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, warnings := parseRegionMap(tc.raw)
			if len(warnings) != 0 {
				t.Errorf("got warnings %v on valid input", warnings)
			}
			if len(m.entries) != tc.wantEntries {
				t.Errorf("entries = %d, want %d", len(m.entries), tc.wantEntries)
			}
		})
	}
}

// TestParseRegionMap_InvalidEntriesWarnButDontAbort pins the
// degrade-gracefully contract: one bad row shouldn't lose every
// good row. Operators get warnings; the matcher still works for
// the entries that parsed.
func TestParseRegionMap_InvalidEntriesWarnButDontAbort(t *testing.T) {
	raw := "us-east-1=10.0.0.0/8,bad-no-equals,ap-northeast-1=999.999.999.999/8,eu-west-1=192.168.0.0/16"
	m, warnings := parseRegionMap(raw)
	if len(m.entries) != 2 {
		t.Errorf("entries = %d, want 2 (us-east-1 + eu-west-1 valid)", len(m.entries))
	}
	if len(warnings) != 2 {
		t.Errorf("warnings = %d, want 2 (missing-equals + bad-CIDR)", len(warnings))
	}
}

// TestResolveRegion_HappyPath pins basic IP→region lookup. Insertion
// order matters when CIDRs overlap (more-specific entries must be
// listed first by the operator); we cover this with an overlapping
// case to fix the contract.
func TestResolveRegion_HappyPath(t *testing.T) {
	m, _ := parseRegionMap("us-east-1=10.0.0.0/8,ap-northeast-1=172.16.0.0/12")
	cases := []struct {
		ip, want string
	}{
		{"10.1.2.3", "us-east-1"},
		{"172.16.0.42", "ap-northeast-1"},
		{"8.8.8.8", ""}, // outside every CIDR
	}
	for _, tc := range cases {
		addr := netip.MustParseAddr(tc.ip)
		if got := m.resolveRegion(addr); got != tc.want {
			t.Errorf("resolveRegion(%s) = %q, want %q", tc.ip, got, tc.want)
		}
	}
}

// TestResolveRegion_OverlappingCIDRsInsertionOrder pins the
// insertion-order rule. An operator who wants a narrower CIDR to
// take precedence over a broader one must list it FIRST. Without
// this test, a refactor to longest-prefix match would silently
// change behaviour AND the env-var docs would need rewriting.
func TestResolveRegion_OverlappingCIDRsInsertionOrder(t *testing.T) {
	// Narrower 10.0.1.0/24 listed first; broader 10.0.0.0/16 second.
	// IPs in 10.0.1.0/24 should resolve to the narrower's region.
	m, _ := parseRegionMap("us-east-1c=10.0.1.0/24,us-east-1=10.0.0.0/16")
	if got := m.resolveRegion(netip.MustParseAddr("10.0.1.42")); got != "us-east-1c" {
		t.Errorf("narrower-first overlap: got %q, want us-east-1c", got)
	}
	if got := m.resolveRegion(netip.MustParseAddr("10.0.2.42")); got != "us-east-1" {
		t.Errorf("broader-only: got %q, want us-east-1", got)
	}
}

// TestResolveRegion_EmptyMatcher pins the "no env configured" case.
// resolveRegion must return "" (not panic) so the register response
// silently omits preferredRegion when the operator hasn't set up
// the table.
func TestResolveRegion_EmptyMatcher(t *testing.T) {
	m, _ := parseRegionMap("")
	if got := m.resolveRegion(netip.MustParseAddr("10.1.2.3")); got != "" {
		t.Errorf("empty matcher: got %q, want empty", got)
	}
	// nil receiver is also valid (defensive).
	var nilm *regionMap
	if got := nilm.resolveRegion(netip.MustParseAddr("10.1.2.3")); got != "" {
		t.Errorf("nil matcher: got %q, want empty", got)
	}
}

// TestResolveRegion_IPv4InIPv6 covers the case where the HTTP server
// reports clients as IPv4-mapped IPv6 (::ffff:1.2.3.4). The
// operator's CIDR table is written in plain v4 form; the matcher
// must unmap before comparing or every v4 client misses.
func TestResolveRegion_IPv4InIPv6(t *testing.T) {
	m, _ := parseRegionMap("us-east-1=10.0.0.0/8")
	addr := netip.MustParseAddr("::ffff:10.1.2.3")
	if got := m.resolveRegion(addr); got != "us-east-1" {
		t.Errorf("4-in-6: got %q, want us-east-1", got)
	}
}

// TestClientIPFromRequest covers the three extraction paths we
// handle: X-Forwarded-For first hop (prod-behind-LB), raw
// RemoteAddr with port (dev-direct), and IPv6 bracketed form.
// Anything we can't parse returns the zero Addr so the caller
// degrades to "no region match" rather than logging at every req.
func TestClientIPFromRequest(t *testing.T) {
	cases := []struct {
		name, remote, xff string
		want              string // "" means zero Addr
	}{
		{"xff first hop wins", "10.0.0.1:1234", "203.0.113.5, 10.0.0.1", "203.0.113.5"},
		{"raw remote when no xff", "10.1.2.3:54321", "", "10.1.2.3"},
		{"ipv6 bracketed remote", "[::1]:8080", "", "::1"},
		{"bare ipv4 remote", "10.1.2.3", "", "10.1.2.3"},
		{"unparseable returns zero", "garbage", "also-garbage", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clientIPFromRequest(tc.remote, tc.xff)
			if tc.want == "" {
				if got.IsValid() {
					t.Errorf("got %v, want zero Addr", got)
				}
				return
			}
			wantAddr := netip.MustParseAddr(tc.want)
			if got != wantAddr {
				t.Errorf("got %v, want %v", got, wantAddr)
			}
		})
	}
}
