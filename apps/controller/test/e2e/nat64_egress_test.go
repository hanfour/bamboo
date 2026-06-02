// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// TestRegister_NAT64EgressActive asserts the controller computes
// RegisterResponse.nat64_egress_active = peer.nat64_egress_approved &&
// tenant.dns64_enabled (NAT64 Phase C2). It exercises the real register
// path and flips the two flags via the repos the fixture exposes.
func TestRegister_NAT64EgressActive(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())

	pubKey := randomPubKey(t)

	// 1. Initial register: neither flag set, so egress must be inactive.
	resp, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Hostname:           "egress-node",
		WireguardPublicKey: pubKey,
		Os:                 "linux",
	})
	if err != nil {
		t.Fatalf("initial Register: %v", err)
	}
	if resp.GetNat64EgressActive() {
		t.Fatalf("initial nat64_egress_active = true, want false (not approved, dns64 off)")
	}

	peerID, err := uuid.Parse(resp.GetSelf().GetId())
	if err != nil {
		t.Fatalf("parse peer id: %v", err)
	}

	bg := context.Background()
	peers := repo.NewPeers(f.pool)
	tenants := repo.NewTenants(f.pool)

	tenant, err := tenants.GetBySlug(bg, f.tenantSlug)
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}

	// 2. Approve the peer AND enable DNS64 for the tenant.
	if err := peers.SetNAT64EgressApproved(bg, peerID, true); err != nil {
		t.Fatalf("SetNAT64EgressApproved: %v", err)
	}
	if _, err := tenants.SetNAT64Config(bg, tenant.ID, "2001:db8:64::/96", true); err != nil {
		t.Fatalf("SetNAT64Config enable: %v", err)
	}

	// 3. Re-register the same key: both flags true → egress active.
	resp, err = f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Hostname:           "egress-node",
		WireguardPublicKey: pubKey,
		Os:                 "linux",
	})
	if err != nil {
		t.Fatalf("re-Register (approved+dns64): %v", err)
	}
	if !resp.GetNat64EgressActive() {
		t.Errorf("nat64_egress_active = false, want true (approved && dns64_enabled)")
	}

	// 4. Disable DNS64 only (peer stays approved): proves the AND —
	//    approval alone is not enough.
	if _, err := tenants.SetNAT64Config(bg, tenant.ID, "", false); err != nil {
		t.Fatalf("SetNAT64Config disable: %v", err)
	}
	resp, err = f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Hostname:           "egress-node",
		WireguardPublicKey: pubKey,
		Os:                 "linux",
	})
	if err != nil {
		t.Fatalf("re-Register (approved, dns64 off): %v", err)
	}
	if resp.GetNat64EgressActive() {
		t.Errorf("nat64_egress_active = true, want false (approved but dns64 disabled)")
	}
}
