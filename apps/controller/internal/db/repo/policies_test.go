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

func TestPolicies_PutThenGet(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	slug := fmt.Sprintf("pol-test-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "Pol Test", slug, "100.64.99.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	policies := repo.NewPolicies(pool)

	first, err := policies.Put(ctx, tenant.ID, `rule "a" { action="allow" sources=["*"] destinations=["*:*"] }`, nil, 0)
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if first.Revision != 1 {
		t.Errorf("first revision = %d, want 1", first.Revision)
	}

	got, err := policies.Get(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Revision != 1 {
		t.Errorf("got revision = %d, want 1", got.Revision)
	}

	second, err := policies.Put(ctx, tenant.ID, `rule "b" { action="deny" sources=["*"] destinations=["*:*"] }`, nil, 0)
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if second.Revision != 2 {
		t.Errorf("second revision = %d, want 2", second.Revision)
	}
}

func TestPolicies_RevisionMismatch(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	slug := fmt.Sprintf("rev-test-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "Rev Test", slug, "100.64.98.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	policies := repo.NewPolicies(pool)

	_, err = policies.Put(ctx, tenant.ID, `rule "a" { action="allow" sources=["*"] destinations=["*:*"] }`, nil, 0)
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}

	// Caller thought current revision was 0; should mismatch (now 1).
	_, err = policies.Put(ctx, tenant.ID, `rule "x" { action="deny" sources=["*"] destinations=["*:*"] }`, nil, 0)
	if err != nil {
		t.Fatalf("Put with expected=0 should treat as no-check, got: %v", err)
	}

	// Now it's at revision 2; passing expected=99 must fail.
	_, err = policies.Put(ctx, tenant.ID, `rule "y" { action="deny" sources=["*"] destinations=["*:*"] }`, nil, 99)
	if !errors.Is(err, repo.ErrRevisionMismatch) {
		t.Errorf("err = %v, want ErrRevisionMismatch", err)
	}

	// And expected=2 (matches current) succeeds.
	_, err = policies.Put(ctx, tenant.ID, `rule "z" { action="deny" sources=["*"] destinations=["*:*"] }`, nil, 2)
	if err != nil {
		t.Errorf("Put with matching expected: %v", err)
	}
}
