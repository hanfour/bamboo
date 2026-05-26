// SPDX-License-Identifier: AGPL-3.0-or-later

package repo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// TestAuditLogs_Insert_PopulatesOccurredAt pins the #222 follow-up
// contract: AuditLogs.Insert writes the persisted occurred_at back
// onto the supplied AuditEvent via RETURNING, so callers reading
// e.OccurredAt after Insert see the actually-stored value rather
// than the Go zero (year 1). Two cases:
//
//	a) Caller leaves OccurredAt zero → DB DEFAULT now() applies →
//	   struct ends up with the DB-side timestamp.
//	b) Caller pre-sets OccurredAt (e.g. erase handler pinning the
//	   response timestamp) → that exact value is persisted and
//	   round-tripped back unchanged.
func TestAuditLogs_Insert_PopulatesOccurredAt(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	slug := fmt.Sprintf("audit-occ-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "Occ", slug, "100.64.230.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	audits := repo.NewAuditLogs(pool)

	// Case (a): caller leaves OccurredAt zero.
	before := time.Now().UTC().Add(-time.Second)
	ev := &repo.AuditEvent{
		TenantID:     &tenant.ID,
		ActorType:    "system",
		Action:       "test.default",
		ResourceType: "test",
	}
	if err := audits.Insert(ctx, ev); err != nil {
		t.Fatalf("Insert default: %v", err)
	}
	if ev.OccurredAt.IsZero() {
		t.Errorf("OccurredAt left zero after Insert; expected RETURNING-populated value")
	}
	if ev.OccurredAt.Before(before) {
		t.Errorf("OccurredAt = %v is before test start %v", ev.OccurredAt, before)
	}

	// Case (b): caller pre-sets OccurredAt — must round-trip.
	//
	// The pinned value is intentionally close to now() rather than
	// a historical date. The tenant cleanup in t.Cleanup cascades
	// audit_log.tenant_id → NULL (per 00001), not DELETE, so the
	// row survives this test's scope. A historical pinned value
	// would later be eaten by TestAuditLogs_DeleteOlderThan's
	// retention sweep (cutoff = now-30d, no tenant filter), tipping
	// its expected count from 1 to 2.
	//
	// Postgres timestamptz has microsecond precision; truncate to
	// micros so Equal() doesn't fail on the trailing nanoseconds
	// Go time.Now() carries that the DB rounds away.
	pinned := time.Now().UTC().Truncate(time.Microsecond)
	ev2 := &repo.AuditEvent{
		TenantID:     &tenant.ID,
		ActorType:    "system",
		Action:       "test.pinned",
		ResourceType: "test",
		OccurredAt:   pinned,
	}
	if err := audits.Insert(ctx, ev2); err != nil {
		t.Fatalf("Insert pinned: %v", err)
	}
	if !ev2.OccurredAt.Equal(pinned) {
		t.Errorf("OccurredAt = %v, want %v (caller-supplied timestamp must round-trip)", ev2.OccurredAt, pinned)
	}
}

func TestAuditLogs_InsertAndList(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	slug := fmt.Sprintf("audit-test-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "Audit Test", slug, "100.64.97.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	audits := repo.NewAuditLogs(pool)

	for _, action := range []string{"peer.register", "policy.update", "preauthkey.create"} {
		err := audits.Insert(ctx, &repo.AuditEvent{
			TenantID:     &tenant.ID,
			ActorType:    "system",
			Action:       action,
			ResourceType: action[:len("peer")], // arbitrary; tests only assert insert + list
			Diff:         json.RawMessage(`{"k":"v"}`),
		})
		if err != nil {
			t.Fatalf("Insert %q: %v", action, err)
		}
	}

	events, err := audits.ListByTenant(ctx, tenant.ID, 10)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if got := len(events); got != 3 {
		t.Errorf("listed %d events, want 3", got)
	}
	// Newest first: last inserted is first in the result.
	if events[0].Action != "preauthkey.create" {
		t.Errorf("events[0].Action = %q, want preauthkey.create", events[0].Action)
	}
}

// TestAuditLogs_ListByResource covers the per-peer timeline path:
//
//	(a) only events matching (tenant, resource_type, resource_id) come back;
//	(b) order is newest-first;
//	(c) limit is honored (default 50, clamped to 200);
//	(d) other-tenant events with the same resource_id do NOT leak.
func TestAuditLogs_ListByResource(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	slugA := fmt.Sprintf("audit-by-res-a-%s", uuid.NewString()[:8])
	tenantA, err := tenants.Create(ctx, "By-Res A", slugA, "100.64.0.0/24")
	if err != nil {
		t.Fatalf("create tenant A: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantA.ID)
	})
	slugB := fmt.Sprintf("audit-by-res-b-%s", uuid.NewString()[:8])
	tenantB, err := tenants.Create(ctx, "By-Res B", slugB, "100.65.0.0/24")
	if err != nil {
		t.Fatalf("create tenant B: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantB.ID)
	})

	audits := repo.NewAuditLogs(pool)
	peerID := uuid.New()
	otherPeerID := uuid.New()

	insert := func(tenantID uuid.UUID, resType string, resID uuid.UUID, action string) {
		t.Helper()
		if err := audits.Insert(ctx, &repo.AuditEvent{
			TenantID:     &tenantID,
			ActorType:    "system",
			Action:       action,
			ResourceType: resType,
			ResourceID:   &resID,
		}); err != nil {
			t.Fatalf("insert %s: %v", action, err)
		}
	}

	// Target peer in tenant A: 3 events.
	insert(tenantA.ID, "peer", peerID, "peer.register")
	insert(tenantA.ID, "peer", peerID, "peer.update")
	insert(tenantA.ID, "peer", peerID, "peer.delete")
	// Same peer-id but tenant B (collision test): must not leak.
	insert(tenantB.ID, "peer", peerID, "peer.register")
	// Different resource in tenant A: must not match.
	insert(tenantA.ID, "peer", otherPeerID, "peer.update")
	// Different resource_type: must not match.
	insert(tenantA.ID, "policy", peerID, "policy.update")

	got, err := audits.ListByResource(ctx, tenantA.ID, "peer", peerID, 50)
	if err != nil {
		t.Fatalf("ListByResource: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (cross-tenant + cross-resource events leaked?)", len(got))
	}
	wantActions := []string{"peer.delete", "peer.update", "peer.register"}
	for i, w := range wantActions {
		if got[i].Action != w {
			t.Errorf("got[%d].Action = %q, want %q (newest-first order)", i, got[i].Action, w)
		}
	}

	// limit honored.
	gotOne, _ := audits.ListByResource(ctx, tenantA.ID, "peer", peerID, 1)
	if len(gotOne) != 1 {
		t.Errorf("limit=1 returned %d events", len(gotOne))
	}

	// limit <= 0 defaults to 50 (we only inserted 3 so 3 is what we see).
	gotDefault, _ := audits.ListByResource(ctx, tenantA.ID, "peer", peerID, 0)
	if len(gotDefault) != 3 {
		t.Errorf("limit=0 should default to 50, returned %d", len(gotDefault))
	}
}

// TestAuditLogs_UpdateBlockedByTrigger pins the migration-00013
// contract: any attempt to UPDATE an audit_log row gets refused
// by the BEFORE trigger. Without this guarantee a stolen DB
// credential could silently rewrite history — and the webhook
// delivery model that landed alongside it (#180/#181) treats the
// audit row as the source of truth for event reconstruction.
func TestAuditLogs_UpdateBlockedByTrigger(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	audits := repo.NewAuditLogs(pool)
	slug := fmt.Sprintf("immut-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "Immut", slug, "100.64.93.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	if err := audits.Insert(ctx, &repo.AuditEvent{
		TenantID:     &tenant.ID,
		ActorType:    "system",
		Action:       "peer.register",
		ResourceType: "peer",
	}); err != nil {
		t.Fatalf("seed Insert: %v", err)
	}

	// Direct UPDATE attempt must fail. The trigger raises a
	// plain-language error; we don't assert on the exact text
	// but the operation must NOT succeed.
	_, uerr := pool.Exec(ctx,
		`UPDATE audit_log SET action = 'tampered' WHERE tenant_id = $1`,
		tenant.ID)
	if uerr == nil {
		t.Error("UPDATE audit_log succeeded; want trigger rejection")
	}
	// Sanity: the row's action is still the original value.
	var action string
	if err := pool.QueryRow(ctx,
		`SELECT action FROM audit_log WHERE tenant_id = $1 LIMIT 1`,
		tenant.ID).Scan(&action); err != nil {
		t.Fatalf("post-UPDATE Read: %v", err)
	}
	if action != "peer.register" {
		t.Errorf("row's action mutated to %q despite trigger", action)
	}
}

// TestAuditLogs_DeleteBlockedByDefault pins the rejection path
// for casual DELETE. The bypass via SET LOCAL is verified in the
// companion test.
func TestAuditLogs_DeleteBlockedByDefault(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	audits := repo.NewAuditLogs(pool)
	slug := fmt.Sprintf("immut-del-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "ImmutDel", slug, "100.64.92.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	if err := audits.Insert(ctx, &repo.AuditEvent{
		TenantID:     &tenant.ID,
		ActorType:    "system",
		Action:       "policy.update",
		ResourceType: "policy",
	}); err != nil {
		t.Fatalf("seed Insert: %v", err)
	}

	_, derr := pool.Exec(ctx, `DELETE FROM audit_log WHERE tenant_id = $1`, tenant.ID)
	if derr == nil {
		t.Error("DELETE audit_log succeeded; want trigger rejection")
	}
	var count int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1`,
		tenant.ID).Scan(&count); err != nil {
		t.Fatalf("post-DELETE count: %v", err)
	}
	if count == 0 {
		t.Error("DELETE removed rows despite trigger")
	}
}

// TestAuditLogs_DeleteBypassWithSessionVar pins the documented
// escape hatch for future retention / GDPR-erasure flows: a
// transaction that SET LOCAL bamboo.allow_audit_delete = 'true'
// may DELETE. Future callers wrap their purge inside a tx that
// opts in; the bypass disappears at commit/rollback so a stray
// connection never inherits it.
func TestAuditLogs_DeleteBypassWithSessionVar(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	audits := repo.NewAuditLogs(pool)
	slug := fmt.Sprintf("immut-bypass-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "ImmutBypass", slug, "100.64.91.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	if err := audits.Insert(ctx, &repo.AuditEvent{
		TenantID:     &tenant.ID,
		ActorType:    "system",
		Action:       "peer.delete",
		ResourceType: "peer",
	}); err != nil {
		t.Fatalf("seed Insert: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// set_config(name, value, is_local=true) is the function-call
	// equivalent of `SET LOCAL` and works inside a pgx.Exec.
	if _, err := tx.Exec(ctx, `SELECT set_config('bamboo.allow_audit_delete', 'true', true)`); err != nil {
		t.Fatalf("set bypass: %v", err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM audit_log WHERE tenant_id = $1`, tenant.ID)
	if err != nil {
		t.Fatalf("bypassed DELETE: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Errorf("DELETE removed %d rows, want 1", tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// After commit the bypass is gone; a follow-up DELETE on a
	// fresh connection sees the trigger reject again. This
	// guarantees the bypass doesn't leak between transactions.
	if err := audits.Insert(ctx, &repo.AuditEvent{
		TenantID:     &tenant.ID,
		ActorType:    "system",
		Action:       "peer.delete.followup",
		ResourceType: "peer",
	}); err != nil {
		t.Fatalf("post-bypass Insert: %v", err)
	}
	_, derr := pool.Exec(ctx, `DELETE FROM audit_log WHERE tenant_id = $1`, tenant.ID)
	if derr == nil {
		t.Error("bypass leaked outside transaction; DELETE succeeded")
	}
}

// TestAuditLogs_DeleteOlderThan pins the §4 P2 retention-reaper
// happy path: insert three rows at known timestamps (via direct
// pool.Exec since Insert always uses now()), call DeleteOlderThan
// with a cutoff that catches two of them, assert only the recent
// row survives.
//
// Also exercises the trigger-bypass path end-to-end — the DELETE
// runs against the live BEFORE-DELETE trigger from #182, so a
// silent regression (forgot SET LOCAL; trigger blocks; reaper
// returns 0 forever) would surface here as either a tag count
// of 0 or a Postgres-level error.
func TestAuditLogs_DeleteOlderThan(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	slug := fmt.Sprintf("audit-reaper-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "Audit Reaper", slug, "100.64.98.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		// Use bypass to clean up — the trigger blocks bare DELETE
		// and we're not testing that here.
		_, _ = pool.Exec(ctx, `
			DO $$ BEGIN
				PERFORM set_config('bamboo.allow_audit_delete', 'true', false);
				DELETE FROM audit_log WHERE tenant_id = $1::uuid;
			END $$
		`, tenant.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	audits := repo.NewAuditLogs(pool)

	// Insert three rows at three timestamps. Use direct SQL to
	// set occurred_at — Insert always uses now() which we can't
	// backdate.
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	tsAges := []string{"INTERVAL '40 days'", "INTERVAL '20 days'", "INTERVAL '1 day'"}
	for i, id := range ids {
		_, err := pool.Exec(ctx, fmt.Sprintf(`
			INSERT INTO audit_log (id, tenant_id, actor_type, action, resource_type, occurred_at)
			VALUES ($1, $2, 'system', 'test', 'test', now() - %s)
		`, tsAges[i]), id, tenant.ID)
		if err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	// Cutoff = 30 days ago. Expect rows 0 (40d old) deleted,
	// rows 1 (20d) + 2 (1d) survive.
	cutoff := time.Now().UTC().AddDate(0, 0, -30)
	deleted, err := audits.DeleteOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteOlderThan: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1 (40d row only)", deleted)
	}

	var survivors int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1`,
		tenant.ID,
	).Scan(&survivors); err != nil {
		t.Fatalf("count: %v", err)
	}
	if survivors != 2 {
		t.Errorf("survivors = %d, want 2 (20d + 1d)", survivors)
	}

	// Re-run with same cutoff: idempotent — nothing left to delete.
	deleted, err = audits.DeleteOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("idempotent DeleteOlderThan: %v", err)
	}
	if deleted != 0 {
		t.Errorf("second pass deleted = %d, want 0 (idempotent)", deleted)
	}
}

// TestAuditLogs_DeleteOlderThan_ZeroCutoffNoop pins the defensive
// case: cutoff = time.Time{} (zero value) must NOT run a
// "DELETE FROM audit_log" with no filter. A bug in the caller that
// forgot to compute now-window could otherwise wipe the entire
// table.
func TestAuditLogs_DeleteOlderThan_ZeroCutoffNoop(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	audits := repo.NewAuditLogs(pool)

	deleted, err := audits.DeleteOlderThan(ctx, time.Time{})
	if err != nil {
		t.Fatalf("zero cutoff returned error: %v", err)
	}
	if deleted != 0 {
		t.Errorf("zero cutoff deleted = %d, want 0", deleted)
	}
}
