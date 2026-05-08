// SPDX-License-Identifier: AGPL-3.0-or-later

package repo_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// requireDB returns a connection pool against the test DSN, or skips the
// test if the env var is not set. Tests are also skipped in short mode.
func requireDB(t *testing.T) *db.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST not set; skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestTenants_CreateAndGet(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	tenants := repo.NewTenants(pool)

	slug := fmt.Sprintf("test-%s", uuid.NewString()[:8])

	created, err := tenants.Create(ctx, "Test Tenant", slug, "100.64.1.0/24")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Slug != slug {
		t.Errorf("slug = %q, want %q", created.Slug, slug)
	}
	if created.IPPool != "100.64.1.0/24" {
		t.Errorf("ip_pool = %q, want 100.64.1.0/24", created.IPPool)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, created.ID)
	})

	got, err := tenants.GetBySlug(ctx, slug)
	if err != nil {
		t.Fatalf("get by slug: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ID mismatch: got %s, want %s", got.ID, created.ID)
	}
}

func TestTenants_GetBySlug_NotFound(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	tenants := repo.NewTenants(pool)

	_, err := tenants.GetBySlug(ctx, "definitely-does-not-exist-"+uuid.NewString())
	if !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
