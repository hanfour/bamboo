// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"io"
	"log/slog"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/clickhouse"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TelemetryHandler implements bamboov1.TelemetryServiceServer.
type TelemetryHandler struct {
	bamboov1.UnimplementedTelemetryServiceServer

	tenants *repo.Tenants
	events  *clickhouse.ConnectionEvents
}

// NewTelemetryHandler constructs a fresh TelemetryHandler. ch may be nil
// (degrades to writing nothing).
func NewTelemetryHandler(pool *db.Pool, ch *clickhouse.Conn) *TelemetryHandler {
	return &TelemetryHandler{
		tenants: repo.NewTenants(pool),
		events:  clickhouse.NewConnectionEvents(ch),
	}
}

// ReportConnectionEvents accepts a stream of events from a peer and
// flushes them into ClickHouse in a single batch when the stream
// closes. The batch keeps the round-trip cost flat regardless of how
// many events the peer queued up.
//
// Tenant resolution: the calling peer's tenant comes from the
// x-tenant-slug metadata header (Phase 1 simplification). A future
// follow-up will swap in JWT bearer-token resolution.
func (h *TelemetryHandler) ReportConnectionEvents(stream bamboov1.TelemetryService_ReportConnectionEventsServer) error {
	ctx := stream.Context()

	tenant, err := h.tenants.GetOrCreate(
		ctx,
		tenantSlugFromMetadata(ctx),
		"Default Tenant",
		"100.64.0.0/24",
	)
	if err != nil {
		return status.Errorf(codes.Internal, "tenant resolve: %v", err)
	}

	const flushAt = 256
	var batch []*clickhouse.ConnectionEvent
	var accepted int64

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := h.events.InsertBatch(ctx, batch); err != nil {
			slog.Warn("clickhouse batch flush failed", "size", len(batch), "err", err)
			return err
		}
		accepted += int64(len(batch))
		batch = batch[:0]
		return nil
	}

	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			if err := flush(); err != nil {
				return status.Errorf(codes.Internal, "flush: %v", err)
			}
			return stream.SendAndClose(&bamboov1.ReportConnectionEventsResponse{
				AcceptedCount: accepted,
			})
		}
		if err != nil {
			_ = flush()
			return err
		}
		event, err := protoConnectionEvent(msg, tenant.ID)
		if err != nil {
			slog.Warn("malformed connection event; dropped", "err", err)
			continue
		}
		batch = append(batch, event)
		if len(batch) >= flushAt {
			if err := flush(); err != nil {
				return status.Errorf(codes.Internal, "flush: %v", err)
			}
		}
	}
}

// ReportMetrics is unary: a peer pushes a single PeerMetrics snapshot.
// Phase 1 records nothing — metrics aggregation lands once the
// observability pipeline is wired. The handler exists so clients can
// call it without seeing Unimplemented.
func (h *TelemetryHandler) ReportMetrics(_ ctxAlias, _ *bamboov1.ReportMetricsRequest) (*bamboov1.ReportMetricsResponse, error) {
	return &bamboov1.ReportMetricsResponse{}, nil
}

// protoConnectionEvent converts a gRPC ConnectionEvent into the
// clickhouse-side struct. Validates that peer ids parse cleanly; the
// rest of the fields are stored as-is.
func protoConnectionEvent(msg *bamboov1.ConnectionEvent, tenantID uuid.UUID) (*clickhouse.ConnectionEvent, error) {
	srcID := uuid.Nil
	if s := msg.GetSourcePeerId(); s != "" {
		parsed, err := uuid.Parse(s)
		if err != nil {
			return nil, err
		}
		srcID = parsed
	}
	dstID := uuid.Nil
	if s := msg.GetDestinationPeerId(); s != "" {
		parsed, err := uuid.Parse(s)
		if err != nil {
			return nil, err
		}
		dstID = parsed
	}

	out := &clickhouse.ConnectionEvent{
		TenantID:          tenantID,
		SourcePeerID:      srcID,
		DestinationPeerID: dstID,
		EventType:         eventTypeName(msg.GetType()),
		Path:              connectionPathName(msg.GetPath()),
		BytesSent:         uint64(max64(msg.GetBytesSent(), 0)),
		BytesReceived:     uint64(max64(msg.GetBytesReceived(), 0)),
		RTTMs:             uint32(max32(msg.GetRttMs(), 0)),
		Reason:            msg.GetReason(),
		MatchedRuleID:     msg.GetMatchedRuleId(),
	}
	if id := msg.GetId(); id != "" {
		if parsed, err := uuid.Parse(id); err == nil {
			out.ID = parsed
		}
	}
	if t := msg.GetOccurredAt(); t != nil {
		out.OccurredAt = t.AsTime()
	}
	return out, nil
}

func eventTypeName(t bamboov1.ConnectionEventType) string {
	if t == bamboov1.ConnectionEventType_CONNECTION_EVENT_TYPE_UNSPECIFIED {
		return "UNSPECIFIED"
	}
	return t.String()
}

func connectionPathName(p bamboov1.ConnectionPath) string {
	if p == bamboov1.ConnectionPath_CONNECTION_PATH_UNSPECIFIED {
		return "UNSPECIFIED"
	}
	return p.String()
}

func max64(v, fallback int64) int64 {
	if v < 0 {
		return fallback
	}
	return v
}

func max32(v, fallback int32) int32 {
	if v < 0 {
		return fallback
	}
	return v
}
