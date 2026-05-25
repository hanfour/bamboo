// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
)

// RevokedSessions is the repository for the session revocation
// denylist (migration 00016). A row pins a single JWT (by its jti
// claim) as invalid before its natural exp; the auth middleware
// consults this on every authenticated request.
type RevokedSessions struct {
	pool *db.Pool
}

// NewRevokedSessions constructs the repository.
func NewRevokedSessions(pool *db.Pool) *RevokedSessions {
	return &RevokedSessions{pool: pool}
}

// Insert records that jti is revoked, capturing the JWT's natural
// expiry so the reaper can prune the row once it's redundant. Safe
// to call twice with the same jti — duplicate inserts are coalesced
// via ON CONFLICT DO NOTHING so a double sign-out doesn't error.
func (r *RevokedSessions) Insert(ctx context.Context, jti, userID uuid.UUID, expiresAt time.Time) error {
	if jti == uuid.Nil {
		return fmt.Errorf("revoked_sessions.Insert: zero jti")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO revoked_sessions (jti, user_id, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (jti) DO NOTHING
	`, jti, userID, expiresAt)
	if err != nil {
		return fmt.Errorf("insert revoked_session: %w", err)
	}
	return nil
}

// IsRevoked returns true when jti has been marked revoked. The auth
// middleware calls this on every request that carries a JWT with a
// non-zero jti — small UUID PK lookup against an indexed table,
// same order of magnitude as the existing users.GetByID hop.
func (r *RevokedSessions) IsRevoked(ctx context.Context, jti uuid.UUID) (bool, error) {
	if jti == uuid.Nil {
		return false, nil
	}
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM revoked_sessions WHERE jti = $1)
	`, jti).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("revoked_sessions exists: %w", err)
	}
	return exists, nil
}

// DeleteExpired drops rows whose JWT has already passed its natural
// exp — the JWT verifier rejects those tokens on its own (exp
// check), so keeping the revocation row is wasted space. Called by
// the hourly reaper.
func (r *RevokedSessions) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM revoked_sessions
		 WHERE expires_at < $1
	`, now)
	if err != nil {
		return 0, fmt.Errorf("delete expired revoked_sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
