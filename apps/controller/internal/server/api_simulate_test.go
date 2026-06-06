// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"testing"

	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

func TestDstTunnelIPs_DualFamily(t *testing.T) {
	dst := &repo.Peer{IP: "100.64.0.5", IP6: "fdba:1100::6440:5"}
	got := dstTunnelIPs(dst)
	want := []string{"100.64.0.5/32", "fdba:1100::6440:5/128"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDstTunnelIPs_V4Only(t *testing.T) {
	dst := &repo.Peer{IP: "100.64.0.5"}
	got := dstTunnelIPs(dst)
	if len(got) != 1 || got[0] != "100.64.0.5/32" {
		t.Errorf("got %v, want [100.64.0.5/32] when IP6 empty", got)
	}
}

func TestPeerToJSON_NAT64Health(t *testing.T) {
	// nil status → normalized "unknown"; reason passed through as-is.
	j := peerToJSON(&repo.Peer{NAT64EgressHealthStatus: nil, NAT64EgressHealthReason: nil})
	if j.NAT64EgressHealthStatus == nil || *j.NAT64EgressHealthStatus != "unknown" {
		t.Errorf("nil status → %v, want unknown", j.NAT64EgressHealthStatus)
	}

	// explicit status passes through.
	st, rs := "unhealthy", "translator down"
	j = peerToJSON(&repo.Peer{NAT64EgressHealthStatus: &st, NAT64EgressHealthReason: &rs})
	if j.NAT64EgressHealthStatus == nil || *j.NAT64EgressHealthStatus != "unhealthy" {
		t.Errorf("status = %v, want unhealthy", j.NAT64EgressHealthStatus)
	}
	if j.NAT64EgressHealthReason == nil || *j.NAT64EgressHealthReason != "translator down" {
		t.Errorf("reason = %v, want 'translator down'", j.NAT64EgressHealthReason)
	}
}
