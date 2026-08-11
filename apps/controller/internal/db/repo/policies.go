// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/jackc/pgx/v5"
)

// Policies is the repository for the per-tenant ACL policy and its
// append-only history.
type Policies struct {
	pool db.Querier
}

// NewPolicies constructs a Policies repository.
func NewPolicies(pool db.Querier) *Policies {
	return &Policies{pool: pool}
}

// PolicyRecord is the persisted form of a policy.
type PolicyRecord struct {
	TenantID  uuid.UUID
	Revision  int64
	HCLSource string
	UpdatedAt time.Time
	UpdatedBy *uuid.UUID
}

// HistoryRecord is a single row from acl_policy_history. Distinct
// from PolicyRecord because the history table tracks who applied a
// revision (applied_by) rather than who currently holds it. The Web
// UI lists these for the rollback + diff features in issue #134.
type HistoryRecord struct {
	TenantID       uuid.UUID
	Revision       int64
	HCLSource      string
	AppliedAt      time.Time
	AppliedBy      *uuid.UUID
	AppliedByEmail string // populated by LEFT JOIN; empty when applied_by is NULL or the user has been deleted.
}

// ErrRevisionMismatch is returned by Put when expectedRevision is
// non-zero and does not match the current revision (optimistic
// concurrency control).
var ErrRevisionMismatch = errors.New("policy revision mismatch")

// Get returns the current policy for a tenant. Returns ErrNotFound when
// no policy has been written yet.
func (r *Policies) Get(ctx context.Context, tenantID uuid.UUID) (*PolicyRecord, error) {
	var rec PolicyRecord
	err := r.pool.QueryRow(ctx, `
		SELECT tenant_id, revision, hcl_source, updated_at, updated_by
		FROM acl_policies
		WHERE tenant_id = $1
	`, tenantID).Scan(
		&rec.TenantID, &rec.Revision, &rec.HCLSource, &rec.UpdatedAt, &rec.UpdatedBy,
	)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &rec, nil
}

// Put writes a new policy revision.
//
// expectedRevision is optimistic concurrency control: if non-zero, Put
// returns ErrRevisionMismatch unless it matches the current persisted
// revision. Pass 0 to disable the check (used for first-write).
//
// The current row in acl_policies is upserted with revision incremented
// by one; the previous revision is appended to acl_policy_history. Both
// writes happen in the same transaction.
func (r *Policies) Put(
	ctx context.Context,
	tenantID uuid.UUID,
	hclSource string,
	updatedBy *uuid.UUID,
	expectedRevision int64,
) (*PolicyRecord, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentRev int64
	err = tx.QueryRow(ctx, `
		SELECT revision FROM acl_policies WHERE tenant_id = $1 FOR UPDATE
	`, tenantID).Scan(&currentRev)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		currentRev = 0
	case err != nil:
		return nil, fmt.Errorf("read current revision: %w", err)
	}

	if expectedRevision != 0 && expectedRevision != currentRev {
		return nil, ErrRevisionMismatch
	}

	nextRev := currentRev + 1
	var rec PolicyRecord
	err = tx.QueryRow(ctx, `
		INSERT INTO acl_policies (tenant_id, revision, hcl_source, updated_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id) DO UPDATE SET
		    revision   = EXCLUDED.revision,
		    hcl_source = EXCLUDED.hcl_source,
		    updated_by = EXCLUDED.updated_by,
		    updated_at = now()
		RETURNING tenant_id, revision, hcl_source, updated_at, updated_by
	`, tenantID, nextRev, hclSource, updatedBy).Scan(
		&rec.TenantID, &rec.Revision, &rec.HCLSource, &rec.UpdatedAt, &rec.UpdatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert policy: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO acl_policy_history (tenant_id, revision, hcl_source, applied_by)
		VALUES ($1, $2, $3, $4)
	`, tenantID, nextRev, hclSource, updatedBy); err != nil {
		return nil, fmt.Errorf("append history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &rec, nil
}

// Bump increments the tenant's policy revision in place without
// authoring a new HCL document. Returns the new revision.
//
// Used by admin actions that change the effective per-peer allowed_ips
// without touching the ACL itself — e.g. approving subnet routes
// (#136) or flipping the exit-node bit (#137). Bumping the revision is
// what makes Heartbeat return PolicyChanged=true on the next tick so
// peers re-pull their config (the missing piece behind issue #170).
//
// Does NOT append to acl_policy_history: the Versions tab tracks ACL
// authoring events, and a phantom "no-op" rollback target there would
// be misleading. The route/exit-node approval is already recorded in
// the audit log by the caller.
//
// First-call behaviour: when no acl_policies row exists for the tenant
// (no ACL ever authored), inserts revision=1 with empty hcl_source.
// Empty HCL still parses to an empty Policy, and policy.Allow's
// zero-rules path returns true — i.e. full-mesh stays the default,
// matching the pre-bump semantics. The client-side enforcing branch
// (revision > 0 + empty allowed_ips → drop peer) is also safe because
// allowedIPsFor always emits at least the peer's tunnel /32.
func (r *Policies) Bump(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	var newRev int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO acl_policies (tenant_id, revision, hcl_source, updated_by)
		VALUES ($1, 1, '', NULL)
		ON CONFLICT (tenant_id) DO UPDATE SET
		    revision   = acl_policies.revision + 1,
		    updated_at = now()
		RETURNING revision
	`, tenantID).Scan(&newRev)
	if err != nil {
		return 0, fmt.Errorf("bump policy revision: %w", err)
	}
	return newRev, nil
}

// ListHistory returns the most recent N revisions for a tenant,
// newest first. Used by the Web UI's "Versions" tab (issue #134) to
// render the rollback list with timestamps and the admin email who
// applied each revision. limit clamps to a small constant — the
// table grows append-only and we don't want to ship an unbounded
// payload over the wire for a tenant with churn.
//
// LEFT JOINs users so the row carries the applier's email inline.
// Deleted users (users.deleted_at IS NOT NULL) yield an empty string
// rather than orphan-rendering the row; the audit semantics survive
// the user deletion even though the friendly email doesn't.
func (r *Policies) ListHistory(ctx context.Context, tenantID uuid.UUID, limit int) ([]*HistoryRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT h.tenant_id, h.revision, h.hcl_source, h.applied_at, h.applied_by,
		       COALESCE(u.email, '')
		FROM acl_policy_history h
		LEFT JOIN users u ON u.id = h.applied_by AND u.deleted_at IS NULL
		WHERE h.tenant_id = $1
		ORDER BY h.revision DESC
		LIMIT $2
	`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*HistoryRecord
	for rows.Next() {
		var h HistoryRecord
		if err := rows.Scan(
			&h.TenantID, &h.Revision, &h.HCLSource, &h.AppliedAt, &h.AppliedBy,
			&h.AppliedByEmail,
		); err != nil {
			return nil, err
		}
		out = append(out, &h)
	}
	return out, rows.Err()
}
