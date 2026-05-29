// SPDX-License-Identifier: AGPL-3.0-or-later

// Package v6map maps a tenant's IPv4 overlay address into a
// deterministic IPv6 ULA by embedding the 32-bit IPv4 into the low 32
// bits of the tenant's /64 pool. See
// docs/design/2026-05-28-nat64-phase-a-overlay-ipv6.md §4.
package v6map

import "net/netip"

// V6From maps an IPv4 address into the low 32 bits of the v6 pool.
// pool must be a /64; its host portion (low 64 bits) is zero by
// definition of a network address, so the 32-bit v4 lands in bytes
// 12..15 with no carry into the prefix.
func V6From(pool netip.Prefix, v4 netip.Addr) netip.Addr {
	base := pool.Masked().Addr().As16()
	b := v4.As4()
	base[12], base[13], base[14], base[15] = b[0], b[1], b[2], b[3]
	return netip.AddrFrom16(base)
}

// V4From extracts the embedded IPv4 from a mapped v6 address. ok is
// false if v6 is not a 16-byte address or bytes 8..11 are nonzero
// (i.e. not a recognisable low-32-bit v4 embedding).
func V4From(v6 netip.Addr) (netip.Addr, bool) {
	if !v6.Is6() {
		return netip.Addr{}, false
	}
	a := v6.As16()
	if a[8]|a[9]|a[10]|a[11] != 0 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte{a[12], a[13], a[14], a[15]}), true
}
