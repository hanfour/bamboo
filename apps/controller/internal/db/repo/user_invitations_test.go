// SPDX-License-Identifier: AGPL-3.0-or-later

package repo_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// TestUserInvitations_RevokeExpired pins the auto-revoke reaper
// contract. Four shapes must behave correctly:
//
//   - expired + unredeemed              → revoked
//   - not yet expired                   → untouched
//   - past expiry + accepted            → untouched (terminal state)
//   - past expiry + already revoked     → untouched (idempotent)
//
// The whole point of this reaper is to free the
// (tenant_id, lower(email)) partial-unique slot so an admin can
// re-invite the same email after the previous attempt expired.
// Without RevokeExpired the re-invite would 409 forever.
func TestUserInvitations_RevokeExpired(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	invs := repo.NewUserInvitations(pool)
	slug := fmt.Sprintf("inv-exp-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "InvExp", slug, "100.64.86.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	// Need a user row to satisfy accepted_by FK on the
	// already-accepted shape below.
	users := repo.NewUsers(pool)
	user, err := users.UpsertOIDC(ctx, &repo.User{
		TenantID:     tenant.ID,
		Email:        "host@example.com",
		OIDCProvider: "test",
		OIDCSubject:  uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	mk := func(email string, expiresAt time.Time) *repo.UserInvitation {
		t.Helper()
		inv, err := invs.Create(ctx, &repo.UserInvitation{
			ID:        uuid.New(),
			TenantID:  tenant.ID,
			Email:     email,
			IsAdmin:   false,
			TokenHash: "stand-in-hash",
			ExpiresAt: expiresAt,
		})
		if err != nil {
			t.Fatalf("Create %s: %v", email, err)
		}
		return inv
	}

	now := time.Now().UTC()
	expiredOpen := mk("expired@example.com", now.Add(-time.Hour))
	freshOpen := mk("fresh@example.com", now.Add(24*time.Hour))
	expiredAccepted := mk("accepted@example.com", now.Add(-time.Hour))
	expiredRevoked := mk("revoked@example.com", now.Add(-time.Hour))

	// Promote expiredAccepted to accepted; expiredRevoked to revoked.
	if err := invs.MarkAccepted(ctx, expiredAccepted.ID, user.ID); err != nil {
		t.Fatalf("MarkAccepted: %v", err)
	}
	if err := invs.MarkRevoked(ctx, expiredRevoked.ID, user.ID); err != nil {
		t.Fatalf("MarkRevoked: %v", err)
	}

	got, err := invs.RevokeExpired(ctx, now)
	if err != nil {
		t.Fatalf("RevokeExpired: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("RevokeExpired returned %d rows, want 1; ids=%v", len(got), idsOf(got))
	}
	if got[0].ID != expiredOpen.ID {
		t.Errorf("revoked id = %s, want %s (expiredOpen)", got[0].ID, expiredOpen.ID)
	}
	if got[0].Email != "expired@example.com" {
		t.Errorf("revoked email = %s", got[0].Email)
	}

	// Verify the actual rows match expectations.
	check := func(id uuid.UUID, wantRevoked bool) {
		t.Helper()
		row, err := invs.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("GetByID %s: %v", id, err)
		}
		if (row.RevokedAt != nil) != wantRevoked {
			t.Errorf("id=%s: revoked=%v, want %v", id, row.RevokedAt != nil, wantRevoked)
		}
	}
	check(expiredOpen.ID, true)
	check(freshOpen.ID, false)
	check(expiredAccepted.ID, false) // accepted_at gates the UPDATE
	check(expiredRevoked.ID, true)   // already revoked; stays revoked

	// A second RevokeExpired call must be idempotent — no new rows
	// to revoke (everything that could be revoked is).
	second, err := invs.RevokeExpired(ctx, now)
	if err != nil {
		t.Fatalf("second RevokeExpired: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("second RevokeExpired returned %d rows, want 0 (idempotent)", len(second))
	}
}

// TestUserInvitations_RevokeExpiredFreesPartialUniqueSlot is the
// motivation test: an expired invitation blocks re-invitation
// of the same email via the partial unique index. After the
// reaper revokes the expired row, re-invitation succeeds.
//
// Without this guarantee the reaper's value is invisible —
// the partial-unique-index release is the whole point.
func TestUserInvitations_RevokeExpiredFreesPartialUniqueSlot(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	tenants := repo.NewTenants(pool)
	invs := repo.NewUserInvitations(pool)
	slug := fmt.Sprintf("reinv-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "Reinv", slug, "100.64.85.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	now := time.Now().UTC()
	_, err = invs.Create(ctx, &repo.UserInvitation{
		ID:        uuid.New(),
		TenantID:  tenant.ID,
		Email:     "alice@example.com",
		TokenHash: "h",
		ExpiresAt: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	// Re-invitation while the expired row still satisfies the
	// partial-unique predicate must fail — that's the bug the
	// reaper fixes.
	_, err = invs.Create(ctx, &repo.UserInvitation{
		ID:        uuid.New(),
		TenantID:  tenant.ID,
		Email:     "alice@example.com",
		TokenHash: "h2",
		ExpiresAt: now.Add(24 * time.Hour),
	})
	if err == nil {
		t.Fatal("expected re-invite before reaper to fail with duplicate-key error; got nil")
	}

	// Run the reaper.
	if _, err := invs.RevokeExpired(ctx, now); err != nil {
		t.Fatalf("RevokeExpired: %v", err)
	}
	// Re-invitation now succeeds.
	if _, err := invs.Create(ctx, &repo.UserInvitation{
		ID:        uuid.New(),
		TenantID:  tenant.ID,
		Email:     "alice@example.com",
		TokenHash: "h3",
		ExpiresAt: now.Add(24 * time.Hour),
	}); err != nil {
		t.Errorf("post-reaper re-invite failed: %v", err)
	}
}

func idsOf(xs []*repo.ExpiredInvite) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(xs))
	for _, x := range xs {
		out = append(out, x.ID)
	}
	return out
}
