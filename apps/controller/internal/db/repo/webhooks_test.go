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

func TestWebhooks_CreateThenList(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	tenants := repo.NewTenants(pool)
	hooks := repo.NewWebhooks(pool)

	slug := fmt.Sprintf("hook-test-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "Hook", slug, "100.64.96.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	created, err := hooks.Create(ctx, &repo.WebhookSubscription{
		TenantID:    tenant.ID,
		URL:         "https://example.com/hook",
		Secret:      "test-secret",
		EventTypes:  []string{"peer.", "policy.update"},
		Description: "test",
		Active:      true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == uuid.Nil {
		t.Error("Create returned zero ID")
	}
	if created.Secret != "test-secret" {
		t.Errorf("Secret round-trip = %q, want test-secret", created.Secret)
	}

	listed, err := hooks.ListByTenant(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Errorf("ListByTenant returned %d rows, want one matching created.ID", len(listed))
	}
}

// TestWebhooks_ActiveForEvent pins the matching grammar:
//
//   - empty event_types ⇒ match every action
//   - exact-string entry matches exactly that action
//   - "prefix." entry (terminated dot) matches any action that
//     starts with that prefix
//   - inactive subscriptions never match
//
// Without these guarantees the publisher can either fire to
// subscribers who didn't ask (data leak) or skip subscribers who
// did (silent drop).
func TestWebhooks_ActiveForEvent(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	tenants := repo.NewTenants(pool)
	hooks := repo.NewWebhooks(pool)

	slug := fmt.Sprintf("matcher-test-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "M", slug, "100.64.95.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	// Four subscriptions exercising each branch of the matcher:
	all, _ := hooks.Create(ctx, &repo.WebhookSubscription{
		TenantID: tenant.ID, URL: "https://x/all", Secret: "s",
		EventTypes: []string{}, Active: true,
	})
	exact, _ := hooks.Create(ctx, &repo.WebhookSubscription{
		TenantID: tenant.ID, URL: "https://x/exact", Secret: "s",
		EventTypes: []string{"peer.register"}, Active: true,
	})
	prefix, _ := hooks.Create(ctx, &repo.WebhookSubscription{
		TenantID: tenant.ID, URL: "https://x/prefix", Secret: "s",
		EventTypes: []string{"peer."}, Active: true,
	})
	inactive, _ := hooks.Create(ctx, &repo.WebhookSubscription{
		TenantID: tenant.ID, URL: "https://x/inactive", Secret: "s",
		EventTypes: []string{}, Active: false,
	})

	cases := []struct {
		action     string
		wantIDs    []uuid.UUID
		dontWantID uuid.UUID
	}{
		{"peer.register", []uuid.UUID{all.ID, exact.ID, prefix.ID}, inactive.ID},
		{"peer.routes.approve", []uuid.UUID{all.ID, prefix.ID}, inactive.ID},
		{"policy.update", []uuid.UUID{all.ID}, inactive.ID},
	}
	for _, c := range cases {
		got, err := hooks.ActiveForEvent(ctx, tenant.ID, c.action)
		if err != nil {
			t.Fatalf("ActiveForEvent %q: %v", c.action, err)
		}
		gotIDs := map[uuid.UUID]bool{}
		for _, g := range got {
			gotIDs[g.ID] = true
		}
		for _, want := range c.wantIDs {
			if !gotIDs[want] {
				t.Errorf("action=%q missing expected subscription %s; got %d rows", c.action, want, len(got))
			}
		}
		if gotIDs[c.dontWantID] {
			t.Errorf("action=%q included inactive subscription", c.action)
		}
	}
}

func TestWebhooks_UpdateAndDelete(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	tenants := repo.NewTenants(pool)
	hooks := repo.NewWebhooks(pool)

	slug := fmt.Sprintf("upd-test-%s", uuid.NewString()[:8])
	tenant, err := tenants.Create(ctx, "U", slug, "100.64.94.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	created, err := hooks.Create(ctx, &repo.WebhookSubscription{
		TenantID: tenant.ID, URL: "https://x/v1", Secret: "s",
		Active: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	newURL := "https://x/v2"
	inactive := false
	updated, err := hooks.Update(ctx, created.ID, repo.WebhookPatch{
		URL: &newURL, Active: &inactive,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.URL != newURL || updated.Active != false {
		t.Errorf("Update applied wrong values: %+v", updated)
	}
	// Secret must survive an update — rotating it via Update would
	// silently invalidate the receiver's HMAC verification.
	if updated.Secret != "s" {
		t.Errorf("Secret changed across Update: %q", updated.Secret)
	}

	if err := hooks.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := hooks.GetByID(ctx, created.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("GetByID after Delete: want ErrNotFound, got %v", err)
	}
}
