// SPDX-License-Identifier: AGPL-3.0-or-later

package wg

import (
	"net"
	"net/netip"
	"strings"
)

// nonGlobal lists the ranges whose addresses are not reachable from
// the public internet: RFC 1918, RFC 6598 CGNAT, IPv4 link-local, IPv6
// ULA and IPv6 link-local. A candidate in one of them works from a
// host on the same subnet, or over a routed private network (VPN,
// Tailscale, corporate WAN) that this host may or may not have.
var nonGlobal = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
}

// linkLocal ranges are never "same LAN": every host has an address
// there, so sharing the subnet says nothing about sharing a link.
var linkLocal = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
}

// bridgeIfacePrefixes name container plumbing whose subnets say nothing
// about which LAN this host is on, and which come and go with every
// container start.
var bridgeIfacePrefixes = []string{"docker", "br-", "veth"}

// OrderCandidates orders a peer's controller-advertised endpoints for
// this host to try, in three groups that each keep controller order:
//  1. private candidates inside one of local (this host's subnets):
//     a same-LAN path is the cheapest;
//  2. public addresses and hostnames;
//  3. the remaining private candidates. They are still tried, last,
//     because a routed private network may reach them; on a plain VPS
//     they are dead and only cost one probe each.
//
// Link-local candidates always land in group 3. Empty entries are
// dropped; no candidates yields nil.
func OrderCandidates(candidates []string, local []netip.Prefix) []string {
	var lan, public, far []string
	for _, c := range candidates {
		if c == "" {
			continue
		}
		addr, private := privateAddr(c)
		switch {
		case !private:
			public = append(public, c)
		case !containsAddr(linkLocal, addr) && containsAddr(local, addr):
			lan = append(lan, c)
		default:
			far = append(far, c)
		}
	}
	return append(append(lan, public...), far...)
}

// InterfacePrefixes returns the subnets of this host's interfaces,
// skipping container bridges. A failure to enumerate yields nil, which
// demotes every private candidate behind the public ones.
func InterfacePrefixes() []netip.Prefix {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	list := make([]ifaceAddrs, 0, len(ifaces))
	for _, i := range ifaces {
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		list = append(list, ifaceAddrs{name: i.Name, addrs: addrs})
	}
	return prefixesFrom(list)
}

type ifaceAddrs struct {
	name  string
	addrs []net.Addr
}

func prefixesFrom(ifaces []ifaceAddrs) []netip.Prefix {
	var out []netip.Prefix
	for _, i := range ifaces {
		if isBridge(i.name) {
			continue
		}
		for _, a := range i.addrs {
			if p, ok := ipNetPrefix(a); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

func isBridge(name string) bool {
	for _, p := range bridgeIfacePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func ipNetPrefix(a net.Addr) (netip.Prefix, bool) {
	ipNet, ok := a.(*net.IPNet)
	if !ok {
		return netip.Prefix{}, false
	}
	addr, ok := netip.AddrFromSlice(ipNet.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	addr = addr.Unmap()
	bits, _ := ipNet.Mask.Size()
	if addr.Is4() && bits > 32 {
		bits -= 96
	}
	return netip.PrefixFrom(addr, bits).Masked(), true
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
