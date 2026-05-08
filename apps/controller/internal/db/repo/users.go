// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
)

// Users is the repository for users table.
type Users struct {
	pool *db.Pool
}

// NewUsers constructs a Users repository.
func NewUsers(pool *db.Pool) *Users {
	return &Users{pool: pool}
}

// User is the domain model.
type User struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	Email        string
	DisplayName  string
	OIDCProvider string
	OIDCSubject  string
	IsAdmin      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// UpsertOIDC creates or updates a user keyed by OIDC identity. Used by the
// login flow when a session is minted.
func (r *Users) UpsertOIDC(ctx context.Context, u *User) (*User, error) {
	var out User
	err := r.pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, display_name, oidc_provider, oidc_subject, is_admin)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, oidc_provider, oidc_subject)
		DO UPDATE SET
		    email        = EXCLUDED.email,
		    display_name = EXCLUDED.display_name,
		    updated_at   = now()
		RETURNING id, tenant_id, email, display_name, oidc_provider, oidc_subject, is_admin, created_at, updated_at
	`, u.TenantID, u.Email, u.DisplayName, u.OIDCProvider, u.OIDCSubject, u.IsAdmin).Scan(
		&out.ID, &out.TenantID, &out.Email, &out.DisplayName,
		&out.OIDCProvider, &out.OIDCSubject, &out.IsAdmin,
		&out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetByID returns a user by primary key. Used by the auth middleware
// when resolving a session JWT to a concrete user record.
func (r *Users) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	var u User
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, email, display_name, oidc_provider, oidc_subject, is_admin, created_at, updated_at
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
	`, id).Scan(
		&u.ID, &u.TenantID, &u.Email, &u.DisplayName,
		&u.OIDCProvider, &u.OIDCSubject, &u.IsAdmin,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &u, nil
}

// GetByEmail returns a user within the given tenant by email.
func (r *Users) GetByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*User, error) {
	var u User
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, email, display_name, oidc_provider, oidc_subject, is_admin, created_at, updated_at
		FROM users
		WHERE tenant_id = $1 AND email = $2 AND deleted_at IS NULL
	`, tenantID, email).Scan(
		&u.ID, &u.TenantID, &u.Email, &u.DisplayName,
		&u.OIDCProvider, &u.OIDCSubject, &u.IsAdmin,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &u, nil
}
