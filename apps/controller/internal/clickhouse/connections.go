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
type ConnectionEvent struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	OccurredAt        time.Time
	SourcePeerID      uuid.UUID
	DestinationPeerID uuid.UUID
	EventType         string
	Path              string
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
		    event_type, path, bytes_sent, bytes_received, rtt_ms,
		    reason, matched_rule_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		e.ID, e.TenantID, e.OccurredAt, e.SourcePeerID, e.DestinationPeerID,
		e.EventType, e.Path, e.BytesSent, e.BytesReceived, e.RTTMs,
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
		    event_type, path, bytes_sent, bytes_received, rtt_ms,
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
			e.EventType, e.Path, e.BytesSent, e.BytesReceived, e.RTTMs,
			e.Reason, e.MatchedRuleID,
		); err != nil {
			return fmt.Errorf("append: %w", err)
		}
	}
	return batch.Send()
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
