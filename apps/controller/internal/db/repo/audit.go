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
}

// NewAuditLogs constructs an AuditLogs repository.
func NewAuditLogs(pool *db.Pool) *AuditLogs {
	return &AuditLogs{pool: pool}
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
}

// Insert persists a single event.
func (r *AuditLogs) Insert(ctx context.Context, e *AuditEvent) error {
	if e.ActorType == "" {
		e.ActorType = "system"
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO audit_log (
		    tenant_id, actor_id, actor_type, action,
		    resource_type, resource_id, diff, ip_address, user_agent
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::inet, $9)
	`,
		e.TenantID, e.ActorID, e.ActorType, e.Action,
		e.ResourceType, e.ResourceID, nullableJSON(e.Diff),
		nullableString(e.IPAddress), nullableString(e.UserAgent),
	)
	return err
}

// ListByTenant returns the most recent events for a tenant, newest first.
func (r *AuditLogs) ListByTenant(ctx context.Context, tenantID uuid.UUID, limit int) ([]*AuditEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, actor_id, actor_type, action,
		       resource_type, resource_id, diff, host(ip_address), user_agent, occurred_at
		FROM audit_log
		WHERE tenant_id = $1
		ORDER BY occurred_at DESC
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
