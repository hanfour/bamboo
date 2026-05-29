// SPDX-License-Identifier: AGPL-3.0-or-later

// Package nat64 owns the NAT64 translation prefix: validation of a
// tenant's configured prefix and RFC 6052 §2.2 synthesis of an IPv6
// address from an IPv4 within a /96. Distinct from ipalloc/v6map,
// which maps the overlay ULA; this is the well-known/NSP translation
// prefix the DNS64 path embeds an IPv4 destination into.
// See docs/design/2026-05-29-nat64-phase-b-dns64.md.
package nat64

import (
	"fmt"
	"net/netip"
)

// DefaultPrefix is the RFC 6052 well-known NAT64 prefix, used when a
// tenant has not configured nat64_prefix.
const DefaultPrefix = "64:ff9b::/96"

// ParsePrefix validates a tenant nat64_prefix string. Empty returns the
// DefaultPrefix. A non-empty value must be a well-formed IPv6 /96 whose
// host bits are zero (a true network address); anything else errors so
// the API edge can 400.
func ParsePrefix(s string) (netip.Prefix, error) {
	if s == "" {
		return netip.MustParsePrefix(DefaultPrefix), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("parse nat64 prefix %q: %w", s, err)
	}
	// Is6() is true for IPv4-mapped addresses (::ffff:0:0); reject
	// those too — a /96 around a v4-mapped block would synthesise a
	// v4-mapped, non-routable address rather than a NAT64 destination.
	if !p.Addr().Is6() || p.Addr().Is4In6() {
		return netip.Prefix{}, fmt.Errorf("nat64 prefix %q must be a native IPv6 /96", s)
	}
	if p.Bits() != 96 {
		return netip.Prefix{}, fmt.Errorf("nat64 prefix %q must be /96, got /%d", s, p.Bits())
	}
	if p.Masked() != p {
		return netip.Prefix{}, fmt.Errorf("nat64 prefix %q has host bits set", s)
	}
	return p, nil
}

// ResolvePrefix returns the canonical /96 string a client should use:
// the stored value, or DefaultPrefix when empty. Assumes s was already
// validated by ParsePrefix at write time, so it does not re-error;
// callers pushing to clients use this to substitute the default.
func ResolvePrefix(s string) string {
	if s == "" {
		return DefaultPrefix
	}
	return s
}

// Synthesize embeds a 32-bit IPv4 into the low 32 bits of a /96 NAT64
// prefix (RFC 6052 §2.2). prefix must be a /96; its host portion is
// zero by definition of a network address, so the four v4 bytes land
// in bytes 12..15 with no carry into the prefix.
func Synthesize(prefix netip.Prefix, v4 netip.Addr) netip.Addr {
	base := prefix.Masked().Addr().As16()
	b := v4.As4()
	base[12], base[13], base[14], base[15] = b[0], b[1], b[2], b[3]
	return netip.AddrFrom16(base)
}
