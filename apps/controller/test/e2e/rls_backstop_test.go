// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"testing"
)

// TestRLSBackstop_ConfinesPeersSelectToTenantGUC pins ADR 0014:
// inside a transaction that sets app.tenant_id = <tenant A>, a bare
// SELECT FROM peers returns only tenant A's rows. The fixture pool
// runs as bamboo_app, so a superuser DSN does not skip the policy.
func TestRLSBackstop_ConfinesPeersSelectToTenantGUC(t *testing.T) {
	f := startFixture(t)
	ctx := context.Background()

	// One peer each in two distinct tenants (helper registers in a fresh
	// tenant per call and cleans it up).
	_, slugA := registerVictimPeerInSeparateTenant(t, f, nil)
	_, slugB := registerVictimPeerInSeparateTenant(t, f, nil)

	var tenantAID string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM tenants WHERE slug = $1`, slugA).Scan(&tenantAID); err != nil {
		t.Fatalf("resolve tenant A id: %v", err)
	}

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	// Transaction-local GUC — the seam RLS keys on (see ADR 0014).
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantAID); err != nil {
		t.Fatalf("set app.tenant_id: %v", err)
	}

	var visible, tenantACount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM peers`).Scan(&visible); err != nil {
		t.Fatalf("count visible peers: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM peers WHERE tenant_id = $1`, tenantAID).Scan(&tenantACount); err != nil {
		t.Fatalf("count tenant A peers: %v", err)
	}
	if tenantACount < 1 {
		t.Fatalf("tenant A has no peers; the fixture did not register one")
	}
	if visible != tenantACount {
		t.Errorf("RLS backstop: bare SELECT saw %d peers under app.tenant_id=A, "+
			"want only tenant A's %d", visible, tenantACount)
	}

	var tenantBID string
	if err := tx.QueryRow(ctx, `SELECT id FROM tenants WHERE slug = $1`, slugB).Scan(&tenantBID); err != nil {
		t.Fatalf("resolve tenant B id: %v", err)
	}
	_, insErr := tx.Exec(ctx, `
		INSERT INTO peers (tenant_id, hostname, wireguard_public_key, ip, approval_status)
		VALUES ($1, 'cross', $2, '100.90.1.1', 'pending')`, tenantBID, randomPubKey(t))
	if insErr == nil {
		t.Fatal("WITH CHECK accepted an insert for tenant B while app.tenant_id was A")
	}
}

// TestRLSBackstop_UnsetGUCSeesNoPeers pins the fail-closed default:
// a bamboo_app query with no app.tenant_id matches zero peer rows.
func TestRLSBackstop_UnsetGUCSeesNoPeers(t *testing.T) {
	f := startFixture(t)
	ctx := context.Background()
	registerVictimPeerInSeparateTenant(t, f, nil)

	var role string
	if err := f.pool.QueryRow(ctx, `SELECT current_user`).Scan(&role); err != nil {
		t.Fatalf("current_user: %v", err)
	}
	if role != "bamboo_app" {
		t.Fatalf("pool role = %q, want bamboo_app (superusers bypass RLS)", role)
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM peers`).Scan(&n); err != nil {
		t.Fatalf("count peers: %v", err)
	}
	if n != 0 {
		t.Errorf("unset app.tenant_id saw %d peers, want 0", n)
	}
}
