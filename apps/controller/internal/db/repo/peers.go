// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
)

// Peers is the repository for peers table.
type Peers struct {
	pool *db.Pool
}

// peerTagsSubquery returns the SQL fragment used by every Peer read
// path to materialize the Tags slice without a second round-trip.
// COALESCE keeps the scan target as a real (possibly empty) array
// so callers don't have to nil-check.
const peerTagsSubquery = `COALESCE((SELECT array_agg(tag ORDER BY tag) FROM peer_tags WHERE peer_id = peers.id), ARRAY[]::text[])`

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
	Endpoints []string
	// Tags are user-defined labels from the peer_tags join table.
	// Populated by every read path via a COALESCE+array_agg subquery
	// so callers never see nil; Insert RETURNING sets this to an
	// empty slice because a freshly-inserted peer has no tags yet.
	Tags []string
	// WGEndpoint is the host:port the hub currently sees this peer
	// dial from, written by the wg-state reporter. NULL until the
	// reporter observes a non-"(none)" endpoint. Distinct from
	// Endpoints, which is what the *peer itself* advertises.
	WGEndpoint      *string
	RxBytes         int64
	TxBytes         int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastSeenAt      *time.Time
	LastHandshakeAt *time.Time // strictly the WG handshake time; NULL = never handshook
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
		          wg_endpoint, rx_bytes, tx_bytes,
		          created_at, updated_at, last_seen_at, last_handshake_at
	`, p.TenantID, p.UserID, p.Hostname, p.WireGuardPublicKey, p.IP, p.OS, p.ClientVersion, p.Status, endpoints).Scan(
		&out.ID, &out.TenantID, &out.UserID, &out.Hostname, &out.WireGuardPublicKey,
		&out.IP, &out.OS, &out.ClientVersion, &out.Status, &out.Endpoints,
		&out.WGEndpoint, &out.RxBytes, &out.TxBytes,
		&out.CreatedAt, &out.UpdatedAt, &out.LastSeenAt, &out.LastHandshakeAt,
	)
	if err != nil {
		return nil, err
	}
	// Freshly-inserted peer has no peer_tags rows yet; skip the
	// subquery and inline the empty slice.
	out.Tags = []string{}
	return &out, nil
}

// GetByID returns a peer by primary key.
func (r *Peers) GetByID(ctx context.Context, id uuid.UUID) (*Peer, error) {
	var p Peer
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, hostname, wireguard_public_key,
		       host(ip), os, client_version, status, endpoints,
		       wg_endpoint, rx_bytes, tx_bytes,
		       created_at, updated_at, last_seen_at, last_handshake_at,
		       `+peerTagsSubquery+`
		FROM peers
		WHERE id = $1
	`, id).Scan(
		&p.ID, &p.TenantID, &p.UserID, &p.Hostname, &p.WireGuardPublicKey,
		&p.IP, &p.OS, &p.ClientVersion, &p.Status, &p.Endpoints,
		&p.WGEndpoint, &p.RxBytes, &p.TxBytes,
		&p.CreatedAt, &p.UpdatedAt, &p.LastSeenAt, &p.LastHandshakeAt,
		&p.Tags,
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
		       wg_endpoint, rx_bytes, tx_bytes,
		       created_at, updated_at, last_seen_at, last_handshake_at,
		       `+peerTagsSubquery+`
		FROM peers
		WHERE tenant_id = $1 AND wireguard_public_key = $2
	`, tenantID, pubKey).Scan(
		&p.ID, &p.TenantID, &p.UserID, &p.Hostname, &p.WireGuardPublicKey,
		&p.IP, &p.OS, &p.ClientVersion, &p.Status, &p.Endpoints,
		&p.WGEndpoint, &p.RxBytes, &p.TxBytes,
		&p.CreatedAt, &p.UpdatedAt, &p.LastSeenAt, &p.LastHandshakeAt,
		&p.Tags,
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
		       wg_endpoint, rx_bytes, tx_bytes,
		       created_at, updated_at, last_seen_at, last_handshake_at,
		       `+peerTagsSubquery+`
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
			&p.WGEndpoint, &p.RxBytes, &p.TxBytes,
			&p.CreatedAt, &p.UpdatedAt, &p.LastSeenAt, &p.LastHandshakeAt,
			&p.Tags,
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

// WGSyncState is the per-peer snapshot the wg-state reporter feeds
// into SyncWGState. It mirrors the subset of `wg show <iface> dump`
// the controller persists: liveness (Status / LastHandshake) plus
// the cosmetic-but-useful counters (Endpoint / RxBytes / TxBytes).
type WGSyncState struct {
	PubKey        string
	Status        string
	LastHandshake time.Time // zero = no handshake observed; existing column value is kept
	Endpoint      string    // "" = wg dump says "(none)"; existing column value is kept
	RxBytes       int64
	TxBytes       int64
}

// SyncWGState mirrors one peer's state from the host's wg dump into
// the peers row identified by pubkey. Used by the wg-state reporter.
//
//   - status is overwritten EXCEPT when the row is admin-disabled
//     ('disabled'), in which case the reporter's online/offline
//     derivation is suppressed and the column sticks at 'disabled'.
//     Wgsync metrics (endpoint, bytes, handshake) still update so
//     forensic observability of a disabled peer remains useful.
//   - last_seen_at and last_handshake_at use GREATEST so a stale
//     dump (clock skew, parallel write from REST Heartbeat) cannot
//     roll the timestamp backwards.
//   - wg_endpoint uses COALESCE: empty Endpoint (meaning "(none)" in
//     the dump) preserves the previously-observed endpoint instead
//     of erasing it, so the UI doesn't blink to "—" on every
//     interface flap.
//   - rx_bytes / tx_bytes overwrite outright. The wg counters are
//     monotonic within an interface lifetime but reset when the
//     interface restarts; storing the current snapshot is the
//     simplest faithful representation.
//
// pubkey is treated as globally unique here, enforced by
// peers_pubkey_global_unique (migration 00004). Returns the number
// of rows updated; the reporter uses 0 as a signal that a pubkey is
// in the wg dump but not in the DB.
func (r *Peers) SyncWGState(ctx context.Context, s WGSyncState) (int64, error) {
	var handshakeArg any
	if !s.LastHandshake.IsZero() {
		handshakeArg = s.LastHandshake.UTC()
	}
	var endpointArg any
	if s.Endpoint != "" {
		endpointArg = s.Endpoint
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET status            = CASE WHEN status = 'disabled' THEN 'disabled' ELSE $1 END,
		       last_seen_at      = GREATEST(last_seen_at, $2::timestamptz),
		       last_handshake_at = GREATEST(last_handshake_at, $2::timestamptz),
		       wg_endpoint       = COALESCE($3, wg_endpoint),
		       rx_bytes          = $4,
		       tx_bytes          = $5,
		       updated_at        = now()
		 WHERE wireguard_public_key = $6
	`, s.Status, handshakeArg, endpointArg, s.RxBytes, s.TxBytes, s.PubKey)
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
		   AND status NOT IN ('offline', 'disabled')
	`, keepPubKeys)
	return err
}

// UpdateHostname renames a peer in place. Returns true when the new
// hostname differs from what was stored, so the caller can decide
// whether to publish a PeerUpdated event and write an audit row.
// A no-op update — same hostname as before — returns (false, nil).
func (r *Peers) UpdateHostname(ctx context.Context, id uuid.UUID, hostname string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET hostname   = $2,
		       updated_at = now()
		 WHERE id = $1
		   AND hostname IS DISTINCT FROM $2
	`, id, hostname)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SetStatus writes the status column directly. Used by the REST
// handlers for the disable / enable buttons; the wg-state reporter
// uses SyncWGState instead, which also bumps the timestamp columns.
// Returns the number of rows updated; 0 means the peer no longer
// exists.
func (r *Peers) SetStatus(ctx context.Context, id uuid.UUID, status string) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET status     = $2,
		       updated_at = now()
		 WHERE id = $1
		   AND status IS DISTINCT FROM $2
	`, id, status)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Delete removes a peer by id. peer_tags rows cascade via the FK
// constraint (migration 00006). Returns the number of rows deleted;
// 0 means the peer was already gone, which is treated as success by
// the REST DELETE handler (idempotent).
func (r *Peers) Delete(ctx context.Context, id uuid.UUID) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM peers WHERE id = $1`, id)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// SetTags replaces a peer's tag set atomically. The previous set is
// deleted then the new tags inserted inside a single transaction so
// a partial write can't leave the peer with a mix of old + new.
//
// Tags are trimmed; empty strings (after trimming) are dropped.
// Duplicates in the input slice collapse to a single row via ON
// CONFLICT DO NOTHING — the table's PK guarantees uniqueness either
// way, but this avoids a noisy error.
//
// Returns the canonicalized tag set the row now carries (sorted,
// de-duplicated, trimmed) so callers can put it in the audit log
// diff and the response without re-querying.
func (r *Peers) SetTags(ctx context.Context, id uuid.UUID, tags []string) ([]string, error) {
	// Canonicalize: trim, drop empties, dedupe, sort. We sort so
	// audit-log diffs are stable across runs even when the caller
	// passes tags in different orders.
	seen := make(map[string]struct{}, len(tags))
	clean := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		clean = append(clean, t)
	}
	sort.Strings(clean)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM peer_tags WHERE peer_id = $1`, id); err != nil {
		return nil, err
	}
	for _, t := range clean {
		if _, err := tx.Exec(ctx, `
			INSERT INTO peer_tags (peer_id, tag) VALUES ($1, $2)
			ON CONFLICT (peer_id, tag) DO NOTHING
		`, id, t); err != nil {
			return nil, err
		}
	}
	// Bump updated_at on the peer so consumers polling for changes
	// see a fresh timestamp even though no peers column changed.
	if _, err := tx.Exec(ctx, `UPDATE peers SET updated_at = now() WHERE id = $1`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return clean, nil
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
