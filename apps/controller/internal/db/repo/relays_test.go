// SPDX-License-Identifier: AGPL-3.0-or-later

package repo_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// TestRelays_InsertListEnabled pins the baseline of the relay
// registry: a new relay's row is returned from Insert, then shows
// up in ListEnabled with health fields zero-valued (never probed).
func TestRelays_InsertListEnabled(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	relays := repo.NewRelays(pool)

	hostname := fmt.Sprintf("relay-%s.example.com", uuid.NewString()[:8])
	created, err := relays.Insert(ctx, &repo.RelayServer{
		Region: "ap-northeast-1", Hostname: hostname, Port: 8443,
		PublicKey: "stub-pubkey", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM relay_servers WHERE id = $1`, created.ID)
	})
	if created.LastHealthStatus != "" {
		t.Errorf("LastHealthStatus on fresh insert = %q, want empty (never probed)",
			created.LastHealthStatus)
	}
	if !created.LastHealthCheckAt.IsZero() {
		t.Errorf("LastHealthCheckAt on fresh insert non-zero: %v", created.LastHealthCheckAt)
	}

	listed, err := relays.ListEnabled(ctx)
	if err != nil {
		t.Fatalf("ListEnabled: %v", err)
	}
	var found bool
	for _, rs := range listed {
		if rs.ID == created.ID {
			found = true
			if rs.Hostname != hostname {
				t.Errorf("hostname = %q, want %q", rs.Hostname, hostname)
			}
		}
	}
	if !found {
		t.Errorf("inserted relay not in ListEnabled")
	}
}

// TestRelays_ListEligibleFiltersUnhealthy pins the §4 P2 stage 1
// invariant: ListEligible drops relays the reaper flagged
// 'unhealthy', INCLUDES rows with status 'unknown' or NULL (never
// probed), and INCLUDES 'healthy' rows. ListEnabled keeps showing
// the unhealthy row so admins can still operate on it.
func TestRelays_ListEligibleFiltersUnhealthy(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	relays := repo.NewRelays(pool)

	// Three relays: healthy, unhealthy, never-probed.
	healthy, _ := relays.Insert(ctx, &repo.RelayServer{
		Region: "us-east-1", Hostname: uniqueHostname(), Port: 8443,
		PublicKey: "stub", Enabled: true,
	})
	unhealthy, _ := relays.Insert(ctx, &repo.RelayServer{
		Region: "us-east-1", Hostname: uniqueHostname(), Port: 8443,
		PublicKey: "stub", Enabled: true,
	})
	neverProbed, _ := relays.Insert(ctx, &repo.RelayServer{
		Region: "us-east-1", Hostname: uniqueHostname(), Port: 8443,
		PublicKey: "stub", Enabled: true,
	})
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx,
			`DELETE FROM relay_servers WHERE id IN ($1, $2, $3)`,
			healthy.ID, unhealthy.ID, neverProbed.ID,
		)
	})

	if err := relays.UpdateHealth(ctx, healthy.ID, "healthy", ""); err != nil {
		t.Fatalf("UpdateHealth healthy: %v", err)
	}
	if err := relays.UpdateHealth(ctx, unhealthy.ID, "unhealthy", "timeout"); err != nil {
		t.Fatalf("UpdateHealth unhealthy: %v", err)
	}
	// Leave neverProbed alone.

	eligible, err := relays.ListEligible(ctx)
	if err != nil {
		t.Fatalf("ListEligible: %v", err)
	}
	ids := map[uuid.UUID]bool{}
	for _, rs := range eligible {
		ids[rs.ID] = true
	}
	if !ids[healthy.ID] {
		t.Errorf("eligible missing 'healthy' relay")
	}
	if !ids[neverProbed.ID] {
		t.Errorf("eligible missing never-probed relay (the include-by-default invariant)")
	}
	if ids[unhealthy.ID] {
		t.Errorf("eligible included 'unhealthy' relay — should be filtered out")
	}

	// ListEnabled (admin view) must STILL show the unhealthy row.
	enabled, _ := relays.ListEnabled(ctx)
	var sawUnhealthy bool
	for _, rs := range enabled {
		if rs.ID == unhealthy.ID {
			sawUnhealthy = true
			if rs.LastHealthStatus != "unhealthy" {
				t.Errorf("LastHealthStatus = %q, want 'unhealthy'", rs.LastHealthStatus)
			}
			if rs.LastHealthError != "timeout" {
				t.Errorf("LastHealthError = %q, want 'timeout'", rs.LastHealthError)
			}
			if rs.LastHealthCheckAt.IsZero() {
				t.Errorf("LastHealthCheckAt zero on probed row")
			}
		}
	}
	if !sawUnhealthy {
		t.Errorf("ListEnabled dropped the unhealthy relay — admins must still see it")
	}
}

// TestRelays_UpdateHealth_ClearsErrorOnRecovery pins the recovery
// path: a relay flagged unhealthy that later flips to healthy must
// have its last_health_error cleared, not stay polluted with the
// stale 'timeout' string from the previous probe.
func TestRelays_UpdateHealth_ClearsErrorOnRecovery(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	relays := repo.NewRelays(pool)

	rs, _ := relays.Insert(ctx, &repo.RelayServer{
		Region: "us-east-1", Hostname: uniqueHostname(), Port: 8443,
		PublicKey: "stub", Enabled: true,
	})
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM relay_servers WHERE id = $1`, rs.ID)
	})

	if err := relays.UpdateHealth(ctx, rs.ID, "unhealthy", "connection refused"); err != nil {
		t.Fatalf("UpdateHealth: %v", err)
	}
	if err := relays.UpdateHealth(ctx, rs.ID, "healthy", ""); err != nil {
		t.Fatalf("UpdateHealth recover: %v", err)
	}
	listed, _ := relays.ListEnabled(ctx)
	for _, r := range listed {
		if r.ID == rs.ID {
			if r.LastHealthStatus != "healthy" {
				t.Errorf("status = %q, want 'healthy'", r.LastHealthStatus)
			}
			if r.LastHealthError != "" {
				t.Errorf("LastHealthError = %q, want empty after recovery", r.LastHealthError)
			}
		}
	}
}

func uniqueHostname() string {
	return fmt.Sprintf("relay-%s.example.com", uuid.NewString()[:8])
}
