// SPDX-License-Identifier: AGPL-3.0-or-later

package clickhouse_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/clickhouse"
)

func TestConnectionEvents_InsertIngressBatch(t *testing.T) {
	c := requireCH(t)
	ctx := context.Background()
	events := clickhouse.NewConnectionEvents(c)
	tenantID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	batch := []clickhouse.IngressRequestEvent{
		{TenantID: tenantID, IngressPeerID: uuid.New(), TargetPeerID: uuid.New(), ServiceID: uuid.New(),
			ClientIP: "203.0.113.7", Status: 200, BytesIn: 120, BytesOut: 4096, DurationMs: 35, OccurredAt: now},
		{TenantID: tenantID, IngressPeerID: uuid.New(), TargetPeerID: uuid.New(), ServiceID: uuid.New(),
			ClientIP: "203.0.113.8", Status: 429, OccurredAt: now.Add(time.Second)},
	}
	if err := events.InsertIngressBatch(ctx, batch); err != nil {
		t.Fatalf("InsertIngressBatch: %v", err)
	}
	n, err := events.CountByTenant(ctx, tenantID, now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("CountByTenant: %v", err)
	}
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
}

func TestConnectionEvents_InsertIngressBatchNilConn(t *testing.T) {
	events := clickhouse.NewConnectionEvents(nil)
	if err := events.InsertIngressBatch(context.Background(), []clickhouse.IngressRequestEvent{{TenantID: uuid.New()}}); err != nil {
		t.Errorf("nil conn must degrade silently, got %v", err)
	}
}
