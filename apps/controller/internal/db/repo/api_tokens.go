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

// APIToken is one row in api_tokens (§4 P2). SecretHash is the
// bcrypt hash of the plaintext shown once at mint time; the
// plaintext itself is gone the moment the create response leaves
// the controller — same convention as pre-auth keys and webhook
// secrets.
type APIToken struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Name        string
	Description string
	SecretHash  string
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
	LastUsedAt  *time.Time
	CreatedBy   *uuid.UUID
	CreatedAt   time.Time
}

// IsActive reports whether the token may be honored by the authn
// middleware right now. False when revoked OR past its expires_at.
// The middleware uses this on every request to avoid honoring a
// token that just expired between scrape time and serve time.
func (t *APIToken) IsActive(now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	if t.ExpiresAt != nil && !t.ExpiresAt.After(now) {
		return false
	}
	return true
}

// APITokens is the repository for api_tokens.
type APITokens struct {
	pool *db.Pool
}

// NewAPITokens constructs the repository.
func NewAPITokens(pool *db.Pool) *APITokens {
	return &APITokens{pool: pool}
}

// Insert persists a fresh token. The caller (handlers / mint
// path) generates id + secret_hash and passes them in — keeping
// secret generation out of the repo means the auth package owns
// the format + bcrypt cost decision.
func (r *APITokens) Insert(ctx context.Context, t *APIToken) (*APIToken, error) {
	if t == nil {
		return nil, errors.New("api_tokens.Insert: nil token")
	}
	if t.Name == "" || t.SecretHash == "" {
		return nil, errors.New("api_tokens.Insert: name and secret_hash are required")
	}
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	var out APIToken
	err := r.pool.QueryRow(ctx, `
		INSERT INTO api_tokens
		    (id, tenant_id, name, description, secret_hash, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, tenant_id, name, COALESCE(description, ''),
		          secret_hash, expires_at, revoked_at, last_used_at,
		          created_by, created_at
	`,
		t.ID, t.TenantID, t.Name, t.Description, t.SecretHash, t.ExpiresAt, t.CreatedBy,
	).Scan(
		&out.ID, &out.TenantID, &out.Name, &out.Description,
		&out.SecretHash, &out.ExpiresAt, &out.RevokedAt, &out.LastUsedAt,
		&out.CreatedBy, &out.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert api_token: %w", err)
	}
	return &out, nil
}

// GetByID is the single-row read used by the authn middleware
// after it has parsed the embedded id from the bearer token.
// Returns ErrNotFound when the id doesn't exist.
func (r *APITokens) GetByID(ctx context.Context, id uuid.UUID) (*APIToken, error) {
	var t APIToken
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, name, COALESCE(description, ''),
		       secret_hash, expires_at, revoked_at, last_used_at,
		       created_by, created_at
		  FROM api_tokens
		 WHERE id = $1
	`, id).Scan(
		&t.ID, &t.TenantID, &t.Name, &t.Description,
		&t.SecretHash, &t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt,
		&t.CreatedBy, &t.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get api_token: %w", err)
	}
	return &t, nil
}

// ListByTenant returns every token row for the tenant, newest
// first. SecretHash stays populated; the handler is responsible
// for not echoing it on the wire.
func (r *APITokens) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*APIToken, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, name, COALESCE(description, ''),
		       secret_hash, expires_at, revoked_at, last_used_at,
		       created_by, created_at
		  FROM api_tokens
		 WHERE tenant_id = $1
		 ORDER BY created_at DESC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query api_tokens: %w", err)
	}
	defer rows.Close()
	var out []*APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(
			&t.ID, &t.TenantID, &t.Name, &t.Description,
			&t.SecretHash, &t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt,
			&t.CreatedBy, &t.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan api_token: %w", err)
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// Revoke flips revoked_at to now() for the row. Idempotent: a
// re-revoke of an already-revoked row is a no-op (RowsAffected=0
// is acceptable; the row stays revoked either way).
//
// Returns ErrNotFound when the id doesn't exist so the REST layer
// can map to 404 without an extra GetByID round trip.
func (r *APITokens) Revoke(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE api_tokens
		   SET revoked_at = COALESCE(revoked_at, now())
		 WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("revoke api_token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkUsed bumps last_used_at to the given time. Called by the
// authn middleware after a successful bcrypt verify. Best-effort:
// a failure here is logged but doesn't fail the request — the
// only consequence is a stale "last used" timestamp in the UI.
//
// Async-safe: the middleware fires this in a separate goroutine
// so the user-facing request doesn't wait on a write.
func (r *APITokens) MarkUsed(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE api_tokens
		   SET last_used_at = $2
		 WHERE id = $1
	`, id, at)
	if err != nil {
		return fmt.Errorf("mark api_token used: %w", err)
	}
	return nil
}
