// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
)

// TenantDNS is the repository for tenant_dns_config rows.
//
// A tenant may have zero or one row here. Get returns a defaults-
// populated record when no row exists so the admin UI can render a
// page before anyone has touched DNS settings; Upsert creates or
// overwrites the row atomically.
type TenantDNS struct {
	pool *db.Pool
}

// NewTenantDNS constructs a TenantDNS repository.
func NewTenantDNS(pool *db.Pool) *TenantDNS {
	return &TenantDNS{pool: pool}
}

// TenantDNSConfig is the domain model. Mirrors the table 1:1.
type TenantDNSConfig struct {
	TenantID           uuid.UUID
	MagicDNSEnabled    bool
	GlobalNameservers  []string
	SearchDomains      []string
	OverrideDNSServers bool
	UpdatedAt          time.Time
	UpdatedBy          *uuid.UUID
}

// defaultsFor returns the defaults applied on read when no row exists
// for a tenant — same shape the migration writes for a fresh insert.
// Kept in Go (rather than relying on PG defaults via a virtual SELECT)
// so callers always get the same struct regardless of whether the row
// has been materialized.
func defaultsFor(tenantID uuid.UUID) *TenantDNSConfig {
	return &TenantDNSConfig{
		TenantID:           tenantID,
		MagicDNSEnabled:    true,
		GlobalNameservers:  []string{},
		SearchDomains:      []string{},
		OverrideDNSServers: false,
	}
}

// Get returns the tenant's DNS config, or a defaults-populated record
// (no row yet). The two cases are distinguishable by UpdatedAt: zero
// time means "never written".
func (r *TenantDNS) Get(ctx context.Context, tenantID uuid.UUID) (*TenantDNSConfig, error) {
	c := defaultsFor(tenantID)
	err := r.pool.QueryRow(ctx, `
		SELECT magic_dns_enabled, global_nameservers, search_domains,
		       override_dns_servers, updated_at, updated_by
		FROM tenant_dns_config
		WHERE tenant_id = $1
	`, tenantID).Scan(
		&c.MagicDNSEnabled, &c.GlobalNameservers, &c.SearchDomains,
		&c.OverrideDNSServers, &c.UpdatedAt, &c.UpdatedBy,
	)
	if err != nil {
		// Distinguish ErrNotFound (legitimate "no row") from real DB
		// errors. asNotFound maps pgx no-rows to ErrNotFound.
		if errors.Is(asNotFound(err), ErrNotFound) {
			return c, nil
		}
		return nil, err
	}
	if c.GlobalNameservers == nil {
		c.GlobalNameservers = []string{}
	}
	if c.SearchDomains == nil {
		c.SearchDomains = []string{}
	}
	return c, nil
}

// Upsert writes the config row, creating it if absent. updated_at is
// bumped on every write; updated_by captures the admin who made the
// change for audit (nil for dev-fallback).
func (r *TenantDNS) Upsert(ctx context.Context, c *TenantDNSConfig) (*TenantDNSConfig, error) {
	ns := c.GlobalNameservers
	if ns == nil {
		ns = []string{}
	}
	sd := c.SearchDomains
	if sd == nil {
		sd = []string{}
	}
	var out TenantDNSConfig
	err := r.pool.QueryRow(ctx, `
		INSERT INTO tenant_dns_config (
		    tenant_id, magic_dns_enabled, global_nameservers, search_domains,
		    override_dns_servers, updated_at, updated_by
		) VALUES ($1, $2, $3, $4, $5, now(), $6)
		ON CONFLICT (tenant_id) DO UPDATE SET
		    magic_dns_enabled    = EXCLUDED.magic_dns_enabled,
		    global_nameservers   = EXCLUDED.global_nameservers,
		    search_domains       = EXCLUDED.search_domains,
		    override_dns_servers = EXCLUDED.override_dns_servers,
		    updated_at           = now(),
		    updated_by           = EXCLUDED.updated_by
		RETURNING tenant_id, magic_dns_enabled, global_nameservers, search_domains,
		          override_dns_servers, updated_at, updated_by
	`, c.TenantID, c.MagicDNSEnabled, ns, sd, c.OverrideDNSServers, c.UpdatedBy).Scan(
		&out.TenantID, &out.MagicDNSEnabled, &out.GlobalNameservers, &out.SearchDomains,
		&out.OverrideDNSServers, &out.UpdatedAt, &out.UpdatedBy,
	)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
