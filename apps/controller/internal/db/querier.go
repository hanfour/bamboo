// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the subset of pgx methods shared by *pgxpool.Pool and pgx.Tx.
// Repositories depend on it (rather than *Pool directly) so the same query
// code runs either straight against the pool or inside a tenant-scoped
// transaction opened by WithTenant. See ADR-0014.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	// Begin opens a transaction. On a *pgxpool.Pool it is a real tx on a
	// fresh connection; on a pgx.Tx (i.e. inside WithTenant) it is a
	// savepoint nested in the tenant tx — either way the repo's own
	// multi-statement atomicity is preserved.
	Begin(ctx context.Context) (pgx.Tx, error)
}

// WithTenant runs fn inside a transaction whose transaction-local
// `app.tenant_id` GUC is set to tenantID. Postgres RLS policies keyed on
// current_setting('app.tenant_id') then confine every statement fn runs to
// that tenant — the DB-level backstop for the application-layer tenant
// checks (ADR-0014).
//
// The GUC is set with set_config(..., is_local=true), so it is scoped to
// this transaction and is reset when the tx commits or rolls back; it never
// leaks onto the pooled connection for the next borrower.
func WithTenant(ctx context.Context, pool *Pool, tenantID uuid.UUID, fn func(Querier) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tenant tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once Commit succeeds

	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID.String()); err != nil {
		return fmt.Errorf("set app.tenant_id: %w", err)
	}

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
