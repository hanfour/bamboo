// SPDX-License-Identifier: AGPL-3.0-or-later

package clickhouse_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/clickhouse"
)

// requireCH returns a connection or skips when CLICKHOUSE_URL_TEST is
// unset. CI does not currently spin up a CH service container; local
// developers point this at the docker-compose instance.
func requireCH(t *testing.T) *clickhouse.Conn {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping clickhouse test in -short mode")
	}
	dsn := os.Getenv("CLICKHOUSE_URL_TEST")
	if dsn == "" {
		t.Skip("CLICKHOUSE_URL_TEST not set; skipping clickhouse test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := clickhouse.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open clickhouse: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestTraces_InsertAndAggregate(t *testing.T) {
	c := requireCH(t)
	ctx := context.Background()
	traces := clickhouse.NewTraces(c)
	tenantID := uuid.New()

	now := time.Now().UTC().Truncate(time.Second)
	for i, ruleID := range []string{"rule-a", "rule-a", "rule-b", "rule-c"} {
		err := traces.Insert(ctx, &clickhouse.EvaluationTrace{
			TenantID:      tenantID,
			OccurredAt:    now.Add(time.Duration(i) * time.Second),
			Source:        "tag:dev",
			Destination:   "tag:staging",
			Port:          443,
			Action:        "allow",
			MatchedRuleID: ruleID,
		})
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	counts, err := traces.RuleHitCounts(ctx, tenantID, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("RuleHitCounts: %v", err)
	}
	if counts["rule-a"] != 2 {
		t.Errorf("rule-a hits = %d, want 2", counts["rule-a"])
	}
	if counts["rule-b"] != 1 || counts["rule-c"] != 1 {
		t.Errorf("counts = %v, want b=1 c=1", counts)
	}
}

func TestTraces_NilConnDegrades(t *testing.T) {
	traces := clickhouse.NewTraces(nil)
	ctx := context.Background()
	if err := traces.Insert(ctx, &clickhouse.EvaluationTrace{
		TenantID:      uuid.New(),
		MatchedRuleID: "x",
		Action:        "allow",
	}); err != nil {
		t.Errorf("nil-conn Insert returned err: %v", err)
	}
	counts, err := traces.RuleHitCounts(ctx, uuid.New(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Errorf("nil-conn RuleHitCounts err: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("nil-conn counts = %v, want empty", counts)
	}
}
