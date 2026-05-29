// SPDX-License-Identifier: AGPL-3.0-or-later

package wg_test

import (
	"strings"
	"testing"

	"github.com/hanfour/bamboo/clients/core/wg"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

func TestBuildDeviceConfig_HappyPath(t *testing.T) {
	priv, err := wg.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}

	peerPriv, _ := wg.GeneratePrivateKey()
	peerPubB64 := peerPriv.PublicKey().Base64()

	resp := &bamboov1.RegisterResponse{
		Self: &bamboov1.Peer{
			Id: "self-id",
			Ip: "100.64.0.1",
		},
		Peers: []*bamboov1.Peer{
			{
				Id:                 "peer-id",
				Ip:                 "100.64.0.2",
				WireguardPublicKey: peerPubB64,
				Endpoints:          []string{"203.0.113.5:51820"},
			},
		},
	}

	cfg, err := wg.BuildDeviceConfig(priv, resp)
	if err != nil {
		t.Fatalf("BuildDeviceConfig: %v", err)
	}
	if cfg.Address.String() != "100.64.0.1/32" {
		t.Errorf("Address = %s, want 100.64.0.1/32", cfg.Address)
	}
	if len(cfg.Peers) != 1 {
		t.Fatalf("len(Peers) = %d, want 1", len(cfg.Peers))
	}
	p := cfg.Peers[0]
	if p.AllowedIPs[0].String() != "100.64.0.2/32" {
		t.Errorf("peer AllowedIPs[0] = %s, want 100.64.0.2/32", p.AllowedIPs[0])
	}
	if p.Endpoint != "203.0.113.5:51820" {
		t.Errorf("peer Endpoint = %q, want 203.0.113.5:51820", p.Endpoint)
	}
	if p.PublicKey != peerPriv.PublicKey() {
		t.Error("peer PublicKey did not match input")
	}
}

func TestBuildDeviceConfig_MissingSelf(t *testing.T) {
	_, err := wg.BuildDeviceConfig(wg.PrivateKey{}, &bamboov1.RegisterResponse{})
	if err == nil {
		t.Error("expected error when Self is nil")
	}
}

func TestBuildDeviceConfig_BadPeerKey(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	resp := &bamboov1.RegisterResponse{
		Self: &bamboov1.Peer{Ip: "100.64.0.1"},
		Peers: []*bamboov1.Peer{
			{Ip: "100.64.0.2", WireguardPublicKey: "definitely-not-base64"},
		},
	}
	if _, err := wg.BuildDeviceConfig(priv, resp); err == nil {
		t.Error("expected error for malformed peer key")
	}
}

func TestBuildDeviceConfig_EnforcingUsesAllowedIps(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	peerPriv, _ := wg.GeneratePrivateKey()

	resp := &bamboov1.RegisterResponse{
		PolicyRevision: 5,
		Self:           &bamboov1.Peer{Ip: "100.64.0.1"},
		Peers: []*bamboov1.Peer{
			{
				Id:                 "db",
				Ip:                 "100.64.0.5",
				WireguardPublicKey: peerPriv.PublicKey().Base64(),
				AllowedIps:         []string{"100.64.0.5/32"},
			},
		},
	}
	cfg, err := wg.BuildDeviceConfig(priv, resp)
	if err != nil {
		t.Fatalf("BuildDeviceConfig: %v", err)
	}
	if len(cfg.Peers) != 1 {
		t.Fatalf("len(Peers) = %d, want 1", len(cfg.Peers))
	}
	if cfg.Peers[0].AllowedIPs[0].String() != "100.64.0.5/32" {
		t.Errorf("AllowedIPs[0] = %s, want 100.64.0.5/32", cfg.Peers[0].AllowedIPs[0])
	}
}

func TestBuildDeviceConfig_EnforcingSkipsPeersWithEmptyAllowedIps(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	allowedPriv, _ := wg.GeneratePrivateKey()
	deniedPriv, _ := wg.GeneratePrivateKey()

	resp := &bamboov1.RegisterResponse{
		PolicyRevision: 1,
		Self:           &bamboov1.Peer{Ip: "100.64.0.1"},
		Peers: []*bamboov1.Peer{
			{
				Id:                 "allowed",
				Ip:                 "100.64.0.5",
				WireguardPublicKey: allowedPriv.PublicKey().Base64(),
				AllowedIps:         []string{"100.64.0.5/32"},
			},
			{
				Id:                 "denied",
				Ip:                 "100.64.0.9",
				WireguardPublicKey: deniedPriv.PublicKey().Base64(),
				// AllowedIps left empty by the controller — policy denies.
			},
		},
	}
	cfg, err := wg.BuildDeviceConfig(priv, resp)
	if err != nil {
		t.Fatalf("BuildDeviceConfig: %v", err)
	}
	if len(cfg.Peers) != 1 {
		t.Fatalf("len(Peers) = %d, want 1 (denied peer should be excluded)", len(cfg.Peers))
	}
	if cfg.Peers[0].AllowedIPs[0].String() != "100.64.0.5/32" {
		t.Errorf("AllowedIPs[0] = %s, want 100.64.0.5/32", cfg.Peers[0].AllowedIPs[0])
	}
}

func TestBuildDeviceConfig_NoPolicyFallsBackToFullMesh(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	peerPriv, _ := wg.GeneratePrivateKey()

	resp := &bamboov1.RegisterResponse{
		// PolicyRevision: 0 (the zero value) → full-mesh fallback.
		Self: &bamboov1.Peer{Ip: "100.64.0.1"},
		Peers: []*bamboov1.Peer{
			{
				Id:                 "peer",
				Ip:                 "100.64.0.2",
				WireguardPublicKey: peerPriv.PublicKey().Base64(),
				// AllowedIps deliberately left empty by the controller —
				// simulates a legacy pre-#149 controller that omitted
				// the field when policy_revision was zero. Modern
				// controllers always populate it (see the test below).
			},
		},
	}
	cfg, err := wg.BuildDeviceConfig(priv, resp)
	if err != nil {
		t.Fatalf("BuildDeviceConfig: %v", err)
	}
	if len(cfg.Peers) != 1 {
		t.Fatalf("len(Peers) = %d, want 1 (full mesh keeps every peer)", len(cfg.Peers))
	}
	if cfg.Peers[0].AllowedIPs[0].String() != "100.64.0.2/32" {
		t.Errorf("AllowedIPs[0] = %s, want 100.64.0.2/32 (derived from peer.IP)", cfg.Peers[0].AllowedIPs[0])
	}
}

// Regression for the sprint-completion E2E (PR #167-#169): controller
// merges admin-approved subnet routes (#136) into a peer's allowedIps
// even when policy_revision is zero — those approvals are policy-
// orthogonal admin sign-offs. The client must honor that list rather
// than discard it for a /32-derived fallback. Without this fix, mac
// mini advertises 192.168.86.0/24, admin approves, controller sends
// allowedIps=["100.64.0.1/32","192.168.86.0/24"], and h4's WireGuard
// silently configures only the /32 — the subnet route never lands.
func TestBuildDeviceConfig_NoPolicyHonorsControllerAllowedIps(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	peerPriv, _ := wg.GeneratePrivateKey()

	resp := &bamboov1.RegisterResponse{
		// PolicyRevision: 0 — no authored ACL, but controller still
		// emits AllowedIps because admin approved a subnet route.
		Self: &bamboov1.Peer{Ip: "100.64.0.3"},
		Peers: []*bamboov1.Peer{
			{
				Id:                 "mac-mini",
				Ip:                 "100.64.0.1",
				WireguardPublicKey: peerPriv.PublicKey().Base64(),
				AllowedIps:         []string{"100.64.0.1/32", "192.168.86.0/24"},
			},
		},
	}
	cfg, err := wg.BuildDeviceConfig(priv, resp)
	if err != nil {
		t.Fatalf("BuildDeviceConfig: %v", err)
	}
	if len(cfg.Peers) != 1 {
		t.Fatalf("len(Peers) = %d, want 1", len(cfg.Peers))
	}
	got := make([]string, 0, len(cfg.Peers[0].AllowedIPs))
	for _, p := range cfg.Peers[0].AllowedIPs {
		got = append(got, p.String())
	}
	want := []string{"100.64.0.1/32", "192.168.86.0/24"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("AllowedIPs = %v, want %v (the controller-approved subnet route must survive)", got, want)
	}
}

// Regression for the #170 bump path: after a fresh-tenant admin
// approves the first route, `repo.Policies.Bump` inserts an empty-HCL
// row at revision=1. The controller is now in "enforcing" mode from
// the client's perspective (revision > 0). Empty HCL parses to a
// zero-rule Policy, so `policy.Allow` returns true for every (src,
// dst) pair → `allowedIPsFor` still emits the peer's /32 → the
// "enforcing && len(AllowedIps)==0" drop branch never triggers and
// full-mesh semantics survive.
//
// This test locks that invariant at the client side so a future
// refactor of policy.Allow's zero-rule branch (or the controller's
// allowedIPsFor /32 baseline) can't silently turn a route-approval
// bump into a network-wide black hole.
func TestBuildDeviceConfig_EnforcingWithBaselineAllowedIpsKeepsPeer(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	peerPriv, _ := wg.GeneratePrivateKey()

	resp := &bamboov1.RegisterResponse{
		// revision=1 mirrors what `policies.Bump` writes on the first
		// admin approval of a fresh tenant — empty HCL, but a non-
		// zero revision flips the client into enforcing mode.
		PolicyRevision: 1,
		Self:           &bamboov1.Peer{Ip: "100.64.0.1"},
		Peers: []*bamboov1.Peer{
			{
				Id:                 "p",
				Ip:                 "100.64.0.2",
				WireguardPublicKey: peerPriv.PublicKey().Base64(),
				// Just the /32 baseline `allowedIPsFor` always emits
				// when `policy.Allow` returns true on a zero-rule
				// policy. No approved subnet routes, no exit-node
				// /0 — the minimum surface the bump path produces.
				AllowedIps: []string{"100.64.0.2/32"},
			},
		},
	}
	cfg, err := wg.BuildDeviceConfig(priv, resp)
	if err != nil {
		t.Fatalf("BuildDeviceConfig: %v", err)
	}
	if len(cfg.Peers) != 1 {
		t.Fatalf("len(Peers) = %d, want 1 (peer must NOT be dropped by the enforcing branch when controller emitted a non-empty AllowedIps baseline)", len(cfg.Peers))
	}
	if got := cfg.Peers[0].AllowedIPs[0].String(); got != "100.64.0.2/32" {
		t.Errorf("AllowedIPs[0] = %s, want 100.64.0.2/32", got)
	}
}

func TestBuildDeviceConfig_EnforcingRejectsMalformedAllowedIps(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	peerPriv, _ := wg.GeneratePrivateKey()

	resp := &bamboov1.RegisterResponse{
		PolicyRevision: 1,
		Self:           &bamboov1.Peer{Ip: "100.64.0.1"},
		Peers: []*bamboov1.Peer{
			{
				Id:                 "p",
				Ip:                 "100.64.0.2",
				WireguardPublicKey: peerPriv.PublicKey().Base64(),
				AllowedIps:         []string{"not a cidr"},
			},
		},
	}
	if _, err := wg.BuildDeviceConfig(priv, resp); err == nil {
		t.Error("expected error for malformed AllowedIps")
	}
}

func TestBuildDeviceConfig_SelfIP6(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	resp := &bamboov1.RegisterResponse{
		Self: &bamboov1.Peer{Ip: "100.64.0.1", Ip6: "fdba:1100::6440:1"},
	}
	cfg, err := wg.BuildDeviceConfig(priv, resp)
	if err != nil {
		t.Fatalf("BuildDeviceConfig: %v", err)
	}
	if cfg.Address.String() != "100.64.0.1/32" {
		t.Errorf("Address = %s, want 100.64.0.1/32", cfg.Address)
	}
	if cfg.Address6.String() != "fdba:1100::6440:1/128" {
		t.Errorf("Address6 = %s, want fdba:1100::6440:1/128", cfg.Address6)
	}
}

func TestBuildDeviceConfig_NoIP6LeavesAddress6Invalid(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	resp := &bamboov1.RegisterResponse{
		Self: &bamboov1.Peer{Ip: "100.64.0.1"}, // no Ip6 (legacy controller)
	}
	cfg, err := wg.BuildDeviceConfig(priv, resp)
	if err != nil {
		t.Fatalf("BuildDeviceConfig: %v", err)
	}
	if cfg.Address6.IsValid() {
		t.Errorf("Address6 = %s, want zero/invalid when self has no ip6", cfg.Address6)
	}
}

func TestDeviceConfig_WGQuick_Format(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	peerPriv, _ := wg.GeneratePrivateKey()

	resp := &bamboov1.RegisterResponse{
		Self: &bamboov1.Peer{Ip: "100.64.0.1"},
		Peers: []*bamboov1.Peer{
			{
				Ip:                 "100.64.0.2",
				WireguardPublicKey: peerPriv.PublicKey().Base64(),
				Endpoints:          []string{"203.0.113.5:51820"},
			},
		},
	}
	cfg, err := wg.BuildDeviceConfig(priv, resp)
	if err != nil {
		t.Fatalf("BuildDeviceConfig: %v", err)
	}
	out := cfg.WGQuick()

	for _, want := range []string{
		"[Interface]", "[Peer]",
		"PrivateKey", "PublicKey",
		"100.64.0.1/32", "100.64.0.2/32",
		"203.0.113.5:51820",
		"PersistentKeepalive = 25",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("WGQuick output missing %q. got:\n%s", want, out)
		}
	}
}

func TestBuildDeviceConfig_MalformedSelfIP6Errors(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	resp := &bamboov1.RegisterResponse{
		Self: &bamboov1.Peer{Ip: "100.64.0.1", Ip6: "not-an-ip"},
	}
	if _, err := wg.BuildDeviceConfig(priv, resp); err == nil {
		t.Error("expected error for malformed self ip6")
	}
}
