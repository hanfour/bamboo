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

// DataDir is Tayga's data-dir (dynamic-mapping persistence). Tayga refuses
// to start (exit 1) when the directory named by its data-dir directive does
// not exist, so the manager must create it before launching tayga.
const DataDir = "/var/lib/tayga/bamboo"

// TaygaSelfV6 is the translator's own IPv6 address on the TUN — Tayga's
// source for translator-originated ICMPv6 and the answer to its ipv6-addr
// directive. Tayga REQUIRES an explicit ipv6-addr whenever the prefix is the
// well-known 64:ff9b::/96 and ipv4-addr is a non-global (RFC 1918) address,
// because it cannot embed a private v4 into the well-known prefix (RFC 6052).
// A fixed ULA is fine: it never appears on the mesh (it lives only on the
// translator's local TUN) and the v6→v4 data path doesn't use it.
const TaygaSelfV6 = "fd64:ff9b::1"

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
ipv6-addr %s
prefix %s
dynamic-pool %s
data-dir %s
`, TunDevice, taygaAddr, TaygaSelfV6, prefix, v4Pool, DataDir), nil
}
