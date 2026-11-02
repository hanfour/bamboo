// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/jackc/pgx/v5/pgconn"
)

// IngressService is one ingress_services row joined with the peer fields
// liveness and the admin list need. See
// docs/design/2026-11-02-public-services-ingress.md §4.1.
type IngressService struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	PeerID     uuid.UUID
	Label      string
	TargetPort int
	Enabled    bool
	ApprovedBy *uuid.UUID // nil = pending
	ApprovedAt *time.Time
	CreatedBy  *uuid.UUID
	CreatedAt  time.Time
	UpdatedAt  time.Time

	PeerHostname       string
	PeerIP             string
	PeerApprovalStatus string
	PeerStatus         string // online / offline / disabled
}

// Approved reports whether an admin has approved the service.
func (s *IngressService) Approved() bool { return s.ApprovedBy != nil }

// Live reports whether the public name should route: enabled, approved,
// and on an approved peer that is not disabled. A deleted peer cascades the row away; a nil
// service (deleted) is not live.
func (s *IngressService) Live() bool {
	if s == nil {
		return false // deleted
	}
	return s.Enabled && s.ApprovedBy != nil && s.PeerApprovalStatus == "approved" && s.PeerStatus != "disabled"
}

// IngressRoute is one live service as the ingress proxy and the ACL
// generator see it.
type IngressRoute struct {
	ServiceID  uuid.UUID
	Label      string
	PeerID     uuid.UUID
	PeerIP     string
	TargetPort int
}

// Validation and uniqueness errors returned by the ingress service repo.
var (
	// ErrIngressLabelTaken means another service (in any tenant) already uses the label.
	ErrIngressLabelTaken = errors.New("ingress label is already in use")
	// ErrIngressLabelInvalid means the label does not match the allowed pattern.
	ErrIngressLabelInvalid = errors.New("label must be 1-32 lowercase letters, digits, or inner hyphens")
	// ErrIngressLabelReserved means the label is on the reserved list.
	ErrIngressLabelReserved = errors.New("label is reserved")
	// ErrIngressPortInvalid means target_port is outside 1024-65535.
	ErrIngressPortInvalid = errors.New("target_port must be between 1024 and 65535")
)

var ingressLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

var ingressReservedLabels = map[string]struct{}{
	"www": {}, "api": {}, "admin": {}, "relay": {}, "controller": {}, "ingress": {}, "mail": {},
}

// ValidateIngressLabel mirrors the migration's CHECK constraints so the
// REST layer can answer 400 before touching the database.
func ValidateIngressLabel(label string) error {
	if !ingressLabelRE.MatchString(label) {
		return ErrIngressLabelInvalid
	}
	if _, reserved := ingressReservedLabels[label]; reserved {
		return ErrIngressLabelReserved
	}
	return nil
}

// ValidateIngressPort mirrors ingress_services_port_range.
func ValidateIngressPort(port int) error {
	if port < 1024 || port > 65535 {
		return ErrIngressPortInvalid
	}
	return nil
}

// IngressServices is the repository for ingress_services.
type IngressServices struct {
	q db.Querier
}

// NewIngressServices constructs the repository.
func NewIngressServices(q db.Querier) *IngressServices { return &IngressServices{q: q} }

const ingressSelect = `
	SELECT s.id, s.tenant_id, s.peer_id, s.label, s.target_port, s.enabled,
	       s.approved_by, s.approved_at, s.created_by, s.created_at, s.updated_at,
	       p.hostname, host(p.ip), p.approval_status, p.status
	  FROM ingress_services s
	  JOIN peers p ON p.id = s.peer_id`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanIngress(row rowScanner) (*IngressService, error) {
	var s IngressService
	err := row.Scan(
		&s.ID, &s.TenantID, &s.PeerID, &s.Label, &s.TargetPort, &s.Enabled,
		&s.ApprovedBy, &s.ApprovedAt, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt,
		&s.PeerHostname, &s.PeerIP, &s.PeerApprovalStatus, &s.PeerStatus,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Create inserts a pending, disabled service and returns it joined with
// its peer. A label used by any tenant yields ErrIngressLabelTaken; the
// enclosing transaction is then aborted, so callers return the error.
func (r *IngressServices) Create(ctx context.Context, tenantID, peerID uuid.UUID, label string, targetPort int, createdBy *uuid.UUID) (*IngressService, error) {
	if err := ValidateIngressLabel(label); err != nil {
		return nil, err
	}
	if err := ValidateIngressPort(targetPort); err != nil {
		return nil, err
	}
	var id uuid.UUID
	err := r.q.QueryRow(ctx, `
		INSERT INTO ingress_services (tenant_id, peer_id, label, target_port, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`, tenantID, peerID, label, targetPort, createdBy).Scan(&id)
	if isUniqueViolation(err, "ingress_services_label_unique") {
		return nil, ErrIngressLabelTaken
	}
	if err != nil {
		return nil, fmt.Errorf("insert ingress service: %w", err)
	}
	return r.GetByID(ctx, id)
}

// GetByID returns one service, or ErrNotFound (also when RLS hides it).
func (r *IngressServices) GetByID(ctx context.Context, id uuid.UUID) (*IngressService, error) {
	s, err := scanIngress(r.q.QueryRow(ctx, ingressSelect+` WHERE s.id = $1`, id))
	if err != nil {
		return nil, asNotFound(err)
	}
	return s, nil
}

// ListByTenant returns the tenant's services ordered by label.
func (r *IngressServices) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*IngressService, error) {
	rows, err := r.q.Query(ctx, ingressSelect+` WHERE s.tenant_id = $1 ORDER BY s.label`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list ingress services: %w", err)
	}
	defer rows.Close()
	out := []*IngressService{}
	for rows.Next() {
		s, err := scanIngress(rows)
		if err != nil {
			return nil, fmt.Errorf("scan ingress service: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ExistsForPeer reports whether the peer hosts any service, in any
// state. Peer approval changes and deletion bump the policy revision
// when it does (ON DELETE CASCADE removes the rows with the peer, so
// callers ask before deleting).
func (r *IngressServices) ExistsForPeer(ctx context.Context, peerID uuid.UUID) (bool, error) {
	var exists bool
	if err := r.q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ingress_services WHERE peer_id = $1)`, peerID).Scan(&exists); err != nil {
		return false, fmt.Errorf("ingress services for peer: %w", err)
	}
	return exists, nil
}

// EnabledApprovedForPeer reports whether the peer hosts a service that
// is enabled and approved, i.e. one whose liveness follows the peer's own
// state (approval, disabled).
func (r *IngressServices) EnabledApprovedForPeer(ctx context.Context, peerID uuid.UUID) (bool, error) {
	var exists bool
	if err := r.q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ingress_services
		WHERE peer_id = $1 AND enabled AND approved_by IS NOT NULL)`, peerID).Scan(&exists); err != nil {
		return false, fmt.Errorf("enabled ingress services for peer: %w", err)
	}
	return exists, nil
}

// Approve records the approver. Returns rows affected.
func (r *IngressServices) Approve(ctx context.Context, id, approver uuid.UUID) (int64, error) {
	return r.exec(ctx, `UPDATE ingress_services
		SET approved_by = $2, approved_at = now(), updated_at = now() WHERE id = $1`, id, approver)
}

// SetEnabled flips enabled. Returns rows affected.
func (r *IngressServices) SetEnabled(ctx context.Context, id uuid.UUID, enabled bool) (int64, error) {
	return r.exec(ctx, `UPDATE ingress_services
		SET enabled = $2, updated_at = now() WHERE id = $1`, id, enabled)
}

// SetTargetPort changes the port and resets approval (spec §4.2).
func (r *IngressServices) SetTargetPort(ctx context.Context, id uuid.UUID, port int) (int64, error) {
	if err := ValidateIngressPort(port); err != nil {
		return 0, err
	}
	return r.exec(ctx, `UPDATE ingress_services
		SET target_port = $2, approved_by = NULL, approved_at = NULL, updated_at = now()
		WHERE id = $1`, id, port)
}

// Delete removes the row. Returns rows affected.
func (r *IngressServices) Delete(ctx context.Context, id uuid.UUID) (int64, error) {
	return r.exec(ctx, `DELETE FROM ingress_services WHERE id = $1`, id)
}

func (r *IngressServices) exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := r.q.Exec(ctx, sql, args...)
	if err != nil {
		return 0, fmt.Errorf("ingress services: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListLiveRoutes returns the tenant's live services. The WHERE clause is
// the same liveness rule as IngressService.Live and ingress_label_live.
func (r *IngressServices) ListLiveRoutes(ctx context.Context, tenantID uuid.UUID) ([]IngressRoute, error) {
	rows, err := r.q.Query(ctx, `
		SELECT s.id, s.label, s.peer_id, host(p.ip), s.target_port
		  FROM ingress_services s
		  JOIN peers p ON p.id = s.peer_id
		  JOIN tenants t ON t.id = s.tenant_id AND t.deleted_at IS NULL
		 WHERE s.tenant_id = $1
		   AND s.enabled AND s.approved_by IS NOT NULL
		   AND p.approval_status = 'approved'
		   AND p.status <> 'disabled'
		 ORDER BY s.label`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list live ingress routes: %w", err)
	}
	defer rows.Close()
	out := []IngressRoute{}
	for rows.Next() {
		var rt IngressRoute
		if err := rows.Scan(&rt.ServiceID, &rt.Label, &rt.PeerID, &rt.PeerIP, &rt.TargetPort); err != nil {
			return nil, fmt.Errorf("scan live route: %w", err)
		}
		out = append(out, rt)
	}
	return out, rows.Err()
}

// LabelTaken asks the SECURITY DEFINER bootstrap function whether any
// tenant uses label. RLS bootstrap: safe without a tenant GUC.
func (r *IngressServices) LabelTaken(ctx context.Context, label string) (bool, error) {
	var taken bool
	if err := r.q.QueryRow(ctx, `SELECT ingress_label_taken($1)`, label).Scan(&taken); err != nil {
		return false, fmt.Errorf("ingress_label_taken: %w", err)
	}
	return taken, nil
}

// LabelLive asks the SECURITY DEFINER bootstrap function whether label is
// live in any tenant. RLS bootstrap: used by Caddy's on-demand TLS ask.
func (r *IngressServices) LabelLive(ctx context.Context, label string) (bool, error) {
	var live bool
	if err := r.q.QueryRow(ctx, `SELECT ingress_label_live($1)`, label).Scan(&live); err != nil {
		return false, fmt.Errorf("ingress_label_live: %w", err)
	}
	return live, nil
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
