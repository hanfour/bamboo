// SPDX-License-Identifier: AGPL-3.0-or-later

package repo_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

func TestAPITokens_InsertGetList(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	tenants := repo.NewTenants(pool)
	tokens := repo.NewAPITokens(pool)

	slug := fmt.Sprintf("tok-test-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "Tok", slug, "100.64.89.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	created, err := tokens.Insert(ctx, &repo.APIToken{
		TenantID:    tenant.ID,
		Name:        "github-actions",
		Description: "CI runner",
		SecretHash:  "bcrypt-hash-stand-in",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if created.ID == uuid.Nil {
		t.Error("Insert returned zero ID")
	}
	got, err := tokens.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "github-actions" || got.Description != "CI runner" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	listed, err := tokens.ListByTenant(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Errorf("ListByTenant returned %d rows, want one matching created.ID", len(listed))
	}
}

// TestAPITokens_IsActiveSemantics pins the authn middleware's
// contract: a revoked OR expired token must NOT authenticate.
// A live token + a not-yet-expired token MUST. Without this the
// middleware would honor a revoked token until its cache miss,
// or accept a token a microsecond past its expiry boundary.
func TestAPITokens_IsActiveSemantics(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		tok  repo.APIToken
		want bool
	}{
		{"live", repo.APIToken{}, true},
		{"revoked", repo.APIToken{RevokedAt: ptrTime(now.Add(-time.Hour))}, false},
		{"expired", repo.APIToken{ExpiresAt: ptrTime(now.Add(-time.Second))}, false},
		{"future-expiry", repo.APIToken{ExpiresAt: ptrTime(now.Add(time.Hour))}, true},
		{"expired-and-revoked", repo.APIToken{
			RevokedAt: ptrTime(now.Add(-time.Hour)),
			ExpiresAt: ptrTime(now.Add(-time.Second)),
		}, false},
	}
	for _, c := range cases {
		if got := c.tok.IsActive(now); got != c.want {
			t.Errorf("%s: IsActive=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestAPITokens_Revoke(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	tenants := repo.NewTenants(pool)
	tokens := repo.NewAPITokens(pool)

	slug := fmt.Sprintf("rev-test-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "Rev", slug, "100.64.88.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})
	created, err := tokens.Insert(ctx, &repo.APIToken{
		TenantID: tenant.ID, Name: "x", SecretHash: "h",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := tokens.Revoke(ctx, created.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got, _ := tokens.GetByID(ctx, created.ID)
	if got.RevokedAt == nil {
		t.Errorf("RevokedAt still nil after Revoke")
	}
	if got.IsActive(time.Now()) {
		t.Errorf("revoked token still reports IsActive=true")
	}

	// Re-revoke is idempotent at the controller; the repo's
	// COALESCE keeps the original revoked_at so the audit trail
	// preserves the first revocation moment.
	first := *got.RevokedAt
	if err := tokens.Revoke(ctx, created.ID); err != nil {
		t.Fatalf("second Revoke: %v", err)
	}
	got2, _ := tokens.GetByID(ctx, created.ID)
	if !got2.RevokedAt.Equal(first) {
		t.Errorf("second Revoke changed timestamp: %v → %v", first, *got2.RevokedAt)
	}

	// Revoke on a non-existent id surfaces ErrNotFound so the
	// REST layer can map to 404 without an extra round trip.
	if err := tokens.Revoke(ctx, uuid.New()); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("Revoke on missing id: want ErrNotFound, got %v", err)
	}
}

func TestAPITokens_MarkUsed(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	tenants := repo.NewTenants(pool)
	tokens := repo.NewAPITokens(pool)

	slug := fmt.Sprintf("used-test-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "Used", slug, "100.64.87.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})
	created, err := tokens.Insert(ctx, &repo.APIToken{
		TenantID: tenant.ID, Name: "u", SecretHash: "h",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	when := time.Now().UTC().Truncate(time.Second)
	if err := tokens.MarkUsed(ctx, created.ID, when); err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}
	got, _ := tokens.GetByID(ctx, created.ID)
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(when) {
		t.Errorf("LastUsedAt = %v, want %v", got.LastUsedAt, when)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
