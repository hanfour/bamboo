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

// TestPeers_SyncWGState covers the wgsync reporter's primary write
// path: pubkey-keyed snapshot update with GREATEST guard on the two
// timestamp columns and COALESCE guard on wg_endpoint. Verifies
//
//	(a) the update lands on the right row;
//	(b) rowsAffected is returned for the caller's drift detection;
//	(c) GREATEST keeps a fresher existing timestamp;
//	(d) COALESCE preserves a previously-observed endpoint when the
//	    next dump shows "(none)" (reporter passes empty string).
func TestPeers_SyncWGState(t *testing.T) {
	pool := requireDB(t)
	tenants := repo.NewTenants(pool)
	peers := repo.NewPeers(pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slug := fmt.Sprintf("syncwg-%s", uuid.NewString()[:8])
	tenant, err := tenants.GetOrCreate(ctx, slug, "sync-wg test", "100.64.0.0/24")
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

	freshTS := time.Now().UTC().Truncate(time.Second)
	n, err := peers.SyncWGState(ctx, repo.WGSyncState{
		PubKey:        pubKey,
		Status:        "online",
		LastHandshake: freshTS,
		Endpoint:      "203.0.113.7:51820",
		RxBytes:       12345,
		TxBytes:       67890,
	})
	if err != nil {
		t.Fatalf("SyncWGState match: %v", err)
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
	if got.LastHandshakeAt == nil || !got.LastHandshakeAt.Equal(freshTS) {
		t.Errorf("last_handshake_at = %v, want %v", got.LastHandshakeAt, freshTS)
	}
	if got.WGEndpoint == nil || *got.WGEndpoint != "203.0.113.7:51820" {
		t.Errorf("wg_endpoint = %v, want 203.0.113.7:51820", got.WGEndpoint)
	}
	if got.RxBytes != 12345 || got.TxBytes != 67890 {
		t.Errorf("bytes = rx=%d tx=%d, want rx=12345 tx=67890", got.RxBytes, got.TxBytes)
	}

	// GREATEST guard: an older handshake must not roll back, and an
	// empty endpoint must not erase the previously-observed value.
	older := freshTS.Add(-1 * time.Hour)
	if _, err := peers.SyncWGState(ctx, repo.WGSyncState{
		PubKey:        pubKey,
		Status:        "offline",
		LastHandshake: older,
		Endpoint:      "", // simulates wg dump "(none)"
		RxBytes:       99999,
		TxBytes:       11111,
	}); err != nil {
		t.Fatalf("SyncWGState older: %v", err)
	}
	got2, _ := peers.GetByID(ctx, inserted.ID)
	if got2.LastSeenAt == nil || !got2.LastSeenAt.Equal(freshTS) {
		t.Errorf("GREATEST failed: last_seen_at rolled back to %v (want %v)", got2.LastSeenAt, freshTS)
	}
	if got2.LastHandshakeAt == nil || !got2.LastHandshakeAt.Equal(freshTS) {
		t.Errorf("GREATEST failed: last_handshake_at rolled back to %v (want %v)", got2.LastHandshakeAt, freshTS)
	}
	if got2.WGEndpoint == nil || *got2.WGEndpoint != "203.0.113.7:51820" {
		t.Errorf("COALESCE failed: wg_endpoint = %v (want stay at 203.0.113.7:51820)", got2.WGEndpoint)
	}
	if got2.RxBytes != 99999 || got2.TxBytes != 11111 {
		t.Errorf("bytes should overwrite, got rx=%d tx=%d", got2.RxBytes, got2.TxBytes)
	}

	// Miss: unknown pubkey returns 0 rows, no error (used by the
	// reporter's drift-detection log).
	n, err = peers.SyncWGState(ctx, repo.WGSyncState{PubKey: randomB64(t), Status: "online", LastHandshake: freshTS})
	if err != nil {
		t.Fatalf("SyncWGState miss: %v", err)
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

// TestPeers_TagsRoundtrip covers the peer_tags wiring: a freshly
// inserted peer has zero tags, SetTags canonicalizes (trim, dedupe,
// sort), and every read path returns the same set.
func TestPeers_TagsRoundtrip(t *testing.T) {
	pool := requireDB(t)
	tenants := repo.NewTenants(pool)
	peers := repo.NewPeers(pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slug := fmt.Sprintf("tags-%s", uuid.NewString()[:8])
	tenant, err := tenants.GetOrCreate(ctx, slug, "tags test", "100.64.0.0/24")
	if err != nil {
		t.Fatalf("GetOrCreate tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	pub := randomB64(t)
	inserted, err := peers.Insert(ctx, &repo.Peer{
		TenantID:           tenant.ID,
		Hostname:           "p-tags",
		WireGuardPublicKey: pub,
		IP:                 "100.64.0.30",
		Status:             "online",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if len(inserted.Tags) != 0 {
		t.Errorf("fresh peer Tags = %v, want []", inserted.Tags)
	}

	// Canonicalization: whitespace + duplicates + out-of-order.
	got, err := peers.SetTags(ctx, inserted.ID, []string{"  db", "lan", "db", "", "  ", "lan", "api"})
	if err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	want := []string{"api", "db", "lan"}
	if !equalStrings(got, want) {
		t.Errorf("SetTags returned %v, want %v", got, want)
	}

	// All read paths see the canonical set.
	byID, _ := peers.GetByID(ctx, inserted.ID)
	if !equalStrings(byID.Tags, want) {
		t.Errorf("GetByID Tags = %v, want %v", byID.Tags, want)
	}
	list, _ := peers.ListByTenant(ctx, tenant.ID)
	if len(list) != 1 || !equalStrings(list[0].Tags, want) {
		t.Errorf("ListByTenant Tags = %v, want %v", list, want)
	}
	byPub, _ := peers.FindByPubKey(ctx, tenant.ID, pub)
	if !equalStrings(byPub.Tags, want) {
		t.Errorf("FindByPubKey Tags = %v, want %v", byPub.Tags, want)
	}

	// Replace with a different set.
	if _, err := peers.SetTags(ctx, inserted.ID, []string{"prod"}); err != nil {
		t.Fatalf("SetTags second: %v", err)
	}
	byID, _ = peers.GetByID(ctx, inserted.ID)
	if !equalStrings(byID.Tags, []string{"prod"}) {
		t.Errorf("after replace Tags = %v, want [prod]", byID.Tags)
	}

	// Empty input clears the set.
	if _, err := peers.SetTags(ctx, inserted.ID, nil); err != nil {
		t.Fatalf("SetTags nil: %v", err)
	}
	byID, _ = peers.GetByID(ctx, inserted.ID)
	if len(byID.Tags) != 0 {
		t.Errorf("after clear Tags = %v, want []", byID.Tags)
	}
}

// TestPeers_UpdateHostname covers happy path + no-op detection.
func TestPeers_UpdateHostname(t *testing.T) {
	pool := requireDB(t)
	tenants := repo.NewTenants(pool)
	peers := repo.NewPeers(pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slug := fmt.Sprintf("rename-%s", uuid.NewString()[:8])
	tenant, _ := tenants.GetOrCreate(ctx, slug, "rename test", "100.64.0.0/24")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	inserted, err := peers.Insert(ctx, &repo.Peer{
		TenantID:           tenant.ID,
		Hostname:           "before",
		WireGuardPublicKey: randomB64(t),
		IP:                 "100.64.0.31",
		Status:             "online",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	changed, err := peers.UpdateHostname(ctx, inserted.ID, "after")
	if err != nil || !changed {
		t.Fatalf("UpdateHostname: changed=%v err=%v", changed, err)
	}
	got, _ := peers.GetByID(ctx, inserted.ID)
	if got.Hostname != "after" {
		t.Errorf("Hostname = %q, want after", got.Hostname)
	}

	// No-op: same value returns false, no error.
	changed, err = peers.UpdateHostname(ctx, inserted.ID, "after")
	if err != nil || changed {
		t.Errorf("no-op UpdateHostname: changed=%v err=%v, want changed=false", changed, err)
	}
}

// TestPeers_Delete covers cascade (peer_tags rows go) and idempotency.
func TestPeers_Delete(t *testing.T) {
	pool := requireDB(t)
	tenants := repo.NewTenants(pool)
	peers := repo.NewPeers(pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slug := fmt.Sprintf("delete-%s", uuid.NewString()[:8])
	tenant, _ := tenants.GetOrCreate(ctx, slug, "delete test", "100.64.0.0/24")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	inserted, _ := peers.Insert(ctx, &repo.Peer{
		TenantID:           tenant.ID,
		Hostname:           "doomed",
		WireGuardPublicKey: randomB64(t),
		IP:                 "100.64.0.32",
		Status:             "online",
	})
	if _, err := peers.SetTags(ctx, inserted.ID, []string{"tag-a", "tag-b"}); err != nil {
		t.Fatalf("SetTags: %v", err)
	}

	n, err := peers.Delete(ctx, inserted.ID)
	if err != nil || n != 1 {
		t.Fatalf("Delete: n=%d err=%v, want n=1", n, err)
	}

	// FK cascade: no peer_tags rows survive.
	var tagCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM peer_tags WHERE peer_id = $1`, inserted.ID).Scan(&tagCount); err != nil {
		t.Fatalf("count peer_tags: %v", err)
	}
	if tagCount != 0 {
		t.Errorf("peer_tags rows after delete = %d, want 0 (cascade)", tagCount)
	}

	// Idempotent: re-delete returns 0 rows, no error.
	n, err = peers.Delete(ctx, inserted.ID)
	if err != nil || n != 0 {
		t.Errorf("re-delete: n=%d err=%v, want n=0", n, err)
	}
}

// TestPeers_SyncWGState_DisabledLock asserts that once an admin
// sets status='disabled', the wgsync reporter cannot override it on
// the next tick. Other wgsync fields still update so a disabled
// peer's forensic data stays fresh.
func TestPeers_SyncWGState_DisabledLock(t *testing.T) {
	pool := requireDB(t)
	tenants := repo.NewTenants(pool)
	peers := repo.NewPeers(pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slug := fmt.Sprintf("dislock-%s", uuid.NewString()[:8])
	tenant, _ := tenants.GetOrCreate(ctx, slug, "disabled-lock test", "100.64.0.0/24")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	pub := randomB64(t)
	inserted, _ := peers.Insert(ctx, &repo.Peer{
		TenantID:           tenant.ID,
		Hostname:           "p-dis",
		WireGuardPublicKey: pub,
		IP:                 "100.64.0.33",
		Status:             "disabled",
	})

	// Reporter ticks with a fresh handshake claiming online: status
	// must stay disabled, but byte counters should still update.
	if _, err := peers.SyncWGState(ctx, repo.WGSyncState{
		PubKey:        pub,
		Status:        "online",
		LastHandshake: time.Now().UTC(),
		Endpoint:      "203.0.113.99:51820",
		RxBytes:       777,
		TxBytes:       888,
	}); err != nil {
		t.Fatalf("SyncWGState: %v", err)
	}
	got, _ := peers.GetByID(ctx, inserted.ID)
	if got.Status != "disabled" {
		t.Errorf("status = %q, want disabled (reporter must not override admin disable)", got.Status)
	}
	if got.RxBytes != 777 || got.TxBytes != 888 {
		t.Errorf("forensic byte counters did not update: rx=%d tx=%d", got.RxBytes, got.TxBytes)
	}
}

// TestPeers_SetConnectionPath_TransitionSemantics pins the contract
// the #138 v2 timeline depends on: SetConnectionPath returns the
// previous path string and a pathChanged flag that only flips when
// the *path* string transitioned, NOT on latency-only updates.
//
// Without this guarantee a noisy RTT would generate a connection_event
// row on every heartbeat and swamp the timeline.
func TestPeers_SetConnectionPath_TransitionSemantics(t *testing.T) {
	pool := requireDB(t)
	tenants := repo.NewTenants(pool)
	peers := repo.NewPeers(pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slug := fmt.Sprintf("path-test-%s", uuid.NewString()[:8])
	tenant, err := tenants.GetOrCreate(ctx, slug, "Path Test", "100.64.0.0/24")
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	p, err := peers.Insert(ctx, &repo.Peer{
		TenantID:           tenant.ID,
		Hostname:           "path-peer",
		WireGuardPublicKey: randomB64(t),
		IP:                 "100.64.0.20",
		Status:             "online",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// First call: NULL → "direct" is a real transition. prev is empty.
	prev, changed, err := peers.SetConnectionPath(ctx, p.ID, "direct", 12)
	if err != nil {
		t.Fatalf("first SetConnectionPath: %v", err)
	}
	if prev != "" || !changed {
		t.Errorf("first SetConnectionPath: prev=%q changed=%v, want \"\" true", prev, changed)
	}

	// Same path, different latency: NOT a path transition.
	prev, changed, err = peers.SetConnectionPath(ctx, p.ID, "direct", 25)
	if err != nil {
		t.Fatalf("latency-only SetConnectionPath: %v", err)
	}
	if changed {
		t.Errorf("latency-only SetConnectionPath: pathChanged=true; want false")
	}
	// prev is still "direct" because the row was updated (latency
	// changed) so the CTE captured the pre-update value.
	if prev != "direct" {
		t.Errorf("latency-only prev=%q, want \"direct\"", prev)
	}

	// Same path, same latency: full no-op.
	prev, changed, err = peers.SetConnectionPath(ctx, p.ID, "direct", 25)
	if err != nil {
		t.Fatalf("no-op SetConnectionPath: %v", err)
	}
	if changed || prev != "" {
		t.Errorf("no-op SetConnectionPath: prev=%q changed=%v, want \"\" false", prev, changed)
	}

	// Real flip: direct → relay.
	prev, changed, err = peers.SetConnectionPath(ctx, p.ID, "relay", 0)
	if err != nil {
		t.Fatalf("direct→relay SetConnectionPath: %v", err)
	}
	if prev != "direct" || !changed {
		t.Errorf("direct→relay: prev=%q changed=%v, want \"direct\" true", prev, changed)
	}
}

func randomB64(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func TestPeers_IP6Roundtrip(t *testing.T) {
	pool := requireDB(t)
	tenants := repo.NewTenants(pool)
	peers := repo.NewPeers(pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slug := fmt.Sprintf("peer-ip6-%s", uuid.NewString()[:8])
	tenant, err := tenants.GetOrCreate(ctx, slug, "ip6 test", "100.64.0.0/24")
	if err != nil {
		t.Fatalf("GetOrCreate tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	pubkey := make([]byte, 32)
	_, _ = rand.Read(pubkey)
	in, err := peers.Insert(ctx, &repo.Peer{
		TenantID:           tenant.ID,
		Hostname:           "ip6host",
		WireGuardPublicKey: base64.StdEncoding.EncodeToString(pubkey),
		IP:                 "100.64.0.5",
		IP6:                "fdba:1100::6440:5",
		Status:             "online",
		ApprovalStatus:     "approved",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if in.IP6 != "fdba:1100::6440:5" {
		t.Errorf("Insert returned IP6 %q, want fdba:1100::6440:5", in.IP6)
	}

	got, err := peers.GetByID(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.IP6 != "fdba:1100::6440:5" {
		t.Errorf("GetByID IP6 = %q, want fdba:1100::6440:5", got.IP6)
	}
}

func TestPeers_NAT64EgressRoundtrip(t *testing.T) {
	pool := requireDB(t)
	tenants := repo.NewTenants(pool)
	peers := repo.NewPeers(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slug := fmt.Sprintf("egr-%s", uuid.NewString()[:8])
	tn, err := tenants.GetOrCreate(ctx, slug, "egress test", "100.64.0.0/24")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tn.ID) })

	pub := make([]byte, 32)
	_, _ = rand.Read(pub)
	in, err := peers.Insert(ctx, &repo.Peer{
		TenantID: tn.ID, Hostname: "egress",
		WireGuardPublicKey: base64.StdEncoding.EncodeToString(pub),
		IP:                 "100.64.0.5", IP6: "fdba:1100::6440:5",
		Status: "online", ApprovalStatus: "approved",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if in.NAT64EgressCapable || in.NAT64EgressApproved {
		t.Errorf("fresh peer egress flags = %v/%v, want false/false", in.NAT64EgressCapable, in.NAT64EgressApproved)
	}
	if err := peers.SetNAT64EgressCapable(ctx, in.ID, true); err != nil {
		t.Fatalf("SetNAT64EgressCapable: %v", err)
	}
	if err := peers.SetNAT64EgressApproved(ctx, in.ID, true); err != nil {
		t.Fatalf("SetNAT64EgressApproved: %v", err)
	}
	got, err := peers.GetByID(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.NAT64EgressCapable || !got.NAT64EgressApproved {
		t.Errorf("after set: %v/%v, want true/true", got.NAT64EgressCapable, got.NAT64EgressApproved)
	}
}

func TestPeers_SetNAT64EgressHealth(t *testing.T) {
	pool := requireDB(t)
	tenants := repo.NewTenants(pool)
	peers := repo.NewPeers(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slug := fmt.Sprintf("c3pr2-%s", uuid.NewString()[:8])
	tn, err := tenants.GetOrCreate(ctx, slug, "C3PR2", "100.64.0.0/24")
	if err != nil {
		t.Fatalf("GetOrCreate tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tn.ID) })

	pub := make([]byte, 32)
	_, _ = rand.Read(pub)
	p, err := peers.Insert(ctx, &repo.Peer{
		TenantID: tn.ID, Hostname: "egress",
		WireGuardPublicKey: base64.StdEncoding.EncodeToString(pub),
		IP:                 "100.64.0.5", Status: "online", ApprovalStatus: "approved",
	})
	if err != nil {
		t.Fatalf("Insert peer: %v", err)
	}
	peerID := p.ID

	// Reported healthy → status healthy, reason cleared.
	if err := peers.SetNAT64EgressHealth(ctx, peerID, true); err != nil {
		t.Fatalf("SetNAT64EgressHealth(true): %v", err)
	}
	got, err := peers.GetByID(ctx, peerID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NAT64EgressHealthStatus == nil || *got.NAT64EgressHealthStatus != "healthy" {
		t.Errorf("status = %v, want healthy", got.NAT64EgressHealthStatus)
	}
	if got.NAT64EgressHealthReason == nil || *got.NAT64EgressHealthReason != "" {
		t.Errorf("reason = %v, want empty", got.NAT64EgressHealthReason)
	}

	// Reported unhealthy → status unhealthy, reason "translator down".
	if err := peers.SetNAT64EgressHealth(ctx, peerID, false); err != nil {
		t.Fatalf("SetNAT64EgressHealth(false): %v", err)
	}
	got, _ = peers.GetByID(ctx, peerID)
	if got.NAT64EgressHealthStatus == nil || *got.NAT64EgressHealthStatus != "unhealthy" {
		t.Errorf("status = %v, want unhealthy", got.NAT64EgressHealthStatus)
	}
	if got.NAT64EgressHealthReason == nil || *got.NAT64EgressHealthReason != "translator down" {
		t.Errorf("reason = %v, want 'translator down'", got.NAT64EgressHealthReason)
	}
}
