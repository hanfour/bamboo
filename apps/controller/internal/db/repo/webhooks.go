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

// WebhookSubscription is one row in webhook_subscriptions. The
// shape mirrors the migration; see 00012_webhook_subscriptions.sql
// for the field-level rationale.
//
// Secret is the HMAC key. It is stored in plaintext (symmetric —
// the receiver verifies sigs with the same value) and is returned
// from Create exactly once. The List + GetByID paths zero it out
// on the way to the wire so the secret never re-appears in REST
// responses after the initial mint, matching the pre-auth-key
// convention.
type WebhookSubscription struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	URL         string
	Secret      string
	EventTypes  []string
	Description string
	Active      bool
	CreatedBy   *uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Webhooks is the repository for webhook_subscriptions.
type Webhooks struct {
	pool *db.Pool
}

// NewWebhooks constructs the repository.
func NewWebhooks(pool *db.Pool) *Webhooks {
	return &Webhooks{pool: pool}
}

// Create inserts a new subscription with the supplied fields and
// returns the persisted row (including the server-assigned ID +
// timestamps). Caller generates the secret and passes it in —
// keeping secret generation outside the repo means the publisher
// package can choose its own RNG (typically crypto/rand) without
// pulling that dependency into the repo layer.
func (r *Webhooks) Create(ctx context.Context, sub *WebhookSubscription) (*WebhookSubscription, error) {
	if sub == nil {
		return nil, errors.New("webhooks.Create: nil subscription")
	}
	if sub.URL == "" || sub.Secret == "" {
		return nil, errors.New("webhooks.Create: url and secret are required")
	}
	if sub.EventTypes == nil {
		sub.EventTypes = []string{}
	}
	var out WebhookSubscription
	out.EventTypes = []string{}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO webhook_subscriptions
		    (tenant_id, url, secret, event_types, description, active, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, tenant_id, url, secret, event_types,
		          COALESCE(description, ''), active,
		          created_by, created_at, updated_at
	`,
		sub.TenantID, sub.URL, sub.Secret, sub.EventTypes,
		sub.Description, sub.Active, sub.CreatedBy,
	).Scan(
		&out.ID, &out.TenantID, &out.URL, &out.Secret, &out.EventTypes,
		&out.Description, &out.Active,
		&out.CreatedBy, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert webhook: %w", err)
	}
	return &out, nil
}

// ListByTenant returns every subscription for the tenant, newest
// first. Secret is left populated — the caller (REST list handler)
// is responsible for zeroing it before serialization.
func (r *Webhooks) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*WebhookSubscription, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, url, secret, event_types,
		       COALESCE(description, ''), active,
		       created_by, created_at, updated_at
		  FROM webhook_subscriptions
		 WHERE tenant_id = $1
		 ORDER BY created_at DESC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query webhooks: %w", err)
	}
	defer rows.Close()
	var out []*WebhookSubscription
	for rows.Next() {
		var w WebhookSubscription
		if err := rows.Scan(
			&w.ID, &w.TenantID, &w.URL, &w.Secret, &w.EventTypes,
			&w.Description, &w.Active,
			&w.CreatedBy, &w.CreatedAt, &w.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan webhook: %w", err)
		}
		out = append(out, &w)
	}
	return out, rows.Err()
}

// GetByID is the single-row read. Returns ErrNotFound when the
// id doesn't exist, so the REST layer can map to 404 without a
// special errors.Is in every caller.
func (r *Webhooks) GetByID(ctx context.Context, id uuid.UUID) (*WebhookSubscription, error) {
	var w WebhookSubscription
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, url, secret, event_types,
		       COALESCE(description, ''), active,
		       created_by, created_at, updated_at
		  FROM webhook_subscriptions
		 WHERE id = $1
	`, id).Scan(
		&w.ID, &w.TenantID, &w.URL, &w.Secret, &w.EventTypes,
		&w.Description, &w.Active,
		&w.CreatedBy, &w.CreatedAt, &w.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get webhook: %w", err)
	}
	return &w, nil
}

// WebhookPatch carries the partial-update fields Update applies.
// Each pointer is optional; nil = leave the corresponding column
// alone. Secret is deliberately NOT mutable here — rotating it
// would silently break the receiver's HMAC verification at exactly
// the moment the admin is trying to debug something else. To
// rotate, the admin deletes + re-creates.
type WebhookPatch struct {
	URL         *string
	EventTypes  *[]string
	Description *string
	Active      *bool
}

// Update applies the patch and returns the updated row, or
// ErrNotFound when id doesn't exist.
func (r *Webhooks) Update(ctx context.Context, id uuid.UUID, patch WebhookPatch) (*WebhookSubscription, error) {
	current, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if patch.URL != nil {
		current.URL = *patch.URL
	}
	if patch.EventTypes != nil {
		current.EventTypes = *patch.EventTypes
		if current.EventTypes == nil {
			current.EventTypes = []string{}
		}
	}
	if patch.Description != nil {
		current.Description = *patch.Description
	}
	if patch.Active != nil {
		current.Active = *patch.Active
	}
	var out WebhookSubscription
	err = r.pool.QueryRow(ctx, `
		UPDATE webhook_subscriptions
		   SET url         = $2,
		       event_types = $3,
		       description = $4,
		       active      = $5,
		       updated_at  = now()
		 WHERE id = $1
		RETURNING id, tenant_id, url, secret, event_types,
		          COALESCE(description, ''), active,
		          created_by, created_at, updated_at
	`,
		id, current.URL, current.EventTypes, current.Description, current.Active,
	).Scan(
		&out.ID, &out.TenantID, &out.URL, &out.Secret, &out.EventTypes,
		&out.Description, &out.Active,
		&out.CreatedBy, &out.CreatedAt, &out.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update webhook: %w", err)
	}
	return &out, nil
}

// Delete removes the subscription. Idempotent: deleting a row that
// no longer exists returns nil, mirroring how the REST layer's
// "DELETE is idempotent" semantics flow back to the caller.
func (r *Webhooks) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM webhook_subscriptions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	return nil
}

// ActiveForEvent returns every active subscription that should
// receive the given event (by Action string). The matching rule:
//
//   - event_types is empty (or NULL → impossible per schema) → subscribe
//     to every event in the tenant.
//
//   - event_types non-empty → deliver iff one of the entries is either:
//
//     1. exactly equal to action ("peer.register" matches "peer.register"); OR
//     2. a prefix terminated by a dot ("peer." matches "peer.register",
//     "peer.update", "peer.routes.approve", etc.).
//
// Prefix-with-dot matching lets a subscriber say "any peer event"
// without enumerating every current and future sub-action; doing
// the prefix match in SQL keeps the publisher's hot path one
// round trip per event rather than fetching all subscriptions and
// filtering in Go.
func (r *Webhooks) ActiveForEvent(ctx context.Context, tenantID uuid.UUID, action string) ([]*WebhookSubscription, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, url, secret, event_types,
		       COALESCE(description, ''), active,
		       created_by, created_at, updated_at
		  FROM webhook_subscriptions
		 WHERE tenant_id = $1
		   AND active = true
		   AND (
		         cardinality(event_types) = 0
		      OR $2 = ANY (event_types)
		      OR EXISTS (
		             SELECT 1 FROM unnest(event_types) AS et
		              WHERE et LIKE '%.'
		                AND $2 LIKE et || '%'
		         )
		   )
		 ORDER BY created_at ASC
	`, tenantID, action)
	if err != nil {
		return nil, fmt.Errorf("query active webhooks: %w", err)
	}
	defer rows.Close()
	var out []*WebhookSubscription
	for rows.Next() {
		var w WebhookSubscription
		if err := rows.Scan(
			&w.ID, &w.TenantID, &w.URL, &w.Secret, &w.EventTypes,
			&w.Description, &w.Active,
			&w.CreatedBy, &w.CreatedAt, &w.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan webhook: %w", err)
		}
		out = append(out, &w)
	}
	return out, rows.Err()
}
