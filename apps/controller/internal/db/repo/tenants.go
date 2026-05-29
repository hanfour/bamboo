// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
)

// Tenants is the repository for tenants table.
type Tenants struct {
	pool *db.Pool
}

// NewTenants constructs a Tenants repository.
func NewTenants(pool *db.Pool) *Tenants {
	return &Tenants{pool: pool}
}

// Tenant is the domain model.
type Tenant struct {
	ID        uuid.UUID
	Name      string
	Slug      string
	IPPool    string // CIDR text form, e.g. "100.64.0.0/24"
	IP6Pool   string // CIDR text form, e.g. "fdba:1100::/64"
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Create inserts a new tenant. Slug must be unique; ipPool must be a CIDR
// within 100.64.0.0/10.
func (r *Tenants) Create(ctx context.Context, name, slug, ipPool string) (*Tenant, error) {
	var t Tenant
	err := r.pool.QueryRow(ctx, `
		INSERT INTO tenants (name, slug, ip_pool)
		VALUES ($1, $2, $3::cidr)
		RETURNING id, name, slug, ip_pool::text, ip6_pool::text, created_at, updated_at
	`, name, slug, ipPool).Scan(
		&t.ID, &t.Name, &t.Slug, &t.IPPool, &t.IP6Pool, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetBySlug returns a tenant by its URL-safe slug. Soft-deleted rows are
// excluded.
func (r *Tenants) GetBySlug(ctx context.Context, slug string) (*Tenant, error) {
	var t Tenant
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, slug, ip_pool::text, ip6_pool::text, created_at, updated_at
		FROM tenants
		WHERE slug = $1 AND deleted_at IS NULL
	`, slug).Scan(&t.ID, &t.Name, &t.Slug, &t.IPPool, &t.IP6Pool, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &t, nil
}

// GetOrCreate looks up a tenant by slug. If missing, it creates one with the
// supplied defaults. This convenience method is intended for development and
// single-tenant deployments. Production registration must validate identity
// before creating a tenant.
func (r *Tenants) GetOrCreate(ctx context.Context, slug, defaultName, defaultIPPool string) (*Tenant, error) {
	t, err := r.GetBySlug(ctx, slug)
	if err == nil {
		return t, nil
	}
	if !errIs(err, ErrNotFound) {
		return nil, err
	}
	return r.Create(ctx, defaultName, slug, defaultIPPool)
}

// GetByID returns a tenant by primary key.
func (r *Tenants) GetByID(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	var t Tenant
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, slug, ip_pool::text, ip6_pool::text, created_at, updated_at
		FROM tenants
		WHERE id = $1 AND deleted_at IS NULL
	`, id).Scan(&t.ID, &t.Name, &t.Slug, &t.IPPool, &t.IP6Pool, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &t, nil
}
