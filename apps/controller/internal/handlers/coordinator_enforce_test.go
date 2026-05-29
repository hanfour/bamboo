// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

// mustParse is a local helper because policy_test.mustParse is not
// exported.
func mustParsePolicy(t *testing.T, src string) *policy.Policy {
	t.Helper()
	p, err := policy.Parse("test.hcl", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

func TestAllowedIPsFor_NilPolicyReturnsTunnelIP(t *testing.T) {
	src := &repo.Peer{ID: uuid.New(), IP: "100.64.0.1", Tags: []string{"dev"}}
	dst := &repo.Peer{ID: uuid.New(), IP: "100.64.0.2", Tags: []string{"prod"}}
	got := allowedIPsFor(nil, src, dst)
	want := []string{"100.64.0.2/32"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("got %v, want %v (full-mesh fallback when no policy)", got, want)
	}
}

func TestAllowedIPsFor_AllowedPair(t *testing.T) {
	p := mustParsePolicy(t, `
rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	src := &repo.Peer{IP: "100.64.0.1", Tags: []string{"dev"}}
	dst := &repo.Peer{IP: "100.64.0.5", Tags: []string{"db"}}
	got := allowedIPsFor(p, src, dst)
	if len(got) != 1 || got[0] != "100.64.0.5/32" {
		t.Errorf("got %v, want [100.64.0.5/32]", got)
	}
}

func TestAllowedIPsFor_DeniedPairReturnsNil(t *testing.T) {
	p := mustParsePolicy(t, `
rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	src := &repo.Peer{IP: "100.64.0.1", Tags: []string{"prod"}}
	dst := &repo.Peer{IP: "100.64.0.5", Tags: []string{"db"}}
	if got := allowedIPsFor(p, src, dst); got != nil {
		t.Errorf("expected nil for denied pair, got %v", got)
	}
}

func TestAllowedIPsFor_IPv6Destination(t *testing.T) {
	src := &repo.Peer{IP: "fd00::1", Tags: []string{"dev"}}
	dst := &repo.Peer{IP: "fd00::2", Tags: []string{"prod"}}
	got := allowedIPsFor(nil, src, dst)
	if len(got) != 1 || got[0] != "fd00::2/128" {
		t.Errorf("got %v, want [fd00::2/128]", got)
	}
}

func TestPeerView_PopulatesAddrAndTags(t *testing.T) {
	p := &repo.Peer{IP: "100.64.0.42", Tags: []string{"web", "prod"}}
	view := peerView(p)
	if view.IP.String() != "100.64.0.42" {
		t.Errorf("IP = %q, want 100.64.0.42", view.IP.String())
	}
	if len(view.Tags) != 2 || view.Tags[0] != "web" {
		t.Errorf("Tags = %v, want [web prod]", view.Tags)
	}
}

func TestPeerView_InvalidIPLeavesZeroAddr(t *testing.T) {
	p := &repo.Peer{IP: "not-an-ip", Tags: nil}
	view := peerView(p)
	if view.IP.IsValid() {
		t.Errorf("expected zero netip.Addr for invalid input, got %v", view.IP)
	}
}

func TestAllowedIPsFor_DualFamilyTunnel(t *testing.T) {
	src := &repo.Peer{IP: "100.64.0.1", IP6: "fdba:1100::6440:1", Tags: []string{"dev"}}
	dst := &repo.Peer{IP: "100.64.0.5", IP6: "fdba:1100::6440:5", Tags: []string{"prod"}}
	got := allowedIPsFor(nil, src, dst)
	want := []string{"100.64.0.5/32", "fdba:1100::6440:5/128"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v (dual-family tunnel baseline)", got, want)
	}
}

func TestAllowedIPsFor_DualFamilyBeforeRoutes(t *testing.T) {
	src := &repo.Peer{IP: "100.64.0.1", IP6: "fdba:1100::6440:1", Tags: []string{"dev"}}
	dst := &repo.Peer{
		IP: "100.64.0.5", IP6: "fdba:1100::6440:5", Tags: []string{"prod"},
		ApprovedRoutes: []string{"10.0.0.0/24"},
	}
	got := allowedIPsFor(nil, src, dst)
	want := []string{"100.64.0.5/32", "fdba:1100::6440:5/128", "10.0.0.0/24"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q, want %q (v6 must precede approved routes)", i, got[i], want[i])
		}
	}
}
