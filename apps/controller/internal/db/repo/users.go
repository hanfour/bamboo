// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
)

// Users is the repository for users table.
type Users struct {
	pool *db.Pool
}

// NewUsers constructs a Users repository.
func NewUsers(pool *db.Pool) *Users {
	return &Users{pool: pool}
}

// User is the domain model.
type User struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	Email        string
	DisplayName  string
	OIDCProvider string
	OIDCSubject  string
	IsAdmin      bool
	// SessionVersion is the user's session-revocation generation
	// counter (slice 3b, migration 00017). Auth middleware rejects
	// JWTs whose sv claim is less than this value, so bumping the
	// column invalidates every outstanding session for the user.
	SessionVersion int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// UpsertOIDC creates or updates a user keyed by OIDC identity. Used by the
// login flow when a session is minted.
func (r *Users) UpsertOIDC(ctx context.Context, u *User) (*User, error) {
	var out User
	err := r.pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, display_name, oidc_provider, oidc_subject, is_admin)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, oidc_provider, oidc_subject)
		DO UPDATE SET
		    email        = EXCLUDED.email,
		    display_name = EXCLUDED.display_name,
		    updated_at   = now()
		RETURNING id, tenant_id, email, display_name, oidc_provider, oidc_subject, is_admin, session_version, created_at, updated_at
	`, u.TenantID, u.Email, u.DisplayName, u.OIDCProvider, u.OIDCSubject, u.IsAdmin).Scan(
		&out.ID, &out.TenantID, &out.Email, &out.DisplayName,
		&out.OIDCProvider, &out.OIDCSubject, &out.IsAdmin, &out.SessionVersion,
		&out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetByID returns a user by primary key. Used by the auth middleware
// when resolving a session JWT to a concrete user record.
func (r *Users) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	var u User
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, email, display_name, oidc_provider, oidc_subject, is_admin, session_version, created_at, updated_at
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
	`, id).Scan(
		&u.ID, &u.TenantID, &u.Email, &u.DisplayName,
		&u.OIDCProvider, &u.OIDCSubject, &u.IsAdmin, &u.SessionVersion,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &u, nil
}

// BumpSessionVersion increments session_version for the user and
// returns the new value. Pairs with the auth-middleware check that
// rejects JWTs whose sv claim is below the stored value — one row
// UPDATE nukes every outstanding session for the user.
//
// Idempotent semantics differ from Insert: a caller that bumps
// twice ends up at +2 (callers wanting "force-revoke now and don't
// double-count" should rely on the new return value rather than
// re-issuing the call).
//
// updated_at is deliberately NOT touched. ListByTenant orders by
// updated_at DESC as the "recently active" sort; a force-sign-out
// is the opposite of activity, so bumping it would push the just-
// signed-out user to the top of the admin Users page — a SOC 2
// incident-response footgun. The session.revoke_all audit row is
// the durable record of when the bump happened.
func (r *Users) BumpSessionVersion(ctx context.Context, userID uuid.UUID) (int, error) {
	var next int
	err := r.pool.QueryRow(ctx, `
		UPDATE users
		   SET session_version = session_version + 1
		 WHERE id = $1 AND deleted_at IS NULL
		RETURNING session_version
	`, userID).Scan(&next)
	if err != nil {
		return 0, asNotFound(err)
	}
	return next, nil
}

// ListByTenant returns every active (non-deleted) user in the tenant,
// most recently active first. updated_at is bumped on every UpsertOIDC
// call, so ordering by it yields a "recently logged in" sort that
// works well for the Users admin page even without a dedicated
// last_seen_at column.
func (r *Users) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*User, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, email, display_name, oidc_provider, oidc_subject, is_admin, session_version, created_at, updated_at
		FROM users
		WHERE tenant_id = $1 AND deleted_at IS NULL
		ORDER BY updated_at DESC
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		var u User
		if err := rows.Scan(
			&u.ID, &u.TenantID, &u.Email, &u.DisplayName,
			&u.OIDCProvider, &u.OIDCSubject, &u.IsAdmin, &u.SessionVersion,
			&u.CreatedAt, &u.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, &u)
	}
	return out, rows.Err()
}

// Erase performs a GDPR Article 17 right-to-erasure on the
// target user (§4 P2 compliance). In one transaction:
//
//  1. Fetch the user's email so we can scrub invitations
//     addressed to the same email below.
//  2. Redact user_invitations matching that email (set to a
//     deterministic placeholder so re-erasure of the same
//     email is idempotent and the partial-unique-index on
//     pending invites isn't blocked).
//  3. Hard-DELETE the user row. Cascade FKs handle the rest:
//     peers.user_id → NULL (peer survives, owner shown as "—"),
//     pre_auth_keys.created_by → NULL, acl_policies.applied_by
//     → NULL, user_invitations.invited_by/accepted_by → NULL,
//     user_group_members → CASCADE delete.
//
// audit_log.actor_id has no FK to users (per the 00001 schema
// comment "not FK so deleted users still appear"), so the
// LEFT JOIN in ListByTenant naturally returns empty email for
// historical events authored by the erased user — exactly the
// GDPR-compliant rendering.
//
// Returns ErrNotFound when no row matches, including the
// already-erased case (idempotent — caller treats both the same).
//
// Audit-log entry for the erasure itself is the CALLER's
// responsibility: this function runs the data plane; the
// admin handler writes "user.erase" with the actor=admin so
// the audit trail records WHO erased WHOM, by uuid (the
// erased user's email is hashed in the audit diff so the
// row stays verifiable without re-introducing the PII).
func (r *Users) Erase(ctx context.Context, userID uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var email string
	var tenantID uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT email, tenant_id FROM users WHERE id = $1 AND deleted_at IS NULL`,
		userID,
	).Scan(&email, &tenantID); err != nil {
		return asNotFound(err)
	}

	// Redact invitations addressed to this email. Use a fixed
	// placeholder so a second erasure of the same email (e.g.
	// they re-invited + immediately re-erased) doesn't violate
	// the partial-unique-index on (tenant_id, lower(email))
	// WHERE pending — the placeholder collapses any later rows
	// into one, and pending invites to an erased email can't
	// coexist in practice anyway (they'd reference a user row
	// that's about to be deleted).
	//
	// The WHERE clause has two important guards:
	//   - tenant_id = $2 confines the scrub to the erased user's
	//     own tenant. The same email can legitimately exist in
	//     other tenants (multi-tenant admin emails like
	//     ops@example.com), and an Article-17 erasure in tenant A
	//     must not silently mutate rows in tenants B/C.
	//   - lower(email) matches the canonical key used by
	//     migration 00007's partial-unique-index and the OIDC
	//     redeem path's strings.EqualFold (see handleCallback).
	//     A mixed-case row like "Han@example.com" would otherwise
	//     survive an erasure of the OIDC-normalised "han@example.com"
	//     users row, leaving PII behind.
	if _, err := tx.Exec(ctx,
		`UPDATE user_invitations
		    SET email = '<erased>'
		  WHERE tenant_id = $1 AND lower(email) = lower($2)`,
		tenantID, email,
	); err != nil {
		return err
	}

	// The DELETE is what GDPR Article 17 actually requires.
	// Cascade FKs (declared in 00001) handle the dependent rows.
	if _, err := tx.Exec(ctx,
		`DELETE FROM users WHERE id = $1`,
		userID,
	); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// GetByEmail returns a user within the given tenant by email.
func (r *Users) GetByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*User, error) {
	var u User
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, email, display_name, oidc_provider, oidc_subject, is_admin, session_version, created_at, updated_at
		FROM users
		WHERE tenant_id = $1 AND email = $2 AND deleted_at IS NULL
	`, tenantID, email).Scan(
		&u.ID, &u.TenantID, &u.Email, &u.DisplayName,
		&u.OIDCProvider, &u.OIDCSubject, &u.IsAdmin, &u.SessionVersion,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &u, nil
}
