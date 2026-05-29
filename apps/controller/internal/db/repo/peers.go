// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/jackc/pgx/v5"
)

// Peers is the repository for peers table.
type Peers struct {
	pool *db.Pool
}

// peerTagsSubquery returns the SQL fragment used by every Peer read
// path to materialize the Tags slice without a second round-trip.
// The schema (migration 00001) has tags + peer_tags normalized: tag
// values live in `tags(id, tenant_id, name)`, peer_tags is the
// (peer_id, tag_id) join. We surface tag *names* — that's what the
// UI and the ACL DSL (`tag:foo`) consume.
const peerTagsSubquery = `COALESCE((
    SELECT array_agg(t.name ORDER BY t.name)
    FROM peer_tags pt
    JOIN tags t ON t.id = pt.tag_id
    WHERE pt.peer_id = peers.id
), ARRAY[]::text[])`

// NewPeers constructs a Peers repository.
func NewPeers(pool *db.Pool) *Peers {
	return &Peers{pool: pool}
}

// Peer is the domain model.
type Peer struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	UserID   *uuid.UUID
	Hostname string
	// PeerDNSName is the short, DNS-safe label this peer is reachable
	// under within the tenant tailnet (resolves to "<label>.bamboo").
	// Nullable: NULL means the peer was inserted before the column
	// existed and hasn't re-registered yet, or a future-flag tenant
	// hasn't opted into MagicDNS. The coordinator's auto-slug path
	// backfills NULLs on the next register; admins can override via
	// PATCH /api/v1/peers/{id}. Migration 00008 enforces uniqueness
	// per tenant via a partial unique index.
	PeerDNSName        *string
	WireGuardPublicKey string
	IP                 string // text form, e.g. "100.64.0.7"
	IP6                string // text form, e.g. "fdba:1100::6440:7"
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
	// OwnerEmail / OwnerDisplayName are populated only by read paths
	// that LEFT JOIN users (ListByTenant, GetByID). For Insert /
	// Update paths and methods that don't join, these stay empty.
	// They mirror users.email / users.display_name for the row
	// identified by peer.user_id. Empty when peer.user_id is NULL.
	OwnerEmail       string
	OwnerDisplayName string

	// ConnectionPath records how the peer is currently reaching the
	// mesh: "direct" (P2P WG handshake), "relay" (NAT traversal
	// failed; using relay fallback), or "unknown" (legacy / not yet
	// reported). Nullable on the wire to match the migration's
	// CHECK + NULL column; the Web UI normalizes nil → "unknown"
	// for back-compat with pre-#138 rows. Migration 00010 adds the
	// column. Distinct from Status (which is online / offline).
	ConnectionPath      *string
	ConnectionPathAt    *time.Time
	ConnectionLatencyMs *int32

	// ApprovalStatus is the device-approval lifecycle state. Distinct
	// from Status (which represents data-plane liveness).
	//   pending  — first-time register via a manual-approval pre-auth-
	//              key; not yet visible to other peers in the mesh.
	//   approved — admin has approved (or the key carried
	//              auto_approve=true / dev-fallback / bearer admin
	//              register implicitly approves). The peer participates
	//              in the mesh.
	//   rejected — admin denied the registration. Peer row is retained
	//              for audit but the row's pubkey is unusable.
	// Migration 00009 adds the column with a backfill that marks every
	// existing peer 'approved' so the rollout is non-breaking. New
	// peers default 'pending'.
	ApprovalStatus string
	// ApprovedAt is set when ApprovalStatus transitions to approved or
	// rejected. NULL for never-acted-upon pending rows. Backfilled to
	// the row's created_at by the 00009 migration for legacy peers so
	// the timeline column still renders coherently in the UI.
	ApprovedAt *time.Time
	// ApprovedByUserID is the admin who acted. NULL for legacy backfill
	// rows + for peers that landed 'approved' via auto_approve=true or
	// require_auth=false dev-fallback (no human admin to attribute).
	ApprovedByUserID *uuid.UUID

	// AdvertisedRoutes (issue #136) is what the peer asked to expose
	// to the rest of the tailnet. ApprovedRoutes is the admin-signed
	// subset that the ACL compiler actually merges into other peers'
	// allowed_ips. Empty slices are the v1 normal — clients that
	// don't pass --advertise-routes leave both columns empty.
	AdvertisedRoutes []string
	ApprovedRoutes   []string
	// ExitNodeCapable (issue #137) records the --advertise-exit-node
	// flag from the peer's last register. ExitNodeApproved tracks
	// the admin's separate sign-off; storing both lets the admin's
	// approval survive a future re-register where the client drops
	// the flag.
	ExitNodeCapable  bool
	ExitNodeApproved bool
	// UsingExitNodePeerID is the peer this row is routing default
	// traffic through (`bamboo up --exit-node=<dns-name>`). NULL =
	// no exit node in use. FK enforces "must be in the same tenant"
	// via ON DELETE SET NULL — if the target peer goes away, this
	// row falls back to direct routing.
	UsingExitNodePeerID *uuid.UUID
}

// Insert creates a new peer. Returns the persisted row.
//
// ApprovalStatus is required: "pending" for the manual-approval path
// or "approved" for the auto-approve / dev-fallback / admin-bearer
// paths. Callers should not pass empty strings — Insert defaults to
// 'pending' as a defensive last resort but a missing value here
// almost always indicates a bug in the credential-resolution path.
func (r *Peers) Insert(ctx context.Context, p *Peer) (*Peer, error) {
	var out Peer
	endpoints := p.Endpoints
	if endpoints == nil {
		endpoints = []string{}
	}
	approval := p.ApprovalStatus
	if approval == "" {
		approval = "pending"
	}
	// approvedAt: set to now() iff the row is being created in the
	// approved state, so the audit timeline ("approved at …") has a
	// real timestamp even when no admin action was needed. Rejected
	// peers cannot be created via Insert — they only reach that state
	// via Reject().
	var approvedAt any
	if approval == "approved" {
		approvedAt = time.Now().UTC()
	}
	// ip6 is nullable: production register always supplies it, but the
	// repo primitive is also used (tests/tools) without one. Bind NULL
	// for the empty case — "":inet would be a syntax error.
	var ip6Arg any
	if p.IP6 != "" {
		ip6Arg = p.IP6
	}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO peers (
		    tenant_id, user_id, hostname, peer_dns_name, wireguard_public_key,
		    ip, ip6, os, client_version, status, endpoints,
		    approval_status, approved_at, approved_by_user_id
		) VALUES ($1, $2, $3, $4, $5, $6::inet, $7::inet, $8, $9, $10, $11, $12, $13, $14)
		RETURNING id, tenant_id, user_id, hostname, peer_dns_name, wireguard_public_key,
		          host(ip), COALESCE(host(ip6), ''), os, client_version, status, endpoints,
		          wg_endpoint, rx_bytes, tx_bytes,
		          created_at, updated_at, last_seen_at, last_handshake_at,
		          approval_status, approved_at, approved_by_user_id,
		          advertised_routes, approved_routes,
		          exit_node_capable, exit_node_approved, using_exit_node_peer_id,
		          connection_path, connection_path_at, connection_latency_ms
	`, p.TenantID, p.UserID, p.Hostname, p.PeerDNSName, p.WireGuardPublicKey,
		p.IP, ip6Arg, p.OS, p.ClientVersion, p.Status, endpoints,
		approval, approvedAt, p.ApprovedByUserID).Scan(
		&out.ID, &out.TenantID, &out.UserID, &out.Hostname, &out.PeerDNSName, &out.WireGuardPublicKey,
		&out.IP, &out.IP6, &out.OS, &out.ClientVersion, &out.Status, &out.Endpoints,
		&out.WGEndpoint, &out.RxBytes, &out.TxBytes,
		&out.CreatedAt, &out.UpdatedAt, &out.LastSeenAt, &out.LastHandshakeAt,
		&out.ApprovalStatus, &out.ApprovedAt, &out.ApprovedByUserID,
		&out.AdvertisedRoutes, &out.ApprovedRoutes,
		&out.ExitNodeCapable, &out.ExitNodeApproved, &out.UsingExitNodePeerID,
		&out.ConnectionPath, &out.ConnectionPathAt, &out.ConnectionLatencyMs,
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
//
// LEFT JOINs users so callers (drawer / list) can show the owner
// alongside peer details without a second round-trip. owner_email /
// owner_display_name are empty strings when peer.user_id is NULL.
func (r *Peers) GetByID(ctx context.Context, id uuid.UUID) (*Peer, error) {
	var p Peer
	var ownerEmail, ownerDisplay *string
	err := r.pool.QueryRow(ctx, `
		SELECT peers.id, peers.tenant_id, peers.user_id, peers.hostname, peers.peer_dns_name, peers.wireguard_public_key,
		       host(peers.ip), COALESCE(host(peers.ip6), ''), peers.os, peers.client_version, peers.status, peers.endpoints,
		       peers.wg_endpoint, peers.rx_bytes, peers.tx_bytes,
		       peers.created_at, peers.updated_at, peers.last_seen_at, peers.last_handshake_at,
		       `+peerTagsSubquery+`,
		       users.email, users.display_name,
		       peers.approval_status, peers.approved_at, peers.approved_by_user_id,
		       peers.advertised_routes, peers.approved_routes,
		       peers.exit_node_capable, peers.exit_node_approved, peers.using_exit_node_peer_id,
		       peers.connection_path, peers.connection_path_at, peers.connection_latency_ms
		FROM peers
		LEFT JOIN users ON users.id = peers.user_id AND users.deleted_at IS NULL
		WHERE peers.id = $1
	`, id).Scan(
		&p.ID, &p.TenantID, &p.UserID, &p.Hostname, &p.PeerDNSName, &p.WireGuardPublicKey,
		&p.IP, &p.IP6, &p.OS, &p.ClientVersion, &p.Status, &p.Endpoints,
		&p.WGEndpoint, &p.RxBytes, &p.TxBytes,
		&p.CreatedAt, &p.UpdatedAt, &p.LastSeenAt, &p.LastHandshakeAt,
		&p.Tags,
		&ownerEmail, &ownerDisplay,
		&p.ApprovalStatus, &p.ApprovedAt, &p.ApprovedByUserID,
		&p.AdvertisedRoutes, &p.ApprovedRoutes,
		&p.ExitNodeCapable, &p.ExitNodeApproved, &p.UsingExitNodePeerID,
		&p.ConnectionPath, &p.ConnectionPathAt, &p.ConnectionLatencyMs,
	)
	if err != nil {
		return nil, asNotFound(err)
	}
	if ownerEmail != nil {
		p.OwnerEmail = *ownerEmail
	}
	if ownerDisplay != nil {
		p.OwnerDisplayName = *ownerDisplay
	}
	return &p, nil
}

// UpdateLastSeen sets last_seen_at = now() and flips status to
// 'online' UNLESS the peer is currently 'disabled' — admin disable
// must survive heartbeat traffic. Returns the peer's tenant_id so
// the caller can scope tenant-level lookups (policy, etc.) without
// a second round-trip. Returns ErrNotFound when the peer no longer
// exists.
//
// last_seen_at still advances for disabled peers — operators want
// to see that a disabled client is still alive in the field
// (it'll keep heartbeating until re-enabled or its session token
// is yanked). Only the status column is sticky.
func (r *Peers) UpdateLastSeen(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	var tenantID uuid.UUID
	err := r.pool.QueryRow(ctx, `
		UPDATE peers
		   SET last_seen_at = now(),
		       status       = CASE WHEN status = 'disabled'
		                            THEN status
		                            ELSE 'online'
		                       END,
		       updated_at   = now()
		 WHERE id = $1
		RETURNING tenant_id
	`, id).Scan(&tenantID)
	if err != nil {
		return uuid.Nil, asNotFound(err)
	}
	return tenantID, nil
}

// FindByPubKey returns a peer matching the (tenant_id, wireguard_public_key)
// pair. Used to make Register idempotent.
func (r *Peers) FindByPubKey(ctx context.Context, tenantID uuid.UUID, pubKey string) (*Peer, error) {
	var p Peer
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, hostname, peer_dns_name, wireguard_public_key,
		       host(ip), COALESCE(host(ip6), ''), os, client_version, status, endpoints,
		       wg_endpoint, rx_bytes, tx_bytes,
		       created_at, updated_at, last_seen_at, last_handshake_at,
		       `+peerTagsSubquery+`,
		       approval_status, approved_at, approved_by_user_id,
		       advertised_routes, approved_routes,
		       exit_node_capable, exit_node_approved, using_exit_node_peer_id,
		       connection_path, connection_path_at, connection_latency_ms
		FROM peers
		WHERE tenant_id = $1 AND wireguard_public_key = $2
	`, tenantID, pubKey).Scan(
		&p.ID, &p.TenantID, &p.UserID, &p.Hostname, &p.PeerDNSName, &p.WireGuardPublicKey,
		&p.IP, &p.IP6, &p.OS, &p.ClientVersion, &p.Status, &p.Endpoints,
		&p.WGEndpoint, &p.RxBytes, &p.TxBytes,
		&p.CreatedAt, &p.UpdatedAt, &p.LastSeenAt, &p.LastHandshakeAt,
		&p.Tags,
		&p.ApprovalStatus, &p.ApprovedAt, &p.ApprovedByUserID,
		&p.AdvertisedRoutes, &p.ApprovedRoutes,
		&p.ExitNodeCapable, &p.ExitNodeApproved, &p.UsingExitNodePeerID,
		&p.ConnectionPath, &p.ConnectionPathAt, &p.ConnectionLatencyMs,
	)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &p, nil
}

// ListByTenant returns all peers within a tenant, ordered by IP.
//
// LEFT JOINs users so the admin Peers page can show the owning user
// alongside each row without a second N+1 round-trip. Empty owner_*
// columns when peer.user_id is NULL (e.g. legacy peers registered
// via dev-fallback before user attribution was wired).
func (r *Peers) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*Peer, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT peers.id, peers.tenant_id, peers.user_id, peers.hostname, peers.peer_dns_name, peers.wireguard_public_key,
		       host(peers.ip), COALESCE(host(peers.ip6), ''), peers.os, peers.client_version, peers.status, peers.endpoints,
		       peers.wg_endpoint, peers.rx_bytes, peers.tx_bytes,
		       peers.created_at, peers.updated_at, peers.last_seen_at, peers.last_handshake_at,
		       `+peerTagsSubquery+`,
		       users.email, users.display_name,
		       peers.approval_status, peers.approved_at, peers.approved_by_user_id,
		       peers.advertised_routes, peers.approved_routes,
		       peers.exit_node_capable, peers.exit_node_approved, peers.using_exit_node_peer_id,
		       peers.connection_path, peers.connection_path_at, peers.connection_latency_ms
		FROM peers
		LEFT JOIN users ON users.id = peers.user_id AND users.deleted_at IS NULL
		WHERE peers.tenant_id = $1
		ORDER BY peers.ip
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var peers []*Peer
	for rows.Next() {
		var p Peer
		var ownerEmail, ownerDisplay *string
		if err := rows.Scan(
			&p.ID, &p.TenantID, &p.UserID, &p.Hostname, &p.PeerDNSName, &p.WireGuardPublicKey,
			&p.IP, &p.IP6, &p.OS, &p.ClientVersion, &p.Status, &p.Endpoints,
			&p.WGEndpoint, &p.RxBytes, &p.TxBytes,
			&p.CreatedAt, &p.UpdatedAt, &p.LastSeenAt, &p.LastHandshakeAt,
			&p.Tags,
			&ownerEmail, &ownerDisplay,
			&p.ApprovalStatus, &p.ApprovedAt, &p.ApprovedByUserID,
			&p.AdvertisedRoutes, &p.ApprovedRoutes,
			&p.ExitNodeCapable, &p.ExitNodeApproved, &p.UsingExitNodePeerID,
			&p.ConnectionPath, &p.ConnectionPathAt, &p.ConnectionLatencyMs,
		); err != nil {
			return nil, err
		}
		if ownerEmail != nil {
			p.OwnerEmail = *ownerEmail
		}
		if ownerDisplay != nil {
			p.OwnerDisplayName = *ownerDisplay
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

// UpdateDNSName sets peers.peer_dns_name. Pass a nil pointer to
// clear the column. Returns true when the column actually changed
// (so the caller can publish a PeerUpdated event); a no-op update
// returns (false, nil). The migration's partial unique index will
// surface a Postgres unique-violation error when name is non-NULL
// and already used by another peer in the same tenant — callers
// should pre-validate via IsDNSNameTaken to give the user a clean
// 409 instead of a server-side DB error.
func (r *Peers) UpdateDNSName(ctx context.Context, id uuid.UUID, name *string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET peer_dns_name = $2,
		       updated_at    = now()
		 WHERE id = $1
		   AND peer_dns_name IS DISTINCT FROM $2
	`, id, name)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// IsDNSNameTaken reports whether a peer in the given tenant already
// uses peer_dns_name = name. NULL rows are not counted as taken
// (the partial unique index treats them as absent), so this is safe
// to call repeatedly during the auto-slug collision retry loop.
func (r *Peers) IsDNSNameTaken(ctx context.Context, tenantID uuid.UUID, name string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM peers
			 WHERE tenant_id = $1
			   AND peer_dns_name = $2
		)
	`, tenantID, name).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
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
// constraint defined in migration 00001. Returns the number of rows
// deleted; 0 means the peer was already gone.
func (r *Peers) Delete(ctx context.Context, id uuid.UUID) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM peers WHERE id = $1`, id)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// SetAdvertisedRoutes replaces the peer's advertised_routes column
// (issue #136). Called by the coordinator when a peer Register /
// Heartbeat carries an --advertise-routes payload. The Web UI's
// "pending advertisements" view diffs advertised_routes against
// approved_routes to surface the admin checklist.
func (r *Peers) SetAdvertisedRoutes(ctx context.Context, id uuid.UUID, routes []string) error {
	if routes == nil {
		routes = []string{}
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET advertised_routes = $2,
		       updated_at        = now()
		 WHERE id = $1
	`, id, routes)
	return err
}

// SetApprovedRoutes replaces the peer's approved_routes column.
// Admin-only on the wire (the REST handler gates this). The ACL
// compiler reads approved_routes when assembling other peers'
// allowed_ips so the approved subset propagates through the mesh
// without a re-register from anyone else.
func (r *Peers) SetApprovedRoutes(ctx context.Context, id uuid.UUID, routes []string) error {
	if routes == nil {
		routes = []string{}
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET approved_routes = $2,
		       updated_at      = now()
		 WHERE id = $1
	`, id, routes)
	return err
}

// SetExitNodeCapable records the --advertise-exit-node flag from a
// Register call (issue #137). Separate from ExitNodeApproved so the
// admin's sign-off survives a future re-register that drops the
// flag — re-approval shouldn't be required just because the client
// config was edited.
func (r *Peers) SetExitNodeCapable(ctx context.Context, id uuid.UUID, capable bool) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET exit_node_capable = $2,
		       updated_at        = now()
		 WHERE id = $1
	`, id, capable)
	return err
}

// SetExitNodeApproved is the admin's separate sign-off on the
// peer's exit-node role. Setting true makes the peer eligible to be
// picked by other peers via --exit-node=<dns>.
func (r *Peers) SetExitNodeApproved(ctx context.Context, id uuid.UUID, approved bool) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET exit_node_approved = $2,
		       updated_at         = now()
		 WHERE id = $1
	`, id, approved)
	return err
}

// SetUsingExitNode records that this peer is routing default
// traffic through `targetID`. Pass nil to clear (no exit node).
// FK enforces the target exists in the same tenant; the REST
// handler additionally verifies tenant scoping before calling.
func (r *Peers) SetUsingExitNode(ctx context.Context, id uuid.UUID, targetID *uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET using_exit_node_peer_id = $2,
		       updated_at              = now()
		 WHERE id = $1
	`, id, targetID)
	return err
}

// SetConnectionPath records the peer's most-recent connection path
// (issue #138). path is one of "direct" / "relay" / "unknown"; the
// CHECK constraint on the column rejects anything else. latencyMs
// is optional — pass 0 to leave the column NULL (clients without
// a measurement do this on every heartbeat).
//
// Returns the *previous* path string (empty when no prior value
// existed, or when the row didn't move because nothing changed) and
// whether the *path* value transitioned to something different. The
// previous-path signal is what powers the #138 v2 connection-event
// timeline — a latency-only update keeps the path the same and so
// must NOT log a timeline row, otherwise normal RTT flicker would
// drown out real direct↔relay flips.
//
// "pathChanged" is true iff the path string is now different from
// what was persisted before. A latency-only update returns false; a
// no-op update where both columns matched also returns false.
func (r *Peers) SetConnectionPath(ctx context.Context, id uuid.UUID, path string, latencyMs int32) (prevPath string, pathChanged bool, err error) {
	var latencyArg any
	if latencyMs > 0 {
		latencyArg = latencyMs
	}
	// CTE captures the pre-update value (NULL → empty string via
	// COALESCE) before the UPDATE runs, then RETURNING surfaces both
	// the old value and the changed flag in one round-trip. The
	// FROM-clause join is how we pull the CTE row into the UPDATE
	// scope so RETURNING can reference it.
	var prev string
	var changed bool
	err = r.pool.QueryRow(ctx, `
		WITH old AS (
		    SELECT COALESCE(connection_path, '') AS prev_path
		      FROM peers
		     WHERE id = $1
		)
		UPDATE peers
		   SET connection_path        = $2,
		       connection_path_at     = now(),
		       connection_latency_ms  = $3,
		       updated_at             = now()
		  FROM old
		 WHERE peers.id = $1
		   AND (
		       connection_path IS DISTINCT FROM $2
		    OR connection_latency_ms IS DISTINCT FROM $3
		   )
		RETURNING old.prev_path, (old.prev_path IS DISTINCT FROM $2)
	`, id, path, latencyArg).Scan(&prev, &changed)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either the peer row is missing, or nothing changed at all
		// (the WHERE clause filtered out the UPDATE). Both shapes
		// surface as the same "nothing to log" signal to the caller.
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return prev, changed, nil
}

// Approve flips a peer from approval_status='pending' to 'approved'
// and stamps the approver. No-op (returns 0) when the row is already
// approved — callers do not need to special-case re-approval. Returns
// ErrNotFound when the peer does not exist. Returns a guard error
// when the peer is currently 'rejected' (a rejected peer must be
// deleted + re-registered; flipping rejected → approved would mask
// the original admin decision in the audit trail).
//
// approver may be nil for dev-fallback paths where no JWT is present.
// The approved_by_user_id column accepts NULL via the same FK
// constraint as user_invitations.accepted_by; nil here avoids the
// foreign-key violation a synthetic uuid.Nil would trigger.
func (r *Peers) Approve(ctx context.Context, id uuid.UUID, approver *uuid.UUID) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET approval_status      = 'approved',
		       approved_at          = now(),
		       approved_by_user_id  = $2,
		       updated_at           = now()
		 WHERE id = $1
		   AND approval_status      = 'pending'
	`, id, approver)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Reject flips a peer from approval_status='pending' to 'rejected'.
// Same shape as Approve including the nilable approver — no-op when
// the row already reached a terminal state. The peer row is retained
// for audit; callers can later Delete it if they want to free the
// pubkey for re-registration.
func (r *Peers) Reject(ctx context.Context, id uuid.UUID, approver *uuid.UUID) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE peers
		   SET approval_status      = 'rejected',
		       approved_at          = now(),
		       approved_by_user_id  = $2,
		       updated_at           = now()
		 WHERE id = $1
		   AND approval_status      = 'pending'
	`, id, approver)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// SetTags replaces a peer's tag set atomically. Tags are normalized
// in the schema: tag *names* live in `tags(tenant_id, name)` and the
// peer_tags join links peer_id → tag_id. SetTags:
//
//  1. canonicalizes the input (trim, drop empties, dedupe, sort);
//  2. upserts each name into `tags` for the peer's tenant;
//  3. replaces the peer_tags rows for this peer with the resolved
//     tag_ids in a single transaction.
//
// Returns the canonical tag-name set the row now carries, so
// callers can audit-log a stable diff without re-querying.
//
// Note we keep orphan `tags` rows on purpose (no DELETE FROM tags
// when the last peer with a tag is untagged). Tags are tenant-
// scoped descriptors and may be referenced by ACL policy text by
// name; collecting them is a separate concern from peer membership.
func (r *Peers) SetTags(ctx context.Context, id uuid.UUID, tags []string) ([]string, error) {
	// Canonicalize: trim, drop empties + over-long, dedupe, sort.
	// The 64-char cap matches the typical max for ACL labels we've
	// seen elsewhere; intentionally silent so a long entry gets
	// dropped instead of failing the whole call (hostname rejects
	// outright because there's only one of it per request).
	seen := make(map[string]struct{}, len(tags))
	clean := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || len(t) > 64 {
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

	// Need the peer's tenant_id to scope tag upserts. We could
	// require callers to pass it, but the API layer already has
	// the peer loaded and looking it up here keeps SetTags's
	// signature minimal.
	var tenantID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT tenant_id FROM peers WHERE id = $1`, id).Scan(&tenantID); err != nil {
		return nil, asNotFound(err)
	}

	// Resolve each tag name to a tag_id, creating the tags row when
	// missing. The ON CONFLICT clause's DO UPDATE is a self-assign
	// no-op that exists solely so RETURNING fires for existing rows;
	// DO NOTHING would skip RETURNING and force a second SELECT.
	tagIDs := make([]uuid.UUID, 0, len(clean))
	for _, name := range clean {
		var tagID uuid.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO tags (tenant_id, name) VALUES ($1, $2)
			ON CONFLICT (tenant_id, name) DO UPDATE SET name = EXCLUDED.name
			RETURNING id
		`, tenantID, name).Scan(&tagID); err != nil {
			return nil, err
		}
		tagIDs = append(tagIDs, tagID)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM peer_tags WHERE peer_id = $1`, id); err != nil {
		return nil, err
	}
	for _, tagID := range tagIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO peer_tags (peer_id, tag_id) VALUES ($1, $2)
			ON CONFLICT (peer_id, tag_id) DO NOTHING
		`, id, tagID); err != nil {
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
