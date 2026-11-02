// SPDX-License-Identifier: AGPL-3.0-or-later

package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// IngressRequestEventType marks one proxied public-services request.
// bamboo-ai excludes these rows from training (apps/ai clickhouse_io.py).
const IngressRequestEventType = "ingress_request"

// IngressRequestEvent is one request through bamboo-ingress. It carries
// metadata only: never request or response bodies, never headers.
//
// Mapping onto connection_events: event_type = "ingress_request",
// path = "ingress", source_peer_id = ingress peer, destination_peer_id =
// target peer, bytes_sent = bytes to the client (BytesOut),
// bytes_received = bytes from the client (BytesIn), plus the ingress-only
// columns service_id, client_ip, http_status, duration_ms.
type IngressRequestEvent struct {
	TenantID      uuid.UUID
	IngressPeerID uuid.UUID
	TargetPeerID  uuid.UUID
	ServiceID     uuid.UUID
	ClientIP      string
	Status        uint16
	BytesIn       uint64
	BytesOut      uint64
	DurationMs    uint32
	OccurredAt    time.Time
}

// InsertIngressBatch writes events in one round-trip. No-op when
// ClickHouse is not configured or the batch is empty.
func (r *ConnectionEvents) InsertIngressBatch(ctx context.Context, events []IngressRequestEvent) error {
	if !r.c.IsConfigured() {
		r.skip.Do(func() { r.logs.do("connection_events.insert_ingress") })
		return nil
	}
	if len(events) == 0 {
		return nil
	}
	batch, err := r.c.driver().PrepareBatch(ctx, `
		INSERT INTO connection_events (
		    id, tenant_id, occurred_at, source_peer_id, destination_peer_id,
		    event_type, path, bytes_sent, bytes_received,
		    service_id, client_ip, http_status, duration_ms
		)`)
	if err != nil {
		return fmt.Errorf("prepare ingress batch: %w", err)
	}
	now := time.Now().UTC()
	for _, e := range events {
		at := e.OccurredAt
		if at.IsZero() {
			at = now
		}
		if err := batch.Append(
			uuid.New(), e.TenantID, at, e.IngressPeerID, e.TargetPeerID,
			IngressRequestEventType, "ingress", e.BytesOut, e.BytesIn,
			e.ServiceID.String(), e.ClientIP, e.Status, e.DurationMs,
		); err != nil {
			return fmt.Errorf("append ingress event: %w", err)
		}
	}
	return batch.Send()
}
