// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
)

// IngressToken is the tenant's current ingress service token. Only the
// jti is stored; the signed token itself is shown once and never kept.
type IngressToken struct {
	TenantID  uuid.UUID
	PeerID    uuid.UUID
	JTI       uuid.UUID
	IssuedBy  *uuid.UUID
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// IngressTokens is the repository for ingress_tokens.
type IngressTokens struct {
	q db.Querier
}

// NewIngressTokens constructs the repository.
func NewIngressTokens(q db.Querier) *IngressTokens { return &IngressTokens{q: q} }

// Put stores t as the tenant's current token, replacing (revoking) any
// previous one.
func (r *IngressTokens) Put(ctx context.Context, t IngressToken) error {
	_, err := r.q.Exec(ctx, `
		INSERT INTO ingress_tokens (tenant_id, peer_id, jti, issued_by, issued_at, expires_at)
		VALUES ($1, $2, $3, $4, now(), $5)
		ON CONFLICT (tenant_id) DO UPDATE SET
		    peer_id = EXCLUDED.peer_id, jti = EXCLUDED.jti, issued_by = EXCLUDED.issued_by,
		    issued_at = now(), expires_at = EXCLUDED.expires_at`,
		t.TenantID, t.PeerID, t.JTI, t.IssuedBy, t.ExpiresAt)
	if err != nil {
		return fmt.Errorf("put ingress token: %w", err)
	}
	return nil
}

// Get returns the tenant's current token, or ErrNotFound.
func (r *IngressTokens) Get(ctx context.Context, tenantID uuid.UUID) (*IngressToken, error) {
	var t IngressToken
	err := r.q.QueryRow(ctx, `
		SELECT tenant_id, peer_id, jti, issued_by, issued_at, expires_at
		  FROM ingress_tokens WHERE tenant_id = $1`, tenantID).Scan(
		&t.TenantID, &t.PeerID, &t.JTI, &t.IssuedBy, &t.IssuedAt, &t.ExpiresAt)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &t, nil
}
