// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"testing"
)

// TestRLSBackstop_ConfinesPeersSelectToTenantGUC is the RED pin for
// docs/adr/0014-tenant-isolation-rls-backstop.md. It is skipped until the
// RLS policies + the app.tenant_id transaction-GUC seam land; the rollout's
// final step un-skips it, at which point it must go green.
//
// Contract: inside a transaction that sets app.tenant_id = <tenant A>, a
// bare `SELECT ... FROM peers` (no WHERE clause) must return ONLY tenant
// A's rows. Today, with no RLS, it returns every tenant's rows — exactly
// the cross-tenant exposure the DB-level backstop exists to prevent.
func TestRLSBackstop_ConfinesPeersSelectToTenantGUC(t *testing.T) {
	t.Skip("pending RLS backstop — see docs/adr/0014-tenant-isolation-rls-backstop.md; un-skip when RLS policies + the app.tenant_id GUC seam land")

	f := startFixture(t)
	ctx := context.Background()

	// One peer each in two distinct tenants (helper registers in a fresh
	// tenant per call and cleans it up).
	_, slugA := registerVictimPeerInSeparateTenant(t, f, nil)
	_, _ = registerVictimPeerInSeparateTenant(t, f, nil)

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
	if visible != tenantACount {
		t.Errorf("RLS backstop: bare SELECT saw %d peers under app.tenant_id=A, "+
			"want only tenant A's %d — RLS is not confining rows to the GUC", visible, tenantACount)
	}
}
