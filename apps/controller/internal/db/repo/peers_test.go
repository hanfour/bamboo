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

// TestPeers_SetStatusByPubKey covers the wgsync reporter's primary
// write path: pubkey-keyed status update with GREATEST guard on
// last_seen_at. Verifies (a) the update lands on the right row,
// (b) rowsAffected is returned for the caller's drift detection,
// (c) GREATEST keeps a fresher existing timestamp.
func TestPeers_SetStatusByPubKey(t *testing.T) {
	pool := requireDB(t)
	tenants := repo.NewTenants(pool)
	peers := repo.NewPeers(pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slug := fmt.Sprintf("setstat-%s", uuid.NewString()[:8])
	tenant, err := tenants.GetOrCreate(ctx, slug, "set-status test", "100.64.0.0/24")
	if err != nil {
		t.Fatalf("GetOrCreate tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	pubKey := randomB64(t)
	inserted, err := peers.Insert(ctx, &repo.Peer{
		TenantID:           tenant.ID,
		Hostname:           "p-set",
		WireGuardPublicKey: pubKey,
		IP:                 "100.64.0.20",
		Status:             "offline",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Match: should flip to online and update last_seen_at.
	freshTS := time.Now().UTC().Truncate(time.Second)
	n, err := peers.SetStatusByPubKey(ctx, pubKey, "online", freshTS)
	if err != nil {
		t.Fatalf("SetStatusByPubKey match: %v", err)
	}
	if n != 1 {
		t.Errorf("rowsAffected on match = %d, want 1", n)
	}
	got, err := peers.GetByID(ctx, inserted.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "online" {
		t.Errorf("status = %s, want online", got.Status)
	}
	if got.LastSeenAt == nil || !got.LastSeenAt.Equal(freshTS) {
		t.Errorf("last_seen_at = %v, want %v", got.LastSeenAt, freshTS)
	}

	// GREATEST guard: an older timestamp must not roll back.
	older := freshTS.Add(-1 * time.Hour)
	if _, err := peers.SetStatusByPubKey(ctx, pubKey, "offline", older); err != nil {
		t.Fatalf("SetStatusByPubKey older: %v", err)
	}
	got2, _ := peers.GetByID(ctx, inserted.ID)
	if got2.LastSeenAt == nil || !got2.LastSeenAt.Equal(freshTS) {
		t.Errorf("GREATEST failed: last_seen_at rolled back to %v (want stay at %v)", got2.LastSeenAt, freshTS)
	}

	// Miss: unknown pubkey returns 0 rows, no error (used by the
	// reporter's drift-detection log).
	n, err = peers.SetStatusByPubKey(ctx, randomB64(t), "online", freshTS)
	if err != nil {
		t.Fatalf("SetStatusByPubKey miss: %v", err)
	}
	if n != 0 {
		t.Errorf("rowsAffected on miss = %d, want 0", n)
	}
}

// TestPeers_MarkOfflineExcept covers the zombie-sweep semantics
// the wgsync reporter relies on: empty keep-list is a no-op (so a
// transient empty wg dump can't mass-flip everyone offline), and
// non-empty keep-list flips exactly the missing peers.
func TestPeers_MarkOfflineExcept(t *testing.T) {
	pool := requireDB(t)
	tenants := repo.NewTenants(pool)
	peers := repo.NewPeers(pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slug := fmt.Sprintf("mark-off-%s", uuid.NewString()[:8])
	tenant, err := tenants.GetOrCreate(ctx, slug, "mark-off test", "100.64.0.0/24")
	if err != nil {
		t.Fatalf("GetOrCreate tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	keepKey := randomB64(t)
	zombieKey := randomB64(t)
	keep, err := peers.Insert(ctx, &repo.Peer{
		TenantID: tenant.ID, Hostname: "keep", WireGuardPublicKey: keepKey,
		IP: "100.64.0.30", Status: "online",
	})
	if err != nil {
		t.Fatalf("Insert keep: %v", err)
	}
	zombie, err := peers.Insert(ctx, &repo.Peer{
		TenantID: tenant.ID, Hostname: "zombie", WireGuardPublicKey: zombieKey,
		IP: "100.64.0.31", Status: "online",
	})
	if err != nil {
		t.Fatalf("Insert zombie: %v", err)
	}

	// Empty keep-list is a no-op (defensive against transient empty dump).
	if err := peers.MarkOfflineExcept(ctx, nil); err != nil {
		t.Fatalf("MarkOfflineExcept empty: %v", err)
	}
	stillKeep, _ := peers.GetByID(ctx, keep.ID)
	stillZombie, _ := peers.GetByID(ctx, zombie.ID)
	if stillKeep.Status != "online" || stillZombie.Status != "online" {
		t.Errorf("empty keep-list flipped peers offline: keep=%s zombie=%s",
			stillKeep.Status, stillZombie.Status)
	}

	// Real keep-list: zombie flips, keep stays.
	if err := peers.MarkOfflineExcept(ctx, []string{keepKey}); err != nil {
		t.Fatalf("MarkOfflineExcept keep-only: %v", err)
	}
	gotKeep, _ := peers.GetByID(ctx, keep.ID)
	gotZombie, _ := peers.GetByID(ctx, zombie.ID)
	if gotKeep.Status != "online" {
		t.Errorf("keep peer flipped: %s", gotKeep.Status)
	}
	if gotZombie.Status != "offline" {
		t.Errorf("zombie peer not flipped: %s", gotZombie.Status)
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
