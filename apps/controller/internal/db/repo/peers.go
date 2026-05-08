// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
)

// Peers is the repository for peers table.
type Peers struct {
	pool *db.Pool
}

// NewPeers constructs a Peers repository.
func NewPeers(pool *db.Pool) *Peers {
	return &Peers{pool: pool}
}

// Peer is the domain model.
type Peer struct {
	ID                 uuid.UUID
	TenantID           uuid.UUID
	UserID             *uuid.UUID
	Hostname           string
	WireGuardPublicKey string
	IP                 string // text form, e.g. "100.64.0.7"
	OS                 string
	ClientVersion      string
	Status             string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastSeenAt         *time.Time
}

// Insert creates a new peer. Returns the persisted row.
func (r *Peers) Insert(ctx context.Context, p *Peer) (*Peer, error) {
	var out Peer
	err := r.pool.QueryRow(ctx, `
		INSERT INTO peers (
		    tenant_id, user_id, hostname, wireguard_public_key, ip, os, client_version, status
		) VALUES ($1, $2, $3, $4, $5::inet, $6, $7, $8)
		RETURNING id, tenant_id, user_id, hostname, wireguard_public_key,
		          host(ip), os, client_version, status,
		          created_at, updated_at, last_seen_at
	`, p.TenantID, p.UserID, p.Hostname, p.WireGuardPublicKey, p.IP, p.OS, p.ClientVersion, p.Status).Scan(
		&out.ID, &out.TenantID, &out.UserID, &out.Hostname, &out.WireGuardPublicKey,
		&out.IP, &out.OS, &out.ClientVersion, &out.Status,
		&out.CreatedAt, &out.UpdatedAt, &out.LastSeenAt,
	)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// FindByPubKey returns a peer matching the (tenant_id, wireguard_public_key)
// pair. Used to make Register idempotent.
func (r *Peers) FindByPubKey(ctx context.Context, tenantID uuid.UUID, pubKey string) (*Peer, error) {
	var p Peer
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, hostname, wireguard_public_key,
		       host(ip), os, client_version, status,
		       created_at, updated_at, last_seen_at
		FROM peers
		WHERE tenant_id = $1 AND wireguard_public_key = $2
	`, tenantID, pubKey).Scan(
		&p.ID, &p.TenantID, &p.UserID, &p.Hostname, &p.WireGuardPublicKey,
		&p.IP, &p.OS, &p.ClientVersion, &p.Status,
		&p.CreatedAt, &p.UpdatedAt, &p.LastSeenAt,
	)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &p, nil
}

// ListByTenant returns all peers within a tenant, ordered by IP.
func (r *Peers) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*Peer, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, user_id, hostname, wireguard_public_key,
		       host(ip), os, client_version, status,
		       created_at, updated_at, last_seen_at
		FROM peers
		WHERE tenant_id = $1
		ORDER BY ip
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var peers []*Peer
	for rows.Next() {
		var p Peer
		if err := rows.Scan(
			&p.ID, &p.TenantID, &p.UserID, &p.Hostname, &p.WireGuardPublicKey,
			&p.IP, &p.OS, &p.ClientVersion, &p.Status,
			&p.CreatedAt, &p.UpdatedAt, &p.LastSeenAt,
		); err != nil {
			return nil, err
		}
		peers = append(peers, &p)
	}
	return peers, rows.Err()
}

// UsedIPs returns the IP addresses already allocated within a tenant.
// Used by the IP allocator.
func (r *Peers) UsedIPs(ctx context.Context, tenantID uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT host(ip) FROM peers WHERE tenant_id = $1
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ips []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, err
		}
		ips = append(ips, ip)
	}
	return ips, rows.Err()
}
