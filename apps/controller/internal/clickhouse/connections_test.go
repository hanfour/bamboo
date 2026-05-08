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
}
