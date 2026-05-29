// SPDX-License-Identifier: AGPL-3.0-or-later

package nat64_test

import (
	"net/netip"
	"testing"

	"github.com/hanfour/bamboo/apps/controller/internal/nat64"
)

func TestParsePrefix_default(t *testing.T) {
	got, err := nat64.ParsePrefix("")
	if err != nil {
		t.Fatalf("ParsePrefix(\"\"): %v", err)
	}
	if got.String() != nat64.DefaultPrefix {
		t.Errorf("ParsePrefix(\"\") = %s, want default %s", got, nat64.DefaultPrefix)
	}
}

func TestParsePrefix_customSlash96(t *testing.T) {
	got, err := nat64.ParsePrefix("2001:db8:1234::/96")
	if err != nil {
		t.Fatalf("ParsePrefix custom /96: %v", err)
	}
	if got.Bits() != 96 || got.Addr().String() != "2001:db8:1234::" {
		t.Errorf("got %s, want 2001:db8:1234::/96", got)
	}
}

func TestParsePrefix_rejectsNonSlash96(t *testing.T) {
	for _, s := range []string{
		"64:ff9b::/64",
		"64:ff9b::/128",
		"100.64.0.0/24",
		"not-a-prefix",
		"64:ff9b::1/96",
		"::ffff:0:0/96", // IPv4-mapped — Is6() is true but not a NAT64 prefix
	} {
		if _, err := nat64.ParsePrefix(s); err == nil {
			t.Errorf("ParsePrefix(%q) = nil error, want rejection", s)
		}
	}
}

func TestResolvePrefix_emptyIsDefault(t *testing.T) {
	if got := nat64.ResolvePrefix(""); got != nat64.DefaultPrefix {
		t.Errorf("ResolvePrefix(\"\") = %s, want %s", got, nat64.DefaultPrefix)
	}
	if got := nat64.ResolvePrefix("2001:db8:1234::/96"); got != "2001:db8:1234::/96" {
		t.Errorf("ResolvePrefix(custom) = %s, want passthrough", got)
	}
}

func TestSynthesize_knownVector(t *testing.T) {
	prefix := netip.MustParsePrefix(nat64.DefaultPrefix)
	got := nat64.Synthesize(prefix, netip.MustParseAddr("93.184.216.34"))
	want := netip.MustParseAddr("64:ff9b::5db8:d822")
	if got != want {
		t.Errorf("Synthesize = %s, want %s", got, want)
	}

	// Custom /96: the prefix bytes (0..11) must be preserved, not just
	// the trailing v4 — guards against writing into the wrong octets.
	custom := netip.MustParsePrefix("2001:db8:1234::/96")
	if g, w := nat64.Synthesize(custom, netip.MustParseAddr("93.184.216.34")),
		netip.MustParseAddr("2001:db8:1234::5db8:d822"); g != w {
		t.Errorf("Synthesize(custom) = %s, want %s", g, w)
	}
}

func TestSynthesize_roundTripLowBits(t *testing.T) {
	prefix := netip.MustParsePrefix(nat64.DefaultPrefix)
	for _, s := range []string{"1.2.3.4", "10.0.0.1", "255.255.255.255", "100.64.0.5"} {
		v4 := netip.MustParseAddr(s)
		v6 := nat64.Synthesize(prefix, v4)
		b := v6.As16()
		if [4]byte{b[12], b[13], b[14], b[15]} != v4.As4() {
			t.Errorf("%s: low 32 bits = %v, want %v", s, b[12:], v4.As4())
		}
	}
}
