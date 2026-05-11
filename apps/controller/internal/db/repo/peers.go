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
	// Endpoints are host:port candidates for direct connection,
	// populated by the client via STUN discovery (or LAN heuristics).
	// Empty until the peer first reports them.
	Endpoints  []string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastSeenAt *time.Time
}

// Insert creates a new peer. Returns the persisted row.
func (r *Peers) Insert(ctx context.Context, p *Peer) (*Peer, error) {
	var out Peer
	endpoints := p.Endpoints
	if endpoints == nil {
		endpoints = []string{}
	}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO peers (
		    tenant_id, user_id, hostname, wireguard_public_key, ip, os, client_version, status, endpoints
		) VALUES ($1, $2, $3, $4, $5::inet, $6, $7, $8, $9)
		RETURNING id, tenant_id, user_id, hostname, wireguard_public_key,
		          host(ip), os, client_version, status, endpoints,
		          created_at, updated_at, last_seen_at
	`, p.TenantID, p.UserID, p.Hostname, p.WireGuardPublicKey, p.IP, p.OS, p.ClientVersion, p.Status, endpoints).Scan(
		&out.ID, &out.TenantID, &out.UserID, &out.Hostname, &out.WireGuardPublicKey,
		&out.IP, &out.OS, &out.ClientVersion, &out.Status, &out.Endpoints,
		&out.CreatedAt, &out.UpdatedAt, &out.LastSeenAt,
	)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetByID returns a peer by primary key.
func (r *Peers) GetByID(ctx context.Context, id uuid.UUID) (*Peer, error) {
	var p Peer
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, hostname, wireguard_public_key,
		       host(ip), os, client_version, status, endpoints,
		       created_at, updated_at, last_seen_at
		FROM peers
		WHERE id = $1
	`, id).Scan(
		&p.ID, &p.TenantID, &p.UserID, &p.Hostname, &p.WireGuardPublicKey,
		&p.IP, &p.OS, &p.ClientVersion, &p.Status, &p.Endpoints,
		&p.CreatedAt, &p.UpdatedAt, &p.LastSeenAt,
	)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &p, nil
}

// UpdateLastSeen sets last_seen_at = now() and status = 'online'.
// Returns the number of rows affected (0 if the peer no longer exists).
func (r *Peers) UpdateLastSeen(ctx context.Context, id uuid.UUID) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET last_seen_at = now(),
		       status       = 'online',
		       updated_at   = now()
		 WHERE id = $1
	`, id)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// FindByPubKey returns a peer matching the (tenant_id, wireguard_public_key)
// pair. Used to make Register idempotent.
func (r *Peers) FindByPubKey(ctx context.Context, tenantID uuid.UUID, pubKey string) (*Peer, error) {
	var p Peer
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, hostname, wireguard_public_key,
		       host(ip), os, client_version, status, endpoints,
		       created_at, updated_at, last_seen_at
		FROM peers
		WHERE tenant_id = $1 AND wireguard_public_key = $2
	`, tenantID, pubKey).Scan(
		&p.ID, &p.TenantID, &p.UserID, &p.Hostname, &p.WireGuardPublicKey,
		&p.IP, &p.OS, &p.ClientVersion, &p.Status, &p.Endpoints,
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
		       host(ip), os, client_version, status, endpoints,
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
			&p.IP, &p.OS, &p.ClientVersion, &p.Status, &p.Endpoints,
			&p.CreatedAt, &p.UpdatedAt, &p.LastSeenAt,
		); err != nil {
			return nil, err
		}
		peers = append(peers, &p)
	}
	return peers, rows.Err()
}

// UpdateEndpoints replaces the peer's endpoint list and bumps
// updated_at. Returns true when the new list differs from what was
// stored (so the caller can decide whether to publish a PeerUpdated
// event on the bus). A no-op update — same list as before — returns
// (false, nil) without an error.
func (r *Peers) UpdateEndpoints(ctx context.Context, id uuid.UUID, endpoints []string) (bool, error) {
	if endpoints == nil {
		endpoints = []string{}
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET endpoints  = $2,
		       updated_at = now()
		 WHERE id = $1
		   AND endpoints IS DISTINCT FROM $2
	`, id, endpoints)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SetStatusByPubKey writes status (and bumps last_seen_at when the
// proposed value is newer) for the peer with the given WireGuard
// public key. Used by the wg-state reporter to mirror cryptokey-
// routing state into the DB without clobbering a fresher
// last_seen_at already written by client Heartbeat.
// lastSeen.IsZero() is passed as NULL so GREATEST keeps the
// existing value.
//
// pubkey is treated as globally unique here, which is enforced by
// the peers_pubkey_global_unique index (migration 00004): WG
// cryptokey routing operates on pubkey alone, so two tenants
// sharing a pubkey is operationally meaningless.
//
// Returns the number of rows updated. The reporter uses 0 as a
// signal that a pubkey is in the wg dump but not in the DB
// (e.g. a manually-added peer that never registered via REST).
func (r *Peers) SetStatusByPubKey(ctx context.Context, pubKey, status string, lastSeen time.Time) (int64, error) {
	var ts any
	if !lastSeen.IsZero() {
		ts = lastSeen.UTC()
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET status       = $1,
		       last_seen_at = GREATEST(last_seen_at, $2::timestamptz),
		       updated_at   = now()
		 WHERE wireguard_public_key = $3
	`, status, ts, pubKey)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// MarkOfflineExcept sets status='offline' on every peer whose
// pubkey is NOT in keepPubKeys. last_seen_at is preserved so the
// UI can still show "last seen N hours ago" for retired peers.
//
// An empty keepPubKeys is treated as "skip the sweep this round"
// rather than "mark every peer offline". The wg-state reporter can
// briefly observe an empty dump (interface flap, host-script
// transient error, controller starting before WG is configured)
// and we don't want one empty observation to flip every peer
// offline simultaneously. The next non-empty tick catches up.
//
// Callers that genuinely want to mark all peers offline should
// use a separate API; none exists today because there's no
// production need.
func (r *Peers) MarkOfflineExcept(ctx context.Context, keepPubKeys []string) error {
	if len(keepPubKeys) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET status     = 'offline',
		       updated_at = now()
		 WHERE wireguard_public_key <> ALL($1::text[])
		   AND status <> 'offline'
	`, keepPubKeys)
	return err
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
