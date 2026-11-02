// SPDX-License-Identifier: AGPL-3.0-or-later

package wg

import (
	"net"
	"net/netip"
)

// nonGlobal lists the ranges whose addresses only mean something on
// the network that handed them out: RFC 1918, RFC 6598 CGNAT, IPv4
// link-local, IPv6 ULA and IPv6 link-local. A peer advertising one of
// these is reachable there only if this host sits on the same subnet.
var nonGlobal = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
}

// UsableCandidates orders a peer's controller-advertised endpoints for
// this host to try. Private candidates are dropped unless they fall
// inside one of local (this host's interface subnets); the ones that
// remain go first, since a same-LAN path is the cheapest. Everything
// else (public addresses and hostnames) follows in controller order.
//
// When nothing survives, the controller's first candidate is returned
// alone: a peer the controller gave an endpoint is never left without
// one. Empty entries are ignored; no candidates yields nil.
func UsableCandidates(candidates []string, local []netip.Prefix) []string {
	var lan, public []string
	for _, c := range candidates {
		if c == "" {
			continue
		}
		addr, private := privateAddr(c)
		switch {
		case !private:
			public = append(public, c)
		case containsAddr(local, addr):
			lan = append(lan, c)
		}
	}
	out := append(lan, public...)
	if len(out) == 0 {
		if first := firstNonEmpty(candidates); first != "" {
			return []string{first}
		}
	}
	return out
}

// InterfacePrefixes returns the subnets of this host's interfaces. A
// failure to enumerate yields nil, which makes every private candidate
// count as off-LAN — the conservative choice.
func InterfacePrefixes() []netip.Prefix {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	out := make([]netip.Prefix, 0, len(addrs))
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		addr, ok := netip.AddrFromSlice(ipNet.IP)
		if !ok {
			continue
		}
		bits, _ := ipNet.Mask.Size()
		out = append(out, netip.PrefixFrom(addr.Unmap(), bits).Masked())
	}
	return out
}

// privateAddr parses host:port and reports whether the host is a
// literal address in a non-global range. Hostnames count as public.
func privateAddr(endpoint string) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(endpoint)
	if err != nil {
		return netip.Addr{}, false
	}
	addr := ap.Addr().Unmap().WithZone("")
	return addr, containsAddr(nonGlobal, addr)
}

func containsAddr(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func firstNonEmpty(s []string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
