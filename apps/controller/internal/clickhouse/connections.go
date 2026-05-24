// SPDX-License-Identifier: AGPL-3.0-or-later

package clickhouse

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ConnectionEvent is one row in connection_events. The fields mirror
// bamboov1.ConnectionEvent but use plain Go types so the package does
// not depend on generated proto code.
//
// PrevPath is the structured "path before this row" signal that
// powers the #138 v2 path-change timeline — kept separate from
// Reason (free-form human-readable explanations like "TCP RST")
// so the two semantics don't collide on a single column.
type ConnectionEvent struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	OccurredAt        time.Time
	SourcePeerID      uuid.UUID
	DestinationPeerID uuid.UUID
	EventType         string
	Path              string
	PrevPath          string
	BytesSent         uint64
	BytesReceived     uint64
	RTTMs             uint32
	Reason            string
	MatchedRuleID     string
}

// ConnectionEvents is the writer / reader for connection_events.
type ConnectionEvents struct {
	c    *Conn
	skip sync.Once
	logs once
}

// NewConnectionEvents wraps a Conn (which may be nil) for the table.
func NewConnectionEvents(c *Conn) *ConnectionEvents {
	return &ConnectionEvents{c: c}
}

// Insert writes one event. Drops with a single warning when CH is
// not configured.
func (r *ConnectionEvents) Insert(ctx context.Context, e *ConnectionEvent) error {
	if !r.c.IsConfigured() {
		r.skip.Do(func() { r.logs.do("connection_events.insert") })
		return nil
	}
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	return r.c.driver().Exec(ctx, `
		INSERT INTO connection_events (
		    id, tenant_id, occurred_at, source_peer_id, destination_peer_id,
		    event_type, path, prev_path, bytes_sent, bytes_received, rtt_ms,
		    reason, matched_rule_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		e.ID, e.TenantID, e.OccurredAt, e.SourcePeerID, e.DestinationPeerID,
		e.EventType, e.Path, e.PrevPath, e.BytesSent, e.BytesReceived, e.RTTMs,
		e.Reason, e.MatchedRuleID,
	)
}

// InsertBatch streams a slice of events in one round-trip. Useful for
// the per-stream flush path in TelemetryService.ReportConnectionEvents.
func (r *ConnectionEvents) InsertBatch(ctx context.Context, events []*ConnectionEvent) error {
	if !r.c.IsConfigured() || len(events) == 0 {
		return nil
	}
	batch, err := r.c.driver().PrepareBatch(ctx, `
		INSERT INTO connection_events (
		    id, tenant_id, occurred_at, source_peer_id, destination_peer_id,
		    event_type, path, prev_path, bytes_sent, bytes_received, rtt_ms,
		    reason, matched_rule_id
		)`)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}

	now := time.Now().UTC()
	for _, e := range events {
		if e.ID == uuid.Nil {
			e.ID = uuid.New()
		}
		if e.OccurredAt.IsZero() {
			e.OccurredAt = now
		}
		if err := batch.Append(
			e.ID, e.TenantID, e.OccurredAt, e.SourcePeerID, e.DestinationPeerID,
			e.EventType, e.Path, e.PrevPath, e.BytesSent, e.BytesReceived, e.RTTMs,
			e.Reason, e.MatchedRuleID,
		); err != nil {
			return fmt.Errorf("append: %w", err)
		}
	}
	return batch.Send()
}

// ListByPeer returns the recent connection_events for a peer (as
// the source side), newest first. Powers the Web PeerDrawer
// connection-path timeline section (issue #138 v2).
//
// limit caps the response row count; callers should pass a small
// number (≤200). since restricts the time window so an idle
// drawer doesn't pull the full 90-day TTL window across the wire.
// CH is not configured ⇒ returns an empty slice without an error,
// matching the same graceful-degrade behaviour the writer uses.
func (r *ConnectionEvents) ListByPeer(
	ctx context.Context,
	tenantID uuid.UUID,
	peerID uuid.UUID,
	since time.Time,
	limit int,
) ([]*ConnectionEvent, error) {
	if !r.c.IsConfigured() {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.c.driver().Query(ctx, `
		SELECT id, tenant_id, occurred_at, source_peer_id, destination_peer_id,
		       event_type, path, prev_path, bytes_sent, bytes_received, rtt_ms,
		       reason, matched_rule_id
		  FROM connection_events
		 WHERE tenant_id = ? AND source_peer_id = ? AND occurred_at >= ?
		 ORDER BY occurred_at DESC
		 LIMIT ?
	`, tenantID, peerID, since, uint64(limit))
	if err != nil {
		return nil, fmt.Errorf("query connection_events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*ConnectionEvent
	for rows.Next() {
		var e ConnectionEvent
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.OccurredAt, &e.SourcePeerID, &e.DestinationPeerID,
			&e.EventType, &e.Path, &e.PrevPath, &e.BytesSent, &e.BytesReceived, &e.RTTMs,
			&e.Reason, &e.MatchedRuleID,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// BandwidthSample is one row from ListBandwidthByPeer — a thin
// projection of the connection_events columns the bandwidth API
// returns. EventType is always "bandwidth_sample" on these rows
// (filtered in the query) so the consumer doesn't have to
// re-check.
type BandwidthSample struct {
	OccurredAt    time.Time
	Path          string
	BytesSent     uint64
	BytesReceived uint64
}

// ListBandwidthByPeer returns bandwidth_sample rows for the
// given peer since `since`, oldest first. Oldest-first ordering
// lets the consumer compute deltas with a single forward pass
// (current.bytes - previous.bytes); newest-first would force a
// reverse iteration.
//
// limit caps the row count; callers should pass a small number
// (≤2000) — at the typical 30s heartbeat cadence that's >16
// hours of samples per peer, plenty for the bandwidth chart's
// rolling-window view. CH not configured ⇒ empty slice, same
// graceful-degrade pattern the writer uses.
func (r *ConnectionEvents) ListBandwidthByPeer(
	ctx context.Context,
	tenantID uuid.UUID,
	peerID uuid.UUID,
	since time.Time,
	limit int,
) ([]*BandwidthSample, error) {
	if !r.c.IsConfigured() {
		return nil, nil
	}
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	// bytes_sent ASC is a tiebreaker for samples that collapse to
	// the same DateTime64(3) millisecond. Cumulative wg counters
	// are monotonic, so within a single peer the smaller bytes_sent
	// IS the earlier sample. This matters because (a) fast CI
	// fixtures can fire multiple heartbeats inside one ms, and
	// (b) two clients sharing the same wall-clock ms at insert
	// time would otherwise order-arbitrarily.
	rows, err := r.c.driver().Query(ctx, `
		SELECT occurred_at, path, bytes_sent, bytes_received
		  FROM connection_events
		 WHERE tenant_id = ? AND source_peer_id = ?
		   AND event_type = 'bandwidth_sample'
		   AND occurred_at >= ?
		 ORDER BY occurred_at ASC, bytes_sent ASC
		 LIMIT ?
	`, tenantID, peerID, since, uint64(limit))
	if err != nil {
		return nil, fmt.Errorf("query bandwidth samples: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*BandwidthSample
	for rows.Next() {
		var s BandwidthSample
		if err := rows.Scan(&s.OccurredAt, &s.Path, &s.BytesSent, &s.BytesReceived); err != nil {
			return nil, fmt.Errorf("scan bandwidth sample: %w", err)
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// CountByTenant returns the count of events for a tenant since `since`.
// Used by tests + future overview widgets.
func (r *ConnectionEvents) CountByTenant(ctx context.Context, tenantID uuid.UUID, since time.Time) (uint64, error) {
	if !r.c.IsConfigured() {
		return 0, nil
	}
	var count uint64
	err := r.c.driver().QueryRow(ctx, `
		SELECT count() FROM connection_events
		WHERE tenant_id = ? AND occurred_at >= ?
	`, tenantID, since).Scan(&count)
	return count, err
}
