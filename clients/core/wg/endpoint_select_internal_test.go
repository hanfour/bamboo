// SPDX-License-Identifier: AGPL-3.0-or-later

package wg

import (
	"net"
	"net/netip"
	"slices"
	"testing"
)

func ipNet(cidr string) net.Addr {
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

func TestPrefixesFromSkipsContainerBridges(t *testing.T) {
	ifaces := []ifaceAddrs{
		{name: "eth0", addrs: []net.Addr{ipNet("192.168.1.20/24"), ipNet("fe80::1/64")}},
		{name: "docker0", addrs: []net.Addr{ipNet("172.17.0.1/16")}},
		{name: "br-1a2b3c", addrs: []net.Addr{ipNet("172.18.0.1/16")}},
		{name: "veth9f00", addrs: []net.Addr{ipNet("169.254.3.3/16")}},
		{name: "wlan0", addrs: []net.Addr{ipNet("10.0.5.9/16")}},
	}
	got := prefixesFrom(ifaces)
	want := []netip.Prefix{
		netip.MustParsePrefix("192.168.1.0/24"),
		netip.MustParsePrefix("fe80::/64"),
		netip.MustParsePrefix("10.0.0.0/16"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
