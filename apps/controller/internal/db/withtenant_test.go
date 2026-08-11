// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestWithTenant_SetsTxLocalGUC proves the seam RLS relies on: WithTenant
// sets app.tenant_id for the duration of its transaction, and the setting is
// tx-local (does not leak onto the pooled connection afterwards). Skips
// without DATABASE_URL_TEST; needs no migrations (no tables involved).
func TestWithTenant_SetsTxLocalGUC(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB test in -short mode")
	}
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST not set; skipping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer pool.Close()

	tid := uuid.New()

	var seen string
	if err := WithTenant(ctx, pool, tid, func(q Querier) error {
		return q.QueryRow(ctx, `SELECT COALESCE(current_setting('app.tenant_id', true), '')`).Scan(&seen)
	}); err != nil {
		t.Fatalf("WithTenant: %v", err)
	}
	if seen != tid.String() {
		t.Errorf("inside tx: app.tenant_id = %q, want %q — WithTenant must set the tx-local GUC", seen, tid)
	}

	// Must NOT persist on the pooled connection after the tx ends.
	var leaked string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(current_setting('app.tenant_id', true), '')`).Scan(&leaked); err != nil {
		t.Fatalf("post-tx query: %v", err)
	}
	if leaked != "" {
		t.Errorf("after tx: app.tenant_id leaked = %q, want empty — set_config must be tx-local, not session-level", leaked)
	}
}
