// SPDX-License-Identifier: AGPL-3.0-or-later

package repo_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// TestUsers_BumpSessionVersion pins the slice-3b contract: the
// counter starts at 0, bumps monotonically, and the returned value
// is the new (post-bump) version. Auth middleware compares
// claims.sv against this, so a regression here would either
// silently no-op a force-sign-out or double-count.
func TestUsers_BumpSessionVersion(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	users := repo.NewUsers(pool)

	slug := fmt.Sprintf("sv-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "SV", slug, "100.64.200.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	user, err := users.UpsertOIDC(ctx, &repo.User{
		TenantID:     tenant.ID,
		Email:        "sv@example.com",
		OIDCProvider: "test",
		OIDCSubject:  uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	if user.SessionVersion != 0 {
		t.Fatalf("fresh user SessionVersion = %d, want 0", user.SessionVersion)
	}

	next, err := users.BumpSessionVersion(ctx, user.ID)
	if err != nil {
		t.Fatalf("bump #1: %v", err)
	}
	if next != 1 {
		t.Errorf("first bump returned %d, want 1", next)
	}

	next, err = users.BumpSessionVersion(ctx, user.ID)
	if err != nil {
		t.Fatalf("bump #2: %v", err)
	}
	if next != 2 {
		t.Errorf("second bump returned %d, want 2", next)
	}

	// Read-back also reflects the bumped value.
	got, err := users.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.SessionVersion != 2 {
		t.Errorf("GetByID SessionVersion = %d, want 2", got.SessionVersion)
	}
}

// TestUsers_BumpSessionVersion_NotFound checks that bumping a
// non-existent user surfaces ErrNotFound rather than a bare
// pgx.ErrNoRows. The handler relies on this to translate to a 404.
func TestUsers_BumpSessionVersion_NotFound(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	users := repo.NewUsers(pool)

	_, err := users.BumpSessionVersion(ctx, uuid.New())
	if !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("BumpSessionVersion(missing) err = %v, want ErrNotFound", err)
	}
}
