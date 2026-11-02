// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/clickhouse"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

const (
	maxIngressEventsPerBatch = 1000
	maxIngressEventsBody     = 1 << 20
	// maxIngressEventAge drops stale rows; the ingress sink flushes every
	// few seconds and never retries, so anything older is not a real
	// recent request.
	maxIngressEventAge = 24 * time.Hour
	// maxIngressEventSkew tolerates clock drift between the VPS and the
	// controller.
	maxIngressEventSkew = 5 * time.Minute
)

// ingressEventJSON is the only accepted shape of one event. Unknown
// fields are rejected, so a client can neither choose tenant_id/peer_id
// nor send headers or bodies.
type ingressEventJSON struct {
	ServiceID  string    `json:"service_id"`
	ClientIP   string    `json:"client_ip"`
	Status     int       `json:"status"`
	BytesIn    int64     `json:"bytes_in"`
	BytesOut   int64     `json:"bytes_out"`
	DurationMs int64     `json:"duration_ms"`
	OccurredAt time.Time `json:"occurred_at"`
}

type ingressEventsReq struct {
	Events []ingressEventJSON `json:"events"`
}

// internalIngressEvents stores per-request metadata from bamboo-ingress
// in ClickHouse connection_events. A malformed batch (unknown field,
// wrong type, trailing data) is 400, an oversized one 413. Inside a
// well-formed batch, rows naming a service outside the token's tenant or
// carrying out-of-range values are dropped. Tenant and ingress peer come
// from the token only.
func (h *HTTPServer) internalIngressEvents(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticateIngress(w, r)
	if !ok {
		return
	}
	req, status, err := decodeIngressEvents(w, r)
	if err != nil {
		writeError(w, status, err)
		return
	}
	peerByService := map[uuid.UUID]uuid.UUID{}
	err = db.WithTenant(r.Context(), h.pool, claims.TenantID, func(q db.Querier) error {
		list, lerr := repo.NewIngressServices(q).ListByTenant(r.Context(), claims.TenantID)
		for _, s := range list {
			peerByService[s.ID] = s.PeerID
		}
		return lerr
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UTC()
	rows := make([]clickhouse.IngressRequestEvent, 0, len(req.Events))
	for _, e := range req.Events {
		if row, ok := ingressEventRow(e, claims, peerByService, now); ok {
			rows = append(rows, row)
		}
	}
	if err := h.connEvents.InsertIngressBatch(r.Context(), rows); err != nil {
		slog.Warn("ingress events insert", "tenant_id", claims.TenantID, "n", len(rows), "err", err)
		writeError(w, http.StatusBadGateway, errors.New("telemetry store unavailable"))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"accepted": len(rows)})
}

// decodeIngressEvents strictly decodes one JSON object of at most
// maxIngressEventsBody bytes and maxIngressEventsPerBatch events.
func decodeIngressEvents(w http.ResponseWriter, r *http.Request) (*ingressEventsReq, int, error) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxIngressEventsBody))
	dec.DisallowUnknownFields()
	var req ingressEventsReq
	err := dec.Decode(&req)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("trailing data")
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return nil, http.StatusRequestEntityTooLarge, errors.New("events body too large")
	}
	if err != nil {
		return nil, http.StatusBadRequest, errors.New("decode events")
	}
	if len(req.Events) > maxIngressEventsPerBatch {
		return nil, http.StatusRequestEntityTooLarge, errors.New("too many events in one batch")
	}
	return &req, 0, nil
}

// ingressEventRow validates one event and maps it onto a row scoped to
// the token's tenant and ingress peer. An unparseable client_ip is
// stored empty rather than dropping the request record.
func ingressEventRow(e ingressEventJSON, claims *auth.IngressClaims, peerByService map[uuid.UUID]uuid.UUID, now time.Time) (clickhouse.IngressRequestEvent, bool) {
	sid, err := uuid.Parse(e.ServiceID)
	if err != nil {
		return clickhouse.IngressRequestEvent{}, false
	}
	target, ok := peerByService[sid]
	if !ok || e.Status < 100 || e.Status > 599 || e.BytesIn < 0 || e.BytesOut < 0 || e.DurationMs < 0 {
		return clickhouse.IngressRequestEvent{}, false
	}
	at := e.OccurredAt.UTC()
	if e.OccurredAt.IsZero() {
		at = now
	}
	if at.Before(now.Add(-maxIngressEventAge)) || at.After(now.Add(maxIngressEventSkew)) {
		return clickhouse.IngressRequestEvent{}, false
	}
	return clickhouse.IngressRequestEvent{
		TenantID: claims.TenantID, IngressPeerID: claims.PeerID, TargetPeerID: target, ServiceID: sid,
		ClientIP: canonicalClientIP(e.ClientIP), Status: uint16(e.Status),
		BytesIn: uint64(e.BytesIn), BytesOut: uint64(e.BytesOut),
		DurationMs: uint32(min(e.DurationMs, math.MaxUint32)), OccurredAt: at,
	}, true
}

// canonicalClientIP returns the address in canonical form without a
// zone, IPv4-mapped IPv6 unmapped; "" when it does not parse.
func canonicalClientIP(s string) string {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return ""
	}
	return addr.WithZone("").Unmap().String()
}
