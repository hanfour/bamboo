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
	ID           uuid.UUID
	Name         string
	Slug         string
	IPPool       string // CIDR text form, e.g. "100.64.0.0/24"
	IP6Pool      string // CIDR text form, e.g. "fdba:1100::/64"
	NAT64Prefix  string // /96 text form; "" means the well-known default (NAT64 Phase B)
	DNS64Enabled bool   // per-tenant DNS64 toggle, default false (NAT64 Phase B)
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Create inserts a new tenant. Slug must be unique; ipPool must be a CIDR
// within 100.64.0.0/10.
func (r *Tenants) Create(ctx context.Context, name, slug, ipPool string) (*Tenant, error) {
	var t Tenant
	err := r.pool.QueryRow(ctx, `
		INSERT INTO tenants (name, slug, ip_pool)
		VALUES ($1, $2, $3::cidr)
		RETURNING id, name, slug, ip_pool::text, ip6_pool::text,
		          COALESCE(nat64_prefix, ''), dns64_enabled, created_at, updated_at
	`, name, slug, ipPool).Scan(
		&t.ID, &t.Name, &t.Slug, &t.IPPool, &t.IP6Pool,
		&t.NAT64Prefix, &t.DNS64Enabled, &t.CreatedAt, &t.UpdatedAt,
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
		SELECT id, name, slug, ip_pool::text, ip6_pool::text,
		       COALESCE(nat64_prefix, ''), dns64_enabled, created_at, updated_at
		FROM tenants
		WHERE slug = $1 AND deleted_at IS NULL
	`, slug).Scan(&t.ID, &t.Name, &t.Slug, &t.IPPool, &t.IP6Pool,
		&t.NAT64Prefix, &t.DNS64Enabled, &t.CreatedAt, &t.UpdatedAt)
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
		SELECT id, name, slug, ip_pool::text, ip6_pool::text,
		       COALESCE(nat64_prefix, ''), dns64_enabled, created_at, updated_at
		FROM tenants
		WHERE id = $1 AND deleted_at IS NULL
	`, id).Scan(&t.ID, &t.Name, &t.Slug, &t.IPPool, &t.IP6Pool,
		&t.NAT64Prefix, &t.DNS64Enabled, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &t, nil
}

// SetNAT64Config updates a tenant's DNS64 settings (NAT64 Phase B).
// prefix must already be validated by nat64.ParsePrefix at the API
// edge; "" stores NULL (the client resolves NULL to the well-known
// default). Returns the updated tenant.
func (r *Tenants) SetNAT64Config(ctx context.Context, id uuid.UUID, prefix string, enabled bool) (*Tenant, error) {
	var prefixArg any // nil → pgx sends NULL; non-nil → sends the string value
	if prefix != "" {
		prefixArg = prefix
	}
	var t Tenant
	err := r.pool.QueryRow(ctx, `
		UPDATE tenants
		SET nat64_prefix = $2, dns64_enabled = $3, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING id, name, slug, ip_pool::text, ip6_pool::text,
		          COALESCE(nat64_prefix, ''), dns64_enabled, created_at, updated_at
	`, id, prefixArg, enabled).Scan(
		&t.ID, &t.Name, &t.Slug, &t.IPPool, &t.IP6Pool,
		&t.NAT64Prefix, &t.DNS64Enabled, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &t, nil
}
