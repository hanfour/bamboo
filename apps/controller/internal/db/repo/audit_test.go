// SPDX-License-Identifier: AGPL-3.0-or-later

package repo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

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
