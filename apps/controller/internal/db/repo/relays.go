// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
)

// Relays is the repository for the relay_servers table.
type Relays struct {
	pool *db.Pool
}

// NewRelays constructs a Relays repository.
func NewRelays(pool *db.Pool) *Relays {
	return &Relays{pool: pool}
}

// RelayServer is the domain model for a registered relay.
//
// LastHealth* columns are populated by the controller's background
// health-check reaper (§4 P2). Zero/empty on rows the reaper hasn't
// touched yet — readers should treat that as 'unknown', not
// 'unhealthy', so a freshly inserted relay reaches clients before
// its first probe.
type RelayServer struct {
	ID                uuid.UUID
	Region            string
	Hostname          string
	Port              int
	PublicKey         string
	Enabled           bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
	LastHealthCheckAt time.Time
	LastHealthStatus  string
	LastHealthError   string
}

// Insert creates a new relay server. Returns the persisted row.
func (r *Relays) Insert(ctx context.Context, rs *RelayServer) (*RelayServer, error) {
	var out RelayServer
	var checkAt *time.Time
	var status, errStr *string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO relay_servers (region, hostname, port, public_key, enabled)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, region, hostname, port, public_key, enabled, created_at, updated_at,
		          last_health_check_at, last_health_status, last_health_error
	`, rs.Region, rs.Hostname, rs.Port, rs.PublicKey, rs.Enabled).Scan(
		&out.ID, &out.Region, &out.Hostname, &out.Port, &out.PublicKey,
		&out.Enabled, &out.CreatedAt, &out.UpdatedAt,
		&checkAt, &status, &errStr,
	)
	if err != nil {
		return nil, err
	}
	if checkAt != nil {
		out.LastHealthCheckAt = *checkAt
	}
	if status != nil {
		out.LastHealthStatus = *status
	}
	if errStr != nil {
		out.LastHealthError = *errStr
	}
	return &out, nil
}

// ListEnabled returns every enabled, non-deleted relay regardless
// of health state. Used by the admin REST surface and by the
// health-check reaper itself (which needs to see unhealthy rows so
// it can re-probe and recover them).
func (r *Relays) ListEnabled(ctx context.Context) ([]*RelayServer, error) {
	return r.queryRelays(ctx, `
		SELECT id, region, hostname, port, public_key, enabled, created_at, updated_at,
		       last_health_check_at, last_health_status, last_health_error
		FROM relay_servers
		WHERE enabled = true AND deleted_at IS NULL
		ORDER BY region, hostname
	`)
}

// ListEligible returns every relay clients should attempt — enabled,
// not soft-deleted, and not currently flagged 'unhealthy'. Rows with
// no health probe yet ('unknown' / NULL) are INCLUDED so a freshly
// inserted relay reaches clients before its first probe lands; a
// stuck reaper that never runs would otherwise silently hide every
// new relay.
//
// Used by the Coordinator's RegisterResponse + the future
// RelaysChanged WatchPeers event. The admin REST surface keeps
// using ListEnabled so operators can still see unhealthy rows in
// the admin table.
func (r *Relays) ListEligible(ctx context.Context) ([]*RelayServer, error) {
	return r.queryRelays(ctx, `
		SELECT id, region, hostname, port, public_key, enabled, created_at, updated_at,
		       last_health_check_at, last_health_status, last_health_error
		FROM relay_servers
		WHERE enabled = true
		  AND deleted_at IS NULL
		  AND COALESCE(last_health_status, 'unknown') <> 'unhealthy'
		ORDER BY region, hostname
	`)
}

// UpdateHealth records the result of one health probe. status MUST
// be one of 'unknown', 'healthy', 'unhealthy' (the CHECK constraint
// rejects anything else). probeErr is the short reason text shown
// to admins when status='unhealthy'; ignored otherwise.
//
// Returns the PREVIOUS status (normalised to 'unknown' when the row
// was never probed before, matching ListEligible's NULL handling),
// so the caller can detect flips and publish a RelaysChanged event
// only when eligibility actually changed. The CTE captures the
// pre-update value in one round-trip — read-then-update would race
// with concurrent probes (which we serialize per-row in practice,
// but the SQL shouldn't assume that).
func (r *Relays) UpdateHealth(ctx context.Context, id uuid.UUID, status, probeErr string) (prev string, err error) {
	var prevPtr *string
	err = r.pool.QueryRow(ctx, `
		WITH old AS (
		    SELECT last_health_status FROM relay_servers WHERE id = $1
		),
		upd AS (
		    UPDATE relay_servers
		       SET last_health_check_at = now(),
		           last_health_status   = $2,
		           last_health_error    = NULLIF($3, ''),
		           updated_at           = now()
		     WHERE id = $1
		    RETURNING 1
		)
		SELECT old.last_health_status FROM old JOIN upd ON true
	`, id, status, probeErr).Scan(&prevPtr)
	if err != nil {
		return "", err
	}
	if prevPtr == nil {
		return "unknown", nil
	}
	return *prevPtr, nil
}

// queryRelays is the shared row-scan loop for the ListEnabled /
// ListEligible variants. Kept private — the discriminator lives in
// the SQL WHERE clause the callers compose.
func (r *Relays) queryRelays(ctx context.Context, q string) ([]*RelayServer, error) {
	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*RelayServer
	for rows.Next() {
		var rs RelayServer
		var checkAt *time.Time
		var status, errStr *string
		if err := rows.Scan(
			&rs.ID, &rs.Region, &rs.Hostname, &rs.Port, &rs.PublicKey,
			&rs.Enabled, &rs.CreatedAt, &rs.UpdatedAt,
			&checkAt, &status, &errStr,
		); err != nil {
			return nil, err
		}
		if checkAt != nil {
			rs.LastHealthCheckAt = *checkAt
		}
		if status != nil {
			rs.LastHealthStatus = *status
		}
		if errStr != nil {
			rs.LastHealthError = *errStr
		}
		out = append(out, &rs)
	}
	return out, rows.Err()
}

// SetEnabled toggles a relay's enabled flag. Used by ops scripts to
// drain a relay before maintenance.
func (r *Relays) SetEnabled(ctx context.Context, id uuid.UUID, enabled bool) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE relay_servers
		   SET enabled    = $2,
		       updated_at = now()
		 WHERE id = $1
	`, id, enabled)
	return err
}

// SoftDelete marks a relay as deleted. Existing client sessions are
// not torn down; they remain valid until the relay process exits.
func (r *Relays) SoftDelete(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE relay_servers
		   SET deleted_at = now(),
		       enabled    = false,
		       updated_at = now()
		 WHERE id = $1 AND deleted_at IS NULL
	`, id)
	return err
}
