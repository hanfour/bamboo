// SPDX-License-Identifier: AGPL-3.0-or-later

// Package clickhouse holds the controller's ClickHouse connection and
// the columnar telemetry tables it writes.
//
// All writes are best-effort: a missing ClickHouse never fails the
// user-facing operation. Operators who do not need analytics can run
// the controller with no clickhouse.url and the writers degrade to
// no-ops with a warning.
package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Conn is a thin wrapper around the underlying driver. Nil is a valid
// value: every method on a nil receiver becomes a no-op.
type Conn struct {
	conn driver.Conn
}

// ErrNotConfigured is returned by Open when dsn is empty.
var ErrNotConfigured = errors.New("clickhouse not configured")

// Open dials ClickHouse and ensures the schema. Returns ErrNotConfigured
// if dsn is empty so callers can choose to soft-fail.
func Open(ctx context.Context, dsn string) (*Conn, error) {
	if dsn == "" {
		return nil, ErrNotConfigured
	}

	opts, err := chgo.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	opts.DialTimeout = 5 * time.Second
	opts.ConnMaxLifetime = time.Hour
	opts.MaxOpenConns = 10
	opts.MaxIdleConns = 5

	c, err := chgo.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("ping clickhouse: %w", err)
	}

	conn := &Conn{conn: c}
	if err := conn.applySchema(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return conn, nil
}

// Close releases the underlying connection. Nil-safe.
func (c *Conn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// driver returns the underlying driver.Conn. Nil-safe via callers.
func (c *Conn) driver() driver.Conn { return c.conn }

// applySchema creates the telemetry tables idempotently.
func (c *Conn) applySchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS evaluation_traces (
		    id              UUID,
		    tenant_id       UUID,
		    occurred_at     DateTime64(3),
		    source          String,
		    destination     String,
		    port            UInt16,
		    protocol        LowCardinality(String),
		    action          LowCardinality(String),
		    matched_rule_id String
		) ENGINE = MergeTree
		ORDER BY (tenant_id, occurred_at)
		TTL toDate(occurred_at) + INTERVAL 90 DAY`,

		// connection_events doubles as (a) the future
		// TelemetryService.ReportConnectionEvents sink for client-
		// reported byte/RTT samples, and (b) the #138 v2 path-change
		// timeline. The schema spans both so we don't end up with two
		// tables that need joining at read time.
		//
		// Per-column conventions:
		//   * destination_peer_id is set to uuid.Nil for events that
		//     are about the source peer as a whole (e.g. path_change)
		//     rather than a specific peer-to-peer link. ListByPeer
		//     filters on source_peer_id only, so the nil sentinel
		//     doesn't interfere with peer-scoped queries.
		//   * reason is the free-form human-readable column reserved
		//     for explanations like "TCP RST" or "denied by rule".
		//     Structured signals belong in their own columns —
		//     prev_path is what powers the path-change "newPath ←
		//     prevPath" rendering instead of stuffing it into reason.
		`CREATE TABLE IF NOT EXISTS connection_events (
		    id                   UUID,
		    tenant_id            UUID,
		    occurred_at          DateTime64(3),
		    source_peer_id       UUID,
		    destination_peer_id  UUID,
		    event_type           LowCardinality(String),
		    path                 LowCardinality(String),
		    prev_path            LowCardinality(String),
		    bytes_sent           UInt64,
		    bytes_received       UInt64,
		    rtt_ms               UInt32,
		    reason               String,
		    matched_rule_id      String
		) ENGINE = MergeTree
		ORDER BY (tenant_id, occurred_at)
		TTL toDate(occurred_at) + INTERVAL 90 DAY`,

		// Upgrade path for clusters that created connection_events
		// before the prev_path column existed. CREATE TABLE IF NOT
		// EXISTS above doesn't ALTER an existing schema, so this
		// explicit ADD COLUMN IF NOT EXISTS keeps the column in
		// sync on already-deployed clusters. Safe to no-op on fresh
		// installs (the CREATE above already includes the column).
		`ALTER TABLE connection_events
		   ADD COLUMN IF NOT EXISTS prev_path LowCardinality(String) AFTER path`,

		// anomaly_findings is the Tier-2 ML pipeline's output. The Go
		// controller never writes here; the bamboo-ai Python module
		// produces rows after each scheduled training pass.
		`CREATE TABLE IF NOT EXISTS anomaly_findings (
		    id              UUID,
		    tenant_id       UUID,
		    occurred_at     DateTime64(3),
		    generated_at    DateTime64(3),
		    score           Float32,
		    model_version   LowCardinality(String),
		    event_id        UUID,
		    event_summary   String
		) ENGINE = MergeTree
		ORDER BY (tenant_id, generated_at)
		TTL toDate(generated_at) + INTERVAL 30 DAY`,
	}
	for _, s := range stmts {
		if err := c.conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("apply clickhouse schema: %w", err)
		}
	}
	return nil
}

// IsConfigured reports whether c writes through to a real connection.
func (c *Conn) IsConfigured() bool { return c != nil && c.conn != nil }

// warnOnce keeps degraded-mode logs from drowning the rest.
type once struct{ done bool }

func (o *once) do(action string) {
	if !o.done {
		slog.Warn("clickhouse not configured; skipping write", "action", action)
		o.done = true
	}
}
