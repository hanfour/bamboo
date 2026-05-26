// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics_test

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/metrics"
)

// requireDB mirrors the helper the repo package uses — skip the
// test cleanly when DATABASE_URL_TEST isn't set so CI without a
// service container doesn't false-fail the metrics suite.
func requireDB(t *testing.T) *db.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestTenantCollector_PeersAndPolicyByTenant drives the full
// register-tenant + insert-peer + put-policy path and asserts the
// /metrics scrape carries the expected per-tenant gauges. This is
// the seam test between the collector's SQL and the actual schema
// — without it, a renamed column or a missing JOIN only surfaces
// at scrape time in production.
func TestTenantCollector_PeersAndPolicyByTenant(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	peers := repo.NewPeers(pool)
	policies := repo.NewPolicies(pool)

	slug := uniqueSlug(t, "tc")
	tenant, err := tenants.Create(ctx, "TC", slug, "100.64.42.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	// Three peers across two (approval_status, status) combinations
	// so we exercise the GROUP BY: one approved+online, one
	// approved+offline, one pending+online.
	for i, spec := range []struct {
		hostname, approval, status string
		ip                         string
	}{
		{"p1", "approved", "online", "100.64.42.10"},
		{"p2", "approved", "offline", "100.64.42.11"},
		{"p3", "pending", "online", "100.64.42.12"},
	} {
		_ = i
		p, err := peers.Insert(ctx, &repo.Peer{
			TenantID:           tenant.ID,
			Hostname:           spec.hostname,
			WireGuardPublicKey: uniqueB64(t),
			IP:                 spec.ip,
			Status:             spec.status,
			ApprovalStatus:     spec.approval,
		})
		if err != nil {
			t.Fatalf("insert peer %s: %v", spec.hostname, err)
		}
		_ = p
	}

	// Author a policy so bamboo_policy_revision emits a series for
	// this tenant.
	if _, err := policies.Put(ctx, tenant.ID,
		`rule "open" { action="allow" sources=["*"] destinations=["*:*"] }`, nil, 0); err != nil {
		t.Fatalf("put policy: %v", err)
	}

	r := metrics.NewRegistry("test", "test")
	if err := metrics.NewTenantCollector(pool).Register(r); err != nil {
		t.Fatalf("register collector: %v", err)
	}

	body := scrapeCollector(t, r)

	wantSeries := []string{
		`bamboo_peers_count{approval_status="approved",status="online",tenant="` + slug + `"} 1`,
		`bamboo_peers_count{approval_status="approved",status="offline",tenant="` + slug + `"} 1`,
		`bamboo_peers_count{approval_status="pending",status="online",tenant="` + slug + `"} 1`,
		`bamboo_policy_revision{tenant="` + slug + `"} 1`,
	}
	for _, want := range wantSeries {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q; body=\n%s", want, body)
		}
	}
}

// TestTenantCollector_NoTenantsNoSeries verifies the no-series-on-
// empty contract: a fresh tenant with zero peers / no policy /
// no relays / no keys must NOT emit a zero-valued gauge for every
// label combination. Prom convention: missing series == zero.
func TestTenantCollector_NoTenantsNoSeries(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	tenants := repo.NewTenants(pool)
	slug := uniqueSlug(t, "empty")
	tenant, err := tenants.Create(ctx, "Empty", slug, "100.64.43.0/24")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
	})

	r := metrics.NewRegistry("test", "test")
	if err := metrics.NewTenantCollector(pool).Register(r); err != nil {
		t.Fatalf("register collector: %v", err)
	}
	body := scrapeCollector(t, r)

	// No peers means no bamboo_peers_count line carries this slug.
	if strings.Contains(body, `tenant="`+slug+`"`) {
		t.Errorf("empty tenant should emit no series; body contained the slug:\n%s", body)
	}
}

// TestTenantCollector_NilPoolEmitsNothing pins the "pool may be nil"
// contract NewTenantCollector documents — handler-level callers
// holding an optional reference must not panic.
func TestTenantCollector_NilPoolEmitsNothing(t *testing.T) {
	r := metrics.NewRegistry("test", "test")
	if err := metrics.NewTenantCollector(nil).Register(r); err != nil {
		t.Fatalf("register: %v", err)
	}
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, name := range []string{
		"bamboo_peers_count",
		"bamboo_policy_revision",
		"bamboo_relays_count",
		"bamboo_preauth_keys_count",
	} {
		// HELP/TYPE lines for the metric should be present (Describe
		// runs), but no actual sample lines — those would need a
		// non-nil pool.
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, name+`{`) {
				t.Errorf("nil-pool collector emitted sample line: %s", line)
			}
		}
	}
}

// scrapeCollector exercises the Handler the same way Prometheus
// would and returns the text-exposition body for substring asserts.
func scrapeCollector(t *testing.T, r *metrics.Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("scrape: status=%d body=%s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func uniqueSlug(t *testing.T, prefix string) string {
	t.Helper()
	return prefix + "-" + uuid.NewString()[:8]
}

func uniqueB64(t *testing.T) string {
	t.Helper()
	// 32-byte stand-in — the metrics tests don't touch crypto, just
	// need a unique-per-row pubkey to satisfy the (tenant_id, pubkey)
	// uniqueness constraint.
	return strings.ReplaceAll(uuid.NewString(), "-", "") + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
}
