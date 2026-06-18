// SPDX-License-Identifier: AGPL-3.0-or-later

package nat64egress_test

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/hanfour/bamboo/clients/core/nat64egress"
)

func TestRenderTaygaConfig_golden(t *testing.T) {
	got, err := nat64egress.RenderTaygaConfig(
		netip.MustParsePrefix("64:ff9b::/96"),
		netip.MustParsePrefix("192.168.255.0/24"),
	)
	if err != nil {
		t.Fatalf("RenderTaygaConfig: %v", err)
	}
	for _, want := range []string{
		"tun-device nat64",
		"ipv4-addr 192.168.255.1",
		// ipv6-addr is mandatory for the well-known prefix + RFC1918 pool
		// combo — tayga --mktun exits 1 without it (NAT64 Phase C2 fix).
		"ipv6-addr " + nat64egress.TaygaSelfV6,
		"prefix 64:ff9b::/96",
		"dynamic-pool 192.168.255.0/24",
		"data-dir " + nat64egress.DataDir,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q\n---\n%s", want, got)
		}
	}
}

func TestRenderTaygaConfig_rejects(t *testing.T) {
	if _, err := nat64egress.RenderTaygaConfig(netip.MustParsePrefix("64:ff9b::/64"), netip.MustParsePrefix("192.168.255.0/24")); err == nil {
		t.Error("non-/96 prefix should error")
	}
	if _, err := nat64egress.RenderTaygaConfig(netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("2001:db8::/64")); err == nil {
		t.Error("non-v4 pool should error")
	}
}
