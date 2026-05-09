// SPDX-License-Identifier: AGPL-3.0-or-later

package repo_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// TestPeers_EndpointsRoundtrip verifies that endpoints set on Insert,
// updated via UpdateEndpoints, are durably persisted and returned by
// every read path the handler uses.
func TestPeers_EndpointsRoundtrip(t *testing.T) {
	pool := requireDB(t)
	tenants := repo.NewTenants(pool)
	peers := repo.NewPeers(pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slug := fmt.Sprintf("peer-eps-%s", uuid.NewString()[:8])
	tenant, err := tenants.GetOrCreate(ctx, slug, "endpoint test", "100.64.0.0/24")
	if err != nil {
		t.Fatalf("GetOrCreate tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	// Insert with no endpoints — should round-trip as empty (not nil).
	first, err := peers.Insert(ctx, &repo.Peer{
		TenantID:           tenant.ID,
		Hostname:           "p1",
		WireGuardPublicKey: randomB64(t),
		IP:                 "100.64.0.10",
		Status:             "online",
	})
	if err != nil {
		t.Fatalf("Insert without endpoints: %v", err)
	}
	if len(first.Endpoints) != 0 {
		t.Errorf("Endpoints after empty Insert = %v, want []", first.Endpoints)
	}

	// Insert another with explicit endpoints.
	second, err := peers.Insert(ctx, &repo.Peer{
		TenantID:           tenant.ID,
		Hostname:           "p2",
		WireGuardPublicKey: randomB64(t),
		IP:                 "100.64.0.11",
		Status:             "online",
		Endpoints:          []string{"203.0.113.5:51820", "10.0.0.5:51820"},
	})
	if err != nil {
		t.Fatalf("Insert with endpoints: %v", err)
	}
	if want := []string{"203.0.113.5:51820", "10.0.0.5:51820"}; !equalStrings(second.Endpoints, want) {
		t.Errorf("Endpoints after Insert = %v, want %v", second.Endpoints, want)
	}

	// UpdateEndpoints reports change=true for a real diff.
	changed, err := peers.UpdateEndpoints(ctx, first.ID, []string{"198.51.100.7:51820"})
	if err != nil {
		t.Fatalf("UpdateEndpoints first: %v", err)
	}
	if !changed {
		t.Error("UpdateEndpoints should report changed=true after a real diff")
	}

	// Calling again with the same value reports change=false.
	changed, err = peers.UpdateEndpoints(ctx, first.ID, []string{"198.51.100.7:51820"})
	if err != nil {
		t.Fatalf("UpdateEndpoints idempotent: %v", err)
	}
	if changed {
		t.Error("UpdateEndpoints should report changed=false on no-op")
	}

	// Read paths see the persisted value.
	got, err := peers.GetByID(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if want := []string{"198.51.100.7:51820"}; !equalStrings(got.Endpoints, want) {
		t.Errorf("GetByID Endpoints = %v, want %v", got.Endpoints, want)
	}

	listed, err := peers.ListByTenant(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	for _, p := range listed {
		if p.ID == first.ID && !equalStrings(p.Endpoints, []string{"198.51.100.7:51820"}) {
			t.Errorf("ListByTenant returned %v for %s; want [198.51.100.7:51820]", p.Endpoints, p.ID)
		}
	}

	// Empty / nil clears the list.
	if _, err := peers.UpdateEndpoints(ctx, first.ID, nil); err != nil {
		t.Fatalf("UpdateEndpoints nil: %v", err)
	}
	cleared, err := peers.GetByID(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetByID after clear: %v", err)
	}
	if len(cleared.Endpoints) != 0 {
		t.Errorf("Endpoints after clear = %v, want empty", cleared.Endpoints)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func randomB64(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf)
}
