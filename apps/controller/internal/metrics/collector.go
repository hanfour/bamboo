// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/prometheus/client_golang/prometheus"
)

// TenantCollector implements prometheus.Collector and emits per-
// tenant gauges on every scrape. Distinct from the always-on
// counters in Registry because tenant counts come from the
// authoritative DB rows — we can't track them with a Counter that
// increments on event flow alone (peer delete by an admin in
// another process would leave a stale counter).
//
// Scrape cost: three small aggregate queries against indexed columns.
// At realistic tenant scale (hundreds of tenants, thousands of peers)
// each query stays under 5ms on a warm cache; Prometheus's default
// scrape interval is 15-30s so the controller-side query rate is
// well under what a tenant-list page load already costs.
//
// Failure mode: a transient DB error skips the affected metric for
// that scrape with a warning log, but does NOT fail the scrape
// overall. Operators reading the metric would see a gap rather than
// a missing endpoint — and a continuous gap surfaces as its own
// alert ("scrape returned but values missing"), which is exactly
// the right signal that the DB is degraded.
type TenantCollector struct {
	pool *db.Pool

	peersCount       *prometheus.Desc
	policyRevision   *prometheus.Desc
	relaysCount      *prometheus.Desc
	preAuthKeysCount *prometheus.Desc

	// queryTimeout caps each scrape-time query. 2s is generous for
	// the aggregates we run; longer than that and we'd rather emit
	// a gap than tie up the scrape connection.
	queryTimeout time.Duration

	// scrapeMu serializes concurrent scrapes so a slow query
	// doesn't run twice in parallel against the same Pool. Prom's
	// HTTP handler defaults to one scrape at a time per scrape job,
	// but exporters that handle multiple jobs (or a misconfigured
	// double-scrape) could otherwise stack queries.
	scrapeMu sync.Mutex
}

// NewTenantCollector constructs a TenantCollector ready to register
// with a Registry. pool may be nil — in that case Collect is a no-op
// (zero series emitted). This keeps tests that don't wire up a DB
// from needing to special-case the collector.
func NewTenantCollector(pool *db.Pool) *TenantCollector {
	return &TenantCollector{
		pool:         pool,
		queryTimeout: 2 * time.Second,
		peersCount: prometheus.NewDesc(
			"bamboo_peers_count",
			"Number of registered peers per tenant, labelled by approval_status and reachability status.",
			[]string{"tenant", "approval_status", "status"},
			nil,
		),
		policyRevision: prometheus.NewDesc(
			"bamboo_policy_revision",
			"Current ACL policy revision per tenant. 0 ⇒ no policy authored yet (full-mesh fallback).",
			[]string{"tenant"},
			nil,
		),
		relaysCount: prometheus.NewDesc(
			"bamboo_relays_count",
			"Number of provisioned relay endpoints per tenant.",
			[]string{"tenant"},
			nil,
		),
		preAuthKeysCount: prometheus.NewDesc(
			"bamboo_preauth_keys_count",
			"Number of pre-auth keys per tenant, labelled by status (pending / used / revoked / expired).",
			[]string{"tenant", "status"},
			nil,
		),
	}
}

// Register wires this collector into the given Registry. Convenience
// wrapper so callers don't need to reach into the unexported reg
// field.
func (c *TenantCollector) Register(r *Registry) error {
	return r.reg.Register(c)
}

// Describe sends the metric descriptors to ch. Required by the
// prometheus.Collector interface.
func (c *TenantCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.peersCount
	ch <- c.policyRevision
	ch <- c.relaysCount
	ch <- c.preAuthKeysCount
}

// Collect runs the per-tenant queries and emits one metric sample
// per row. Called by the Prometheus HTTP handler on every scrape.
func (c *TenantCollector) Collect(ch chan<- prometheus.Metric) {
	if c.pool == nil {
		return
	}
	c.scrapeMu.Lock()
	defer c.scrapeMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), c.queryTimeout)
	defer cancel()

	c.collectPeers(ctx, ch)
	c.collectPolicyRevisions(ctx, ch)
	c.collectRelays(ctx, ch)
	c.collectPreAuthKeys(ctx, ch)
}

// collectPeers emits bamboo_peers_count{tenant, approval_status,
// status}. One series per (tenant, approval, status) combination
// that actually has rows. Combinations with zero peers don't emit
// a series — the canonical Prom semantic for "no events of this
// shape" is "no series", and emitting explicit zero rows would
// inflate the time-series count.
func (c *TenantCollector) collectPeers(ctx context.Context, ch chan<- prometheus.Metric) {
	rows, err := c.pool.Query(ctx, `
		SELECT t.slug, p.approval_status, p.status, count(*)
		  FROM peers p
		  JOIN tenants t ON t.id = p.tenant_id
		 GROUP BY t.slug, p.approval_status, p.status
	`)
	if err != nil {
		slog.Warn("metrics: peers count query", "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var slug, approval, status string
		var count int64
		if err := rows.Scan(&slug, &approval, &status, &count); err != nil {
			slog.Warn("metrics: peers count scan", "err", err)
			return
		}
		ch <- prometheus.MustNewConstMetric(
			c.peersCount, prometheus.GaugeValue, float64(count),
			slug, approval, status,
		)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("metrics: peers count rows", "err", err)
	}
}

// collectPolicyRevisions emits bamboo_policy_revision{tenant}.
// Tenants without an acl_policies row don't appear in the result
// — their effective revision is 0, but operators read "missing
// series" as "no policy authored" without needing the explicit zero.
func (c *TenantCollector) collectPolicyRevisions(ctx context.Context, ch chan<- prometheus.Metric) {
	rows, err := c.pool.Query(ctx, `
		SELECT t.slug, p.revision
		  FROM acl_policies p
		  JOIN tenants t ON t.id = p.tenant_id
	`)
	if err != nil {
		slog.Warn("metrics: policy revision query", "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		var revision int64
		if err := rows.Scan(&slug, &revision); err != nil {
			slog.Warn("metrics: policy revision scan", "err", err)
			return
		}
		ch <- prometheus.MustNewConstMetric(
			c.policyRevision, prometheus.GaugeValue, float64(revision), slug,
		)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("metrics: policy revision rows", "err", err)
	}
}

// collectRelays emits bamboo_relays_count{tenant}. Tenants without
// any provisioned relays don't appear — same "no series ⇒ zero"
// convention as the peer count.
func (c *TenantCollector) collectRelays(ctx context.Context, ch chan<- prometheus.Metric) {
	rows, err := c.pool.Query(ctx, `
		SELECT t.slug, count(*)
		  FROM relays r
		  JOIN tenants t ON t.id = r.tenant_id
		 GROUP BY t.slug
	`)
	if err != nil {
		slog.Warn("metrics: relays count query", "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		var count int64
		if err := rows.Scan(&slug, &count); err != nil {
			slog.Warn("metrics: relays count scan", "err", err)
			return
		}
		ch <- prometheus.MustNewConstMetric(
			c.relaysCount, prometheus.GaugeValue, float64(count), slug,
		)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("metrics: relays count rows", "err", err)
	}
}

// collectPreAuthKeys emits bamboo_preauth_keys_count{tenant,
// status}. status is derived in SQL because the keys table stores
// raw timestamps + flags rather than a status column — the same
// derivation the Web UI's PreAuthKeyTable uses, kept in lock-step
// so a metric anomaly is debuggable against the same UI string.
//
// Derivation order (matches apps/web/.../PreAuthKeyTable.tsx):
//
//	revoked_at set                              → revoked
//	expires_at set + past                       → expired
//	!reusable && use_count > 0                  → used (one-shot consumed)
//	reusable  && use_count > 0                  → reusable (active, has redeems)
//	else                                        → pending (never used yet)
func (c *TenantCollector) collectPreAuthKeys(ctx context.Context, ch chan<- prometheus.Metric) {
	rows, err := c.pool.Query(ctx, `
		SELECT t.slug,
		       CASE
		           WHEN k.revoked_at IS NOT NULL THEN 'revoked'
		           WHEN k.expires_at IS NOT NULL AND k.expires_at < now() THEN 'expired'
		           WHEN NOT k.reusable AND k.use_count > 0 THEN 'used'
		           WHEN k.reusable AND k.use_count > 0 THEN 'reusable'
		           ELSE 'pending'
		       END AS status,
		       count(*)
		  FROM pre_auth_keys k
		  JOIN tenants t ON t.id = k.tenant_id
		 GROUP BY t.slug, status
	`)
	if err != nil {
		slog.Warn("metrics: preauth keys query", "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var slug, status string
		var count int64
		if err := rows.Scan(&slug, &status, &count); err != nil {
			slog.Warn("metrics: preauth keys scan", "err", err)
			return
		}
		ch <- prometheus.MustNewConstMetric(
			c.preAuthKeysCount, prometheus.GaugeValue, float64(count),
			slug, status,
		)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("metrics: preauth keys rows", "err", err)
	}
}
