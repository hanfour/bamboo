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
