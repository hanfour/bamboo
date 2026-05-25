// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// routeAdminUsers is the prefix handler for /api/v1/admin/users/*
// sub-routes. Currently only /sign-out-all is wired (slice 3b);
// the same dispatcher slot is the natural home for future admin-
// scoped user mutations (force role change, force tag refresh,
// etc.).
//
// Auth: every action under this prefix is admin-only. Tenant scope
// comes from the caller's own JWT — an admin cannot reach into
// users belonging to a different tenant (cross-tenant requests
// return 404 to avoid leaking existence).
func (h *HTTPServer) routeAdminUsers(w http.ResponseWriter, r *http.Request) {
	authn, err := h.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if authn == nil || authn.claims == nil {
		writeError(w, http.StatusUnauthorized, errors.New("admin auth required"))
		return
	}
	actor, err := h.users.GetByID(r.Context(), authn.claims.UserID)
	if err != nil || actor == nil || !actor.IsAdmin {
		writeError(w, http.StatusForbidden, errors.New("admin only"))
		return
	}

	// path layout: /api/v1/admin/users/{id}/{action}
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	targetID, parseErr := uuid.Parse(parts[0])
	if parseErr != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid user id"))
		return
	}
	action := parts[1]
	switch action {
	case "sign-out-all":
		h.adminUserSignOutAll(w, r, actor, targetID)
	default:
		http.NotFound(w, r)
	}
}

// adminUserSignOutAll is the slice-3b force-sign-out endpoint.
// POST /api/v1/admin/users/{id}/sign-out-all bumps the target's
// users.session_version, which the REST + gRPC auth middlewares
// compare against the claims.sv on every request. The next time
// any of the user's outstanding JWTs is presented, the middleware
// rejects with "session revoked (force sign-out)".
//
// Cross-tenant requests return 404 (not 403) so an admin can't
// probe foreign tenants for user-id existence.
//
// The actor may bump themselves — useful when the admin wants to
// invalidate sessions on devices they no longer control. Their
// current session is killed too; they will be signed out on the
// next request and have to re-authenticate.
func (h *HTTPServer) adminUserSignOutAll(w http.ResponseWriter, r *http.Request, actor *repo.User, targetID uuid.UUID) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	target, err := h.users.GetByID(r.Context(), targetID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Errorf("resolve user: %w", err))
		return
	}
	if target.TenantID != actor.TenantID {
		// Tenant boundary — do not leak that this id exists.
		http.NotFound(w, r)
		return
	}

	next, err := h.users.BumpSessionVersion(r.Context(), targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("bump session_version: %w", err))
		return
	}
	auditSessionRevokeAll(r.Context(), h.audits, actor.TenantID, actor.ID, target.ID, target.Email, next, requestIPString(r), r.UserAgent())

	writeJSON(w, http.StatusOK, map[string]any{
		"userId":         target.ID.String(),
		"sessionVersion": next,
	})
}

// auditSessionRevokeAll writes the audit row for an admin-driven
// force-sign-out of a user. Actor = the admin who pressed the
// button; resource = the targeted user. Diff carries the bumped
// session_version (so log-only readers see "from N to N+1") plus
// the target email for human-friendly searches.
func auditSessionRevokeAll(ctx context.Context, audits *repo.AuditLogs, tenantID, actorID, targetID uuid.UUID, targetEmail string, newVersion int, ip, userAgent string) {
	if audits == nil {
		return
	}
	diff, _ := json.Marshal(map[string]any{
		"targetEmail":       targetEmail,
		"newSessionVersion": newVersion,
	})
	ev := &repo.AuditEvent{
		TenantID:     &tenantID,
		ActorType:    "user",
		ActorID:      &actorID,
		Action:       "session.revoke_all",
		ResourceType: "user",
		ResourceID:   &targetID,
		Diff:         diff,
	}
	if ip != "" {
		ev.IPAddress = &ip
	}
	if userAgent != "" {
		ev.UserAgent = &userAgent
	}
	if err := audits.Insert(ctx, ev); err != nil {
		slog.Warn("audit session.revoke_all", "err", err, "target_id", targetID)
	}
}
