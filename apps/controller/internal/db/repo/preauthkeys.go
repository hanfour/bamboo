// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
)

// PreAuthKeys is the repository for pre_auth_keys table.
type PreAuthKeys struct {
	pool *db.Pool
}

// NewPreAuthKeys constructs a PreAuthKeys repository.
func NewPreAuthKeys(pool *db.Pool) *PreAuthKeys {
	return &PreAuthKeys{pool: pool}
}

// PreAuthKey is the domain model.
type PreAuthKey struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Description string
	SecretHash  string
	Tags        []string
	Reusable    bool
	Ephemeral   bool
	CreatedBy   *uuid.UUID
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
	UseCount    int64
}

// Create inserts a new key. The caller is responsible for hashing the
// secret before calling.
func (r *PreAuthKeys) Create(ctx context.Context, k *PreAuthKey) (*PreAuthKey, error) {
	// Normalize: pgx maps a nil Go slice to SQL NULL, but the column is
	// NOT NULL with default '{}'. Replace nil with an empty slice.
	tags := k.Tags
	if tags == nil {
		tags = []string{}
	}

	var out PreAuthKey
	err := r.pool.QueryRow(ctx, `
		INSERT INTO pre_auth_keys (
		    id, tenant_id, description, secret_hash, tags, reusable, ephemeral,
		    created_by, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, tenant_id, description, secret_hash, tags, reusable, ephemeral,
		          created_by, created_at, expires_at, revoked_at, use_count
	`, k.ID, k.TenantID, k.Description, k.SecretHash, tags, k.Reusable, k.Ephemeral,
		k.CreatedBy, k.ExpiresAt).Scan(
		&out.ID, &out.TenantID, &out.Description, &out.SecretHash, &out.Tags,
		&out.Reusable, &out.Ephemeral,
		&out.CreatedBy, &out.CreatedAt, &out.ExpiresAt, &out.RevokedAt, &out.UseCount,
	)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetByID returns a key by primary key (used by the redemption flow after
// parsing the prefix).
func (r *PreAuthKeys) GetByID(ctx context.Context, id uuid.UUID) (*PreAuthKey, error) {
	var k PreAuthKey
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, description, secret_hash, tags, reusable, ephemeral,
		       created_by, created_at, expires_at, revoked_at, use_count
		FROM pre_auth_keys
		WHERE id = $1
	`, id).Scan(
		&k.ID, &k.TenantID, &k.Description, &k.SecretHash, &k.Tags,
		&k.Reusable, &k.Ephemeral,
		&k.CreatedBy, &k.CreatedAt, &k.ExpiresAt, &k.RevokedAt, &k.UseCount,
	)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &k, nil
}

// MarkRedeemed atomically increments use_count and, for non-reusable keys,
// records the revocation timestamp so the key cannot be reused.
func (r *PreAuthKeys) MarkRedeemed(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE pre_auth_keys
		   SET use_count  = use_count + 1,
		       revoked_at = CASE WHEN reusable THEN revoked_at ELSE now() END
		 WHERE id = $1
	`, id)
	return err
}

// ListByTenant returns all keys for a tenant, newest first.
func (r *PreAuthKeys) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*PreAuthKey, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, description, secret_hash, tags, reusable, ephemeral,
		       created_by, created_at, expires_at, revoked_at, use_count
		FROM pre_auth_keys
		WHERE tenant_id = $1
		ORDER BY created_at DESC
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []*PreAuthKey
	for rows.Next() {
		var k PreAuthKey
		if err := rows.Scan(
			&k.ID, &k.TenantID, &k.Description, &k.SecretHash, &k.Tags,
			&k.Reusable, &k.Ephemeral,
			&k.CreatedBy, &k.CreatedAt, &k.ExpiresAt, &k.RevokedAt, &k.UseCount,
		); err != nil {
			return nil, err
		}
		keys = append(keys, &k)
	}
	return keys, rows.Err()
}

// Revoke marks a key as revoked. Idempotent — already-revoked keys are
// untouched.
func (r *PreAuthKeys) Revoke(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE pre_auth_keys
		   SET revoked_at = COALESCE(revoked_at, now())
		 WHERE id = $1
	`, id)
	return err
}
