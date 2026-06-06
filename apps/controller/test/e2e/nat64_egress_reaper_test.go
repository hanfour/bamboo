// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// TestReconcileNAT64Egress_StaleActiveFailsOver: two approved+healthy
// egresses, dns64 on; the lower-UUID (active) one goes stale; one
// ReconcileNAT64Egress sweep must mark it unhealthy/'stale', fail over to
// the higher-UUID healthy egress, and bump (selection changed).
func TestReconcileNAT64Egress_StaleActiveFailsOver(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())
	bg := context.Background()
	peers := repo.NewPeers(f.pool)
	tenants := repo.NewTenants(f.pool)

	regA, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{Hostname: "egA", WireguardPublicKey: randomPubKey(t), Os: "linux"})
	if err != nil {
		t.Fatalf("register egA: %v", err)
	}
	regB, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{Hostname: "egB", WireguardPublicKey: randomPubKey(t), Os: "linux"})
	if err != nil {
		t.Fatalf("register egB: %v", err)
	}
	idA := uuid.MustParse(regA.GetSelf().GetId())
	idB := uuid.MustParse(regB.GetSelf().GetId())

	tn, err := tenants.GetBySlug(bg, f.tenantSlug)
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if _, err := tenants.SetNAT64Config(bg, tn.ID, "64:ff9b::/96", true); err != nil {
		t.Fatal(err)
	}
	if err := peers.SetNAT64EgressApproved(bg, idA, true); err != nil {
		t.Fatal(err)
	}
	if err := peers.SetNAT64EgressApproved(bg, idB, true); err != nil {
		t.Fatal(err)
	}
	if err := peers.SetNAT64EgressHealth(bg, idA, true); err != nil {
		t.Fatal(err)
	}
	if err := peers.SetNAT64EgressHealth(bg, idB, true); err != nil {
		t.Fatal(err)
	}

	lo, hi := idA, idB
	if idB.String() < idA.String() {
		lo, hi = idB, idA
	}

	// Make selection deterministic regardless of devmode register's
	// approval behavior: both must be mesh-approved + online for
	// selectEgress to consider them. Harmless if register already set these.
	if _, err := f.pool.Exec(bg, `UPDATE peers SET approval_status='approved', status='online' WHERE id = ANY($1)`, []uuid.UUID{lo, hi}); err != nil {
		t.Fatal(err)
	}
	// Both fresh, then backdate the active (lo) past the staleness window.
	if _, err := f.pool.Exec(bg, `UPDATE peers SET last_seen_at = now() WHERE id = ANY($1)`, []uuid.UUID{lo, hi}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(bg, `UPDATE peers SET last_seen_at = now() - interval '5 minutes' WHERE id = $1`, lo); err != nil {
		t.Fatal(err)
	}

	// prevSelected = lo (what selection was before lo went stale).
	// ReconcileNAT64Egress is server-side only, so use the in-process
	// handler (f.coordSrv), not the gRPC client (f.coord).
	selected, bumped, err := f.coordSrv.ReconcileNAT64Egress(bg, tn.ID, lo)
	if err != nil {
		t.Fatalf("ReconcileNAT64Egress: %v", err)
	}
	if selected != hi {
		t.Errorf("selected = %v, want hi (%v) after lo went stale", selected, hi)
	}
	if !bumped {
		t.Errorf("bumped = false, want true (selection changed lo→hi)")
	}
	gotLo, _ := peers.GetByID(bg, lo)
	if gotLo.NAT64EgressHealthStatus == nil || *gotLo.NAT64EgressHealthStatus != "unhealthy" {
		t.Errorf("lo status = %v, want unhealthy", gotLo.NAT64EgressHealthStatus)
	}
	if gotLo.NAT64EgressHealthReason == nil || *gotLo.NAT64EgressHealthReason != "stale" {
		t.Errorf("lo reason = %v, want 'stale'", gotLo.NAT64EgressHealthReason)
	}

	// Idempotent: a 2nd sweep with prevSelected=hi makes no change → no bump.
	selected2, bumped2, err := f.coordSrv.ReconcileNAT64Egress(bg, tn.ID, hi)
	if err != nil {
		t.Fatal(err)
	}
	if selected2 != hi || bumped2 {
		t.Errorf("second sweep: selected=%v bumped=%v, want hi/false", selected2, bumped2)
	}
}
