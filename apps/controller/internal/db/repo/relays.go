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
type RelayServer struct {
	ID        uuid.UUID
	Region    string
	Hostname  string
	Port      int
	PublicKey string
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Insert creates a new relay server. Returns the persisted row.
func (r *Relays) Insert(ctx context.Context, rs *RelayServer) (*RelayServer, error) {
	var out RelayServer
	err := r.pool.QueryRow(ctx, `
		INSERT INTO relay_servers (region, hostname, port, public_key, enabled)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, region, hostname, port, public_key, enabled, created_at, updated_at
	`, rs.Region, rs.Hostname, rs.Port, rs.PublicKey, rs.Enabled).Scan(
		&out.ID, &out.Region, &out.Hostname, &out.Port, &out.PublicKey,
		&out.Enabled, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListEnabled returns every enabled, non-deleted relay. The controller
// uses this to populate RegisterResponse.relay_servers so every peer
// gets the current list.
func (r *Relays) ListEnabled(ctx context.Context) ([]*RelayServer, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, region, hostname, port, public_key, enabled, created_at, updated_at
		FROM relay_servers
		WHERE enabled = true AND deleted_at IS NULL
		ORDER BY region, hostname
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*RelayServer
	for rows.Next() {
		var rs RelayServer
		if err := rows.Scan(
			&rs.ID, &rs.Region, &rs.Hostname, &rs.Port, &rs.PublicKey,
			&rs.Enabled, &rs.CreatedAt, &rs.UpdatedAt,
		); err != nil {
			return nil, err
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
