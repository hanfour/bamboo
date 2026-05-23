// SPDX-License-Identifier: AGPL-3.0-or-later

package clickhouse_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/clickhouse"
)

func TestConnectionEvents_InsertBatchAndCount(t *testing.T) {
	c := requireCH(t)
	ctx := context.Background()
	events := clickhouse.NewConnectionEvents(c)
	tenantID := uuid.New()

	now := time.Now().UTC().Truncate(time.Second)
	batch := []*clickhouse.ConnectionEvent{
		{TenantID: tenantID, OccurredAt: now, EventType: "CONNECTION_ESTABLISHED", Path: "DIRECT_HOST", BytesSent: 1024},
		{TenantID: tenantID, OccurredAt: now.Add(time.Second), EventType: "CONNECTION_CLOSED", Path: "DIRECT_HOST", BytesReceived: 2048},
		{TenantID: tenantID, OccurredAt: now.Add(2 * time.Second), EventType: "RELAY_FALLBACK", Path: "RELAY"},
	}
	if err := events.InsertBatch(ctx, batch); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	count, err := events.CountByTenant(ctx, tenantID, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountByTenant: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

// TestConnectionEvents_ListByPeer covers the #138 v2 query path: the
// PeerDrawer fetches only the rows where this peer is the source
// (path is a per-peer property), newest-first, scoped to a recent
// time window. Other peers' rows must NOT leak in.
func TestConnectionEvents_ListByPeer(t *testing.T) {
	c := requireCH(t)
	ctx := context.Background()
	events := clickhouse.NewConnectionEvents(c)
	tenantID := uuid.New()
	peerA := uuid.New()
	peerB := uuid.New()

	now := time.Now().UTC().Truncate(time.Second)
	rows := []*clickhouse.ConnectionEvent{
		{TenantID: tenantID, OccurredAt: now.Add(-3 * time.Hour), SourcePeerID: peerA, EventType: "path_change", Path: "direct", Reason: ""},
		{TenantID: tenantID, OccurredAt: now.Add(-2 * time.Hour), SourcePeerID: peerA, EventType: "path_change", Path: "relay", Reason: "direct"},
		{TenantID: tenantID, OccurredAt: now.Add(-1 * time.Hour), SourcePeerID: peerA, EventType: "path_change", Path: "direct", Reason: "relay"},
		// Peer B's event — must not show up in peerA's timeline.
		{TenantID: tenantID, OccurredAt: now.Add(-90 * time.Minute), SourcePeerID: peerB, EventType: "path_change", Path: "relay", Reason: "direct"},
		// Older-than-window peerA event — must be filtered by since.
		{TenantID: tenantID, OccurredAt: now.Add(-48 * time.Hour), SourcePeerID: peerA, EventType: "path_change", Path: "unknown", Reason: ""},
	}
	if err := events.InsertBatch(ctx, rows); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	got, err := events.ListByPeer(ctx, tenantID, peerA, now.Add(-24*time.Hour), 50)
	if err != nil {
		t.Fatalf("ListByPeer: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3 (peerB filtered out, 48h-old row outside since window)", len(got))
	}
	// Newest-first ordering: paths should arrive direct, relay, direct.
	wantPaths := []string{"direct", "relay", "direct"}
	for i, e := range got {
		if e.Path != wantPaths[i] {
			t.Errorf("got[%d].Path = %q, want %q", i, e.Path, wantPaths[i])
		}
		if e.SourcePeerID != peerA {
			t.Errorf("got[%d].SourcePeerID = %v, want peerA", i, e.SourcePeerID)
		}
	}
}

func TestConnectionEvents_NilConnDegrades(t *testing.T) {
	events := clickhouse.NewConnectionEvents(nil)
	ctx := context.Background()

	if err := events.Insert(ctx, &clickhouse.ConnectionEvent{TenantID: uuid.New()}); err != nil {
		t.Errorf("nil-conn Insert err: %v", err)
	}
	if err := events.InsertBatch(ctx, []*clickhouse.ConnectionEvent{{TenantID: uuid.New()}}); err != nil {
		t.Errorf("nil-conn InsertBatch err: %v", err)
	}
	count, err := events.CountByTenant(ctx, uuid.New(), time.Now().Add(-time.Hour))
	if err != nil || count != 0 {
		t.Errorf("nil-conn CountByTenant = %d, %v; want 0, nil", count, err)
	}
	listed, err := events.ListByPeer(ctx, uuid.New(), uuid.New(), time.Now().Add(-time.Hour), 10)
	if err != nil || len(listed) != 0 {
		t.Errorf("nil-conn ListByPeer = %d rows, err=%v; want 0, nil", len(listed), err)
	}
}
