// SPDX-License-Identifier: AGPL-3.0-or-later

package repo_test

import (
	"net/netip"
	"testing"

	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// TestDefaultTenantCIDR pins the auto-created-tenant default to the
// Tailscale-avoiding 100.127.0.0/24 (PR #229). This is the single
// source of truth shared by every GetOrCreate call site; the test
// guards against a regression to the old 100.64.0.0/24 that collided
// with Tailscale/Headscale CGNAT allocation.
func TestDefaultTenantCIDR(t *testing.T) {
	if repo.DefaultTenantCIDR != "100.127.0.0/24" {
		t.Fatalf("DefaultTenantCIDR = %q, want 100.127.0.0/24", repo.DefaultTenantCIDR)
	}
	p, err := netip.ParsePrefix(repo.DefaultTenantCIDR)
	if err != nil {
		t.Fatalf("DefaultTenantCIDR is not a valid prefix: %v", err)
	}
	if p.Addr() != netip.MustParseAddr("100.127.0.0") || p.Bits() != 24 {
		t.Errorf("DefaultTenantCIDR network = %s, want 100.127.0.0/24", p)
	}
}
