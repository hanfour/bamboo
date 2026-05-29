// SPDX-License-Identifier: AGPL-3.0-or-later

package v6map_test

import (
	"encoding/binary"
	"math/big"
	"net/netip"
	"testing"

	"github.com/hanfour/bamboo/apps/controller/internal/ipalloc/v6map"
)

func TestV6From_knownEncoding(t *testing.T) {
	pool := netip.MustParsePrefix("fdba:1100::/64")
	got := v6map.V6From(pool, netip.MustParseAddr("100.64.0.5"))
	want := netip.MustParseAddr("fdba:1100::6440:5")
	if got != want {
		t.Errorf("V6From = %s, want %s", got, want)
	}
}

func TestV6From_V4From_roundTrip(t *testing.T) {
	pool := netip.MustParsePrefix("fdba:1100::/64")
	for _, s := range []string{
		"100.64.0.1", "100.64.0.5", "100.64.0.255",
		"100.64.1.244", "100.127.255.254",
	} {
		v4 := netip.MustParseAddr(s)
		v6 := v6map.V6From(pool, v4)
		got, ok := v6map.V4From(v6)
		if !ok {
			t.Fatalf("V4From(%s) ok=false", v6)
		}
		if got != v4 {
			t.Errorf("round trip = %s, want %s (v6=%s)", got, v4, v6)
		}
	}
}

func TestV4From_rejectsNonMapped(t *testing.T) {
	// Nonzero bytes 8..11 => not a low-32 v4 embedding.
	_, ok := v6map.V4From(netip.MustParseAddr("fdba:1100::dead:0:6440:5"))
	if ok {
		t.Error("V4From should reject an address with nonzero bytes 8..11")
	}
}

// sqlInetAdd replicates PostgreSQL's (host(ip6_pool)::inet +
// (ip - '0.0.0.0'::inet)) arithmetic: add the v4's 32-bit integer
// value to the v6 pool's network address.
func sqlInetAdd(pool netip.Prefix, v4 netip.Addr) netip.Addr {
	base := pool.Masked().Addr().As16()
	b := v4.As4()
	offset := uint64(binary.BigEndian.Uint32(b[:]))
	n := new(big.Int).SetBytes(base[:])
	n.Add(n, new(big.Int).SetUint64(offset))
	var out [16]byte
	n.FillBytes(out[:])
	return netip.AddrFrom16(out)
}

func TestV6From_matchesSQLBackfill(t *testing.T) {
	pool := netip.MustParsePrefix("fdba:1100::/64")
	for _, s := range []string{
		"100.64.0.1", "100.64.0.5", "100.64.0.255",
		"100.64.1.244", "100.127.255.254", "100.64.255.255",
	} {
		v4 := netip.MustParseAddr(s)
		if got, want := v6map.V6From(pool, v4), sqlInetAdd(pool, v4); got != want {
			t.Errorf("%s: V6From=%s, SQL-form=%s", s, got, want)
		}
	}
}
