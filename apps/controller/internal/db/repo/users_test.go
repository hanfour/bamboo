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

// TestUsers_BumpSessionVersion_LeavesUpdatedAt pins that
// BumpSessionVersion does NOT touch updated_at. ListByTenant
// orders by updated_at DESC as the "recently active" sort; a
// force-sign-out is the inverse of activity, so bumping it would
// push the just-signed-out user to the top of the Users admin
// page — an incident-response footgun. Regression here would
// silently re-introduce that misleading display.
func TestUsers_BumpSessionVersion_LeavesUpdatedAt(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	users := repo.NewUsers(pool)

	slug := fmt.Sprintf("svup-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "SVUp", slug, "100.64.201.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	user, err := users.UpsertOIDC(ctx, &repo.User{
		TenantID:     tenant.ID,
		Email:        "svup@example.com",
		OIDCProvider: "test",
		OIDCSubject:  uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	before := user.UpdatedAt

	if _, err := users.BumpSessionVersion(ctx, user.ID); err != nil {
		t.Fatalf("bump: %v", err)
	}

	after, err := users.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !after.UpdatedAt.Equal(before) {
		t.Errorf("UpdatedAt changed after BumpSessionVersion: before=%v after=%v — would corrupt 'recently active' sort",
			before, after.UpdatedAt)
	}
}

// TestUsers_ListByTenant_CarriesSessionVersion pins that
// ListByTenant's SELECT covers session_version. Without this,
// the Users admin page would surface stale 0 values regardless
// of the stored counter — and a future caller minting a JWT
// from a ListByTenant-loaded row would bypass the slice-3b
// force-sign-out gate by carrying sv=0.
func TestUsers_ListByTenant_CarriesSessionVersion(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	users := repo.NewUsers(pool)

	slug := fmt.Sprintf("svlist-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "SVList", slug, "100.64.202.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	user, err := users.UpsertOIDC(ctx, &repo.User{
		TenantID:     tenant.ID,
		Email:        "svlist@example.com",
		OIDCProvider: "test",
		OIDCSubject:  uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	if _, err := users.BumpSessionVersion(ctx, user.ID); err != nil {
		t.Fatalf("bump: %v", err)
	}
	if _, err := users.BumpSessionVersion(ctx, user.ID); err != nil {
		t.Fatalf("bump #2: %v", err)
	}

	list, err := users.ListByTenant(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	var found *repo.User
	for _, u := range list {
		if u.ID == user.ID {
			found = u
			break
		}
	}
	if found == nil {
		t.Fatalf("ListByTenant didn't return the user we just created")
	}
	if found.SessionVersion != 2 {
		t.Errorf("ListByTenant SessionVersion = %d, want 2 (column was dropped from SELECT)", found.SessionVersion)
	}

	got, err := users.GetByEmail(ctx, tenant.ID, "svlist@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if got.SessionVersion != 2 {
		t.Errorf("GetByEmail SessionVersion = %d, want 2 (column was dropped from SELECT)", got.SessionVersion)
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
