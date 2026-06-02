// SPDX-License-Identifier: AGPL-3.0-or-later

package nat64egress

import (
	"fmt"
	"net/netip"
)

// DefaultV4Pool is Tayga's default dynamic NAT64 pool — a private /24
// unlikely to collide with the egress box's networks. Override via
// --nat64-v4-pool when it does.
const DefaultV4Pool = "192.168.255.0/24"

// TunDevice is the TUN interface name Tayga + the routes use.
const TunDevice = "nat64"

// RenderTaygaConfig produces the tayga.conf body for a NAT64 /96 prefix
// and a v4 dynamic pool. ipv4-addr is the first usable address in the
// pool (Tayga's own address on the TUN). prefix must be a /96; v4Pool a
// v4 CIDR.
func RenderTaygaConfig(prefix netip.Prefix, v4Pool netip.Prefix) (string, error) {
	if !prefix.Addr().Is6() || prefix.Bits() != 96 {
		return "", fmt.Errorf("nat64 prefix %s must be a /96", prefix)
	}
	if !v4Pool.Addr().Is4() {
		return "", fmt.Errorf("v4 pool %s must be IPv4", v4Pool)
	}
	taygaAddr := v4Pool.Masked().Addr().Next() // .1 of the pool
	return fmt.Sprintf(`# Managed by bamboo (NAT64 Phase C2) — do not edit.
tun-device %s
ipv4-addr %s
prefix %s
dynamic-pool %s
data-dir /var/lib/tayga/bamboo
`, TunDevice, taygaAddr, prefix, v4Pool), nil
}
