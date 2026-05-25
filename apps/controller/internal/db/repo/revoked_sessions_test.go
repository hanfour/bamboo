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

// TestRevokedSessions_RoundTrip covers the slice-3a denylist
// contract: insert a jti, see it as revoked, double-insert is
// idempotent. Expired-jti pruning has its own test below.
func TestRevokedSessions_RoundTrip(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	users := repo.NewUsers(pool)
	rs := repo.NewRevokedSessions(pool)

	slug := fmt.Sprintf("rs-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "RS", slug, "100.64.190.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	user, err := users.UpsertOIDC(ctx, &repo.User{
		TenantID:     tenant.ID,
		Email:        "rs@example.com",
		OIDCProvider: "test",
		OIDCSubject:  uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	jti := uuid.New()

	// Fresh jti — not revoked.
	revoked, err := rs.IsRevoked(ctx, jti)
	if err != nil {
		t.Fatalf("IsRevoked pre: %v", err)
	}
	if revoked {
		t.Errorf("fresh jti reports revoked")
	}

	if err := rs.Insert(ctx, jti, user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	revoked, err = rs.IsRevoked(ctx, jti)
	if err != nil {
		t.Fatalf("IsRevoked post: %v", err)
	}
	if !revoked {
		t.Errorf("inserted jti not reported as revoked")
	}

	// Idempotent — a double sign-out must not error.
	if err := rs.Insert(ctx, jti, user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Errorf("double Insert errored: %v", err)
	}

	// Zero jti is never revoked (legacy-token contract).
	revoked, err = rs.IsRevoked(ctx, uuid.Nil)
	if err != nil {
		t.Fatalf("IsRevoked zero: %v", err)
	}
	if revoked {
		t.Errorf("zero jti reports revoked")
	}
}

// TestRevokedSessions_DeleteExpired pins the reaper contract.
// Rows whose underlying JWT has passed exp are redundant (the
// verifier rejects on its own) and must be pruned; rows whose JWT
// is still alive stay.
func TestRevokedSessions_DeleteExpired(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	users := repo.NewUsers(pool)
	rs := repo.NewRevokedSessions(pool)

	slug := fmt.Sprintf("rs-exp-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "RSExp", slug, "100.64.191.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	user, err := users.UpsertOIDC(ctx, &repo.User{
		TenantID:     tenant.ID,
		Email:        "rsexp@example.com",
		OIDCProvider: "test",
		OIDCSubject:  uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	pastJTI := uuid.New()
	futureJTI := uuid.New()

	if err := rs.Insert(ctx, pastJTI, user.ID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("Insert past: %v", err)
	}
	if err := rs.Insert(ctx, futureJTI, user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Insert future: %v", err)
	}

	deleted, err := rs.DeleteExpired(ctx, time.Now())
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	gone, _ := rs.IsRevoked(ctx, pastJTI)
	if gone {
		t.Errorf("past jti still revoked after sweep")
	}
	alive, _ := rs.IsRevoked(ctx, futureJTI)
	if !alive {
		t.Errorf("future jti was incorrectly swept")
	}
}
