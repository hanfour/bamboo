// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"fmt"
	"net/netip"
	"strings"
)

// regionMap resolves a client IP to a region label by walking an
// operator-supplied (region, CIDR) table. Implements §4 P2 stage 5.5
// server-side region detection — the controller fills in the
// RegisterResponse.preferred_region field so a client without its
// own region hint still gets fleet-wide geo guidance.
//
// Match order is INSERTION order, NOT longest-prefix or most-specific
// first. Operators choosing overlapping CIDRs are responsible for
// listing the narrower one first; the alternative (longest-prefix
// match every Register) costs O(n log n) per lookup at hot-path
// scale for almost no real-world win, since prod tables are typically
// 5-20 non-overlapping rows.
//
// Empty / unparseable env var ⇒ zero entries, every lookup returns "".
// We log invalid entries once at startup so an operator typo doesn't
// silently produce an empty matcher.
type regionMap struct {
	entries []regionEntry
}

type regionEntry struct {
	region string
	cidr   netip.Prefix
}

// parseRegionMap parses the BAMBOO_REGION_CIDRS env var into a
// regionMap. Format is comma-separated `region=cidr` pairs; a region
// may appear more than once to associate multiple CIDRs with it.
// Examples:
//
//	"us-east-1=10.0.0.0/8"
//	"us-east-1=10.0.0.0/8,ap-northeast-1=172.16.0.0/12"
//	"us-east-1=10.0.0.0/8,us-east-1=192.168.1.0/24,ap-northeast-1=172.16.0.0/12"
//
// Whitespace around tokens is tolerated. Returns a non-nil regionMap
// even when the input is empty / fully invalid — the caller doesn't
// need a nil check. The returned `warnings` slice carries one
// human-readable string per invalid entry so the caller can log
// them at startup (we don't log here because metrics package
// tests would then leak log output).
func parseRegionMap(raw string) (*regionMap, []string) {
	m := &regionMap{}
	var warnings []string
	if strings.TrimSpace(raw) == "" {
		return m, nil
	}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq <= 0 || eq == len(pair)-1 {
			warnings = append(warnings, fmt.Sprintf("region map: entry %q missing region=cidr; skipping", pair))
			continue
		}
		region := strings.TrimSpace(pair[:eq])
		cidrStr := strings.TrimSpace(pair[eq+1:])
		prefix, err := netip.ParsePrefix(cidrStr)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("region map: %q has invalid CIDR %q: %v", region, cidrStr, err))
			continue
		}
		m.entries = append(m.entries, regionEntry{region: region, cidr: prefix})
	}
	return m, warnings
}

// resolveRegion returns the region label for clientAddr, or "" when
// the table is empty / no entry matches. IPv4-mapped IPv6 addresses
// (e.g. ::ffff:1.2.3.4 — what an HTTP server hosting both stacks
// sees from a v4 client) get unmapped before matching so an operator
// supplying a v4 CIDR doesn't have to also write the v6 form.
func (m *regionMap) resolveRegion(clientAddr netip.Addr) string {
	if m == nil || len(m.entries) == 0 {
		return ""
	}
	if clientAddr.Is4In6() {
		clientAddr = clientAddr.Unmap()
	}
	for _, e := range m.entries {
		if e.cidr.Contains(clientAddr) {
			return e.region
		}
	}
	return ""
}

// clientIPFromRequest extracts the client's IP from the HTTP request.
// Honors X-Forwarded-For (first hop) since the controller typically
// runs behind a reverse proxy / load balancer in prod; falls back to
// the raw RemoteAddr otherwise.
//
// Returns the zero netip.Addr when extraction fails — the caller
// treats that as "no region match" rather than logging at every
// request. An attacker setting X-Forwarded-For only changes what
// region we hint; they can't elevate auth or pierce tenant scope,
// so the trust model is "use as advisory only" rather than
// "validate as authoritative".
func clientIPFromRequest(remoteAddr, xForwardedFor string) netip.Addr {
	if xForwardedFor != "" {
		first := strings.TrimSpace(strings.SplitN(xForwardedFor, ",", 2)[0])
		if addr, err := netip.ParseAddr(first); err == nil {
			return addr
		}
	}
	// RemoteAddr is "host:port" — strip the port.
	host := remoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	// IPv6 RemoteAddr can be "[::1]:1234"; strip brackets if present.
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr
	}
	return netip.Addr{}
}
