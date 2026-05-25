// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
)

// AuditLogs is the append-only operation log.
//
// Inserts must never fail the user-facing operation: callers log + ignore
// errors here. Audit-loss is preferred over user-visible failure during
// Phase 1; we will revisit once a real SOC 2 review demands stricter
// semantics.
type AuditLogs struct {
	pool *db.Pool
	hook AuditHook
}

// AuditHook is fired by AuditLogs.Insert after a successful DB
// write. The webhook publisher (§4 P2) uses this to fan an event
// out to outbound HTTP subscriptions without the audit repo
// having to import the publisher package — the dependency points
// downward, server → publisher → repo, never back up.
//
// Hooks run synchronously on the caller's goroutine. A slow hook
// would become a latency floor on the user-facing operation that
// triggered the audit; implementations must do their work
// asynchronously (the webhook publisher uses an internal bounded
// channel + worker goroutine, and the hook itself returns within
// a few microseconds).
//
// Receiving an event with ev.TenantID == nil is legal — instance-
// wide audit rows happen for bootstrap operations. The hook is
// expected to filter appropriately.
type AuditHook interface {
	Hook(ctx context.Context, ev *AuditEvent)
}

// NewAuditLogs constructs an AuditLogs repository.
func NewAuditLogs(pool *db.Pool) *AuditLogs {
	return &AuditLogs{pool: pool}
}

// SetHook installs (or replaces) the AuditHook. Pass nil to
// disable. The hook fires after every successful Insert.
// Idempotent; safe to call before or after the server starts.
func (r *AuditLogs) SetHook(hook AuditHook) {
	if r == nil {
		return
	}
	r.hook = hook
}

// AuditEvent is a single row in audit_log.
type AuditEvent struct {
	ID           uuid.UUID
	TenantID     *uuid.UUID
	ActorID      *uuid.UUID
	ActorType    string // user | system | api
	Action       string // e.g. "peer.register", "policy.update"
	ResourceType string // e.g. "peer", "policy", "pre_auth_key"
	ResourceID   *uuid.UUID
	Diff         json.RawMessage
	IPAddress    *string
	UserAgent    *string
	OccurredAt   time.Time
	// ActorEmail is populated by ListByResource via a LEFT JOIN on
	// users when ActorType='user'. Other read paths leave it empty.
	// Empty string = unresolved (system actor or deleted user).
	ActorEmail string
}

// Insert persists a single event. After a successful DB write the
// installed AuditHook (if any) fires synchronously. Hook errors do
// not propagate — the audit write is the source of truth, and a
// hook failure (e.g. webhook publisher queue full) is recoverable
// from the audit row.
//
// e.OccurredAt is two-way: if the caller pre-sets a non-zero value
// (e.g. the erase handler pins the response timestamp), that exact
// value is persisted; otherwise the DB's `DEFAULT now()` applies.
// Either way the post-Insert struct carries the actually-stored
// timestamp via RETURNING — fixes the prior trap where readers of
// e.OccurredAt after Insert would see the Go zero value.
func (r *AuditLogs) Insert(ctx context.Context, e *AuditEvent) error {
	if e.ActorType == "" {
		e.ActorType = "system"
	}
	var occurredArg any
	if !e.OccurredAt.IsZero() {
		occurredArg = e.OccurredAt
	}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO audit_log (
		    tenant_id, actor_id, actor_type, action,
		    resource_type, resource_id, diff, ip_address, user_agent, occurred_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::inet, $9, COALESCE($10, now()))
		RETURNING occurred_at
	`,
		e.TenantID, e.ActorID, e.ActorType, e.Action,
		e.ResourceType, e.ResourceID, nullableJSON(e.Diff),
		nullableString(e.IPAddress), nullableString(e.UserAgent),
		occurredArg,
	).Scan(&e.OccurredAt)
	if err == nil && r.hook != nil {
		r.hook.Hook(ctx, e)
	}
	return err
}

// ListByTenant returns the most recent events for a tenant, newest
// first, joined against users so user-actor events surface an email.
// Powers the dashboard's tenant-wide activity feed; mirrors the
// JOIN shape used by ListByResource so callers get the same
// AuditEvent.ActorEmail contract.
func (r *AuditLogs) ListByTenant(ctx context.Context, tenantID uuid.UUID, limit int) ([]*AuditEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.tenant_id, a.actor_id, a.actor_type, a.action,
		       a.resource_type, a.resource_id, a.diff,
		       host(a.ip_address), a.user_agent, a.occurred_at,
		       COALESCE(u.email, '')
		FROM audit_log a
		LEFT JOIN users u
		       ON u.id = a.actor_id
		      AND a.actor_type = 'user'
		WHERE a.tenant_id = $1
		ORDER BY a.occurred_at DESC
		LIMIT $2
	`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*AuditEvent
	for rows.Next() {
		var e AuditEvent
		var ip *string
		var diff []byte
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.ActorID, &e.ActorType, &e.Action,
			&e.ResourceType, &e.ResourceID, &diff, &ip, &e.UserAgent, &e.OccurredAt,
			&e.ActorEmail,
		); err != nil {
			return nil, err
		}
		if len(diff) > 0 {
			e.Diff = diff
		}
		e.IPAddress = ip
		out = append(out, &e)
	}
	return out, rows.Err()
}

// ListByResource returns audit events for a specific (tenant, resource)
// pair, newest first. Used by the per-peer timeline in the Web UI.
// Joins users to resolve actor_id → email for user-driven events;
// system events leave ActorEmail empty.
//
// limit is clamped to [1, 200]: the timeline UI shows a bounded
// recent slice; deeper history will be a separate, paginated endpoint.
func (r *AuditLogs) ListByResource(ctx context.Context, tenantID uuid.UUID, resourceType string, resourceID uuid.UUID, limit int) ([]*AuditEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.tenant_id, a.actor_id, a.actor_type, a.action,
		       a.resource_type, a.resource_id, a.diff,
		       host(a.ip_address), a.user_agent, a.occurred_at,
		       COALESCE(u.email, '')
		FROM audit_log a
		LEFT JOIN users u
		       ON u.id = a.actor_id
		      AND a.actor_type = 'user'
		WHERE a.tenant_id = $1
		  AND a.resource_type = $2
		  AND a.resource_id = $3
		ORDER BY a.occurred_at DESC
		LIMIT $4
	`, tenantID, resourceType, resourceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*AuditEvent
	for rows.Next() {
		var e AuditEvent
		var ip *string
		var diff []byte
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.ActorID, &e.ActorType, &e.Action,
			&e.ResourceType, &e.ResourceID, &diff, &ip, &e.UserAgent, &e.OccurredAt,
			&e.ActorEmail,
		); err != nil {
			return nil, err
		}
		if len(diff) > 0 {
			e.Diff = diff
		}
		e.IPAddress = ip
		out = append(out, &e)
	}
	return out, rows.Err()
}

// DeleteOlderThan deletes every audit_log row whose occurred_at is
// strictly before cutoff and returns the number of rows removed.
// Powers the §4 P2 retention reaper that completes #182's audit
// log story — immutability landed there; this is the time-based
// purge counterpart.
//
// Bypasses the BEFORE DELETE trigger from migration 00013 by
// scoping `bamboo.allow_audit_delete = 'true'` to the local
// transaction with SET LOCAL. The trigger's default-deny path
// still catches every other caller (ad-hoc psql DELETE, an
// internal mistake from another repo, a stolen-credential
// scenario); SET LOCAL evaporates on COMMIT/ROLLBACK so the
// bypass can't leak past this function.
//
// Returns 0, nil when cutoff is the zero time — defensive against
// a misconfigured caller turning "delete everything ever" on by
// accident. A real retention window of "delete now and earlier"
// makes no sense and almost certainly indicates a bug.
func (r *AuditLogs) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	if cutoff.IsZero() {
		return 0, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// SET LOCAL: scoped to this transaction, auto-cleared on
	// commit/rollback. set_config(name, value, is_local) is the
	// function form — clearer than the SET LOCAL statement form
	// when the value is constant and the audit reader is
	// looking for "what bypasses the trigger".
	if _, err := tx.Exec(ctx, `SELECT set_config('bamboo.allow_audit_delete', 'true', true)`); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM audit_log WHERE occurred_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func nullableJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

func nullableString(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}
