// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// routeAdminUsers is the prefix handler for /api/v1/admin/users/*
// sub-routes. Each {id}/{action} pair is dispatched to a small
// helper; admin gating + tenant scope live up here so every action
// gets the same checks.
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
	if err != nil {
		// Separate the DB-error path from the non-admin path so a
		// transient PG blip doesn't look identical to a real
		// authorization denial. Mirrors requireAdmin in api.go.
		writeError(w, http.StatusInternalServerError, fmt.Errorf("resolve actor: %w", err))
		return
	}
	if !actor.IsAdmin {
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
	case "erase":
		h.adminUserErase(w, r, actor, targetID)
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
	auditSessionRevokeAll(r.Context(), h.audits, actor.TenantID, actor.ID, target.ID, target.Email, target.SessionVersion, next, requestIPString(r), r.UserAgent())

	writeJSON(w, http.StatusOK, map[string]any{
		"userId":         target.ID.String(),
		"sessionVersion": next,
	})
}

// adminUserErase serves POST /api/v1/admin/users/{id}/erase —
// GDPR Article 17 right-to-erasure for one user in the calling
// admin's tenant.
//
// Wire shape:
//
//	POST /api/v1/admin/users/{user-uuid}/erase
//	→ 200 {"erasedUserId": "<uuid>", "erasedAt": "<rfc3339>"}
//
// Semantics:
//   - Tenant-admin within own tenant. Cross-tenant erasure (super-
//     admin) is a follow-up; v1 limits blast radius to the admin's
//     own tenant.
//   - Hard-DELETE of the user row. Cascade FKs from 00001 handle:
//     peers.user_id → NULL (peer keeps serving; "—" owner badge),
//     pre_auth_keys.created_by → NULL, acl_policies.applied_by →
//     NULL, user_invitations.{invited_by,accepted_by,revoked_by}
//     → NULL, user_group_members → CASCADE delete.
//   - user_invitations.email separately redacted in same tx (no
//     FK on email; the redact closes the obvious PII leak).
//   - audit_log.actor_id has no FK so historical events authored
//     by this user keep their actor_id but the ListByTenant
//     LEFT JOIN returns empty email — the row records "someone
//     did this" without revealing whom, which IS the GDPR-
//     compliant rendering.
//   - Audit row for the erasure itself: actor = admin, target =
//     erased UUID, diff = {targetEmailSHA256}. The hash lets a
//     future auditor verify "was this email erased" without
//     re-introducing the plaintext PII.
//   - Idempotent: re-erasing an already-erased user returns 404
//     (the row is gone) rather than a 5xx, so a retried request
//     after a network blip surfaces predictably.
//
// Self-erasure: admin cannot erase their own user row. Otherwise
// the next request would 401 + the admin loses the ability to
// audit the erasure. Erase-yourself flows for non-admins are out
// of scope until we have a non-admin self-service surface.
func (h *HTTPServer) adminUserErase(w http.ResponseWriter, r *http.Request, actor *repo.User, targetID uuid.UUID) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if actor.ID == targetID {
		// Self-erase blocked — see route comment.
		writeError(w, http.StatusBadRequest, errors.New("admin cannot erase their own account; ask another admin"))
		return
	}

	target, err := h.users.GetByID(r.Context(), targetID)
	if err != nil || target == nil {
		// Already-erased or never-existed both surface as 404 so a
		// retry doesn't get a weird 5xx.
		writeError(w, http.StatusNotFound, errors.New("user not found"))
		return
	}
	if target.TenantID != actor.TenantID {
		// Cross-tenant erasure blocked. Don't reveal whether the
		// user exists in another tenant — 404 is the right shape
		// for "your tenant doesn't have this user."
		writeError(w, http.StatusNotFound, errors.New("user not found"))
		return
	}

	emailHash := sha256.Sum256([]byte(target.Email))
	emailHashHex := hex.EncodeToString(emailHash[:])

	if err := h.users.Erase(r.Context(), targetID); err != nil {
		// Racy concurrent erase: another admin (or our own
		// retry-after-network-blip) deleted the row between the
		// GetByID above and the in-tx SELECT inside Erase. The
		// docstring promises idempotent 404 on already-erased,
		// so surface that rather than a misleading 500.
		if errors.Is(err, repo.ErrNotFound) {
			writeError(w, http.StatusNotFound, errors.New("user not found"))
			return
		}
		slog.Warn("user erase", "target", targetID, "admin", actor.ID, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Capture the wall-clock erasure time once and pin it on the
	// AuditEvent. AuditLogs.Insert (since #222 follow-up) persists
	// a non-zero OccurredAt verbatim instead of letting the DB
	// default to `now()`, so the response's erasedAt and the
	// stored audit row's occurred_at are byte-exactly equal —
	// auditors can join the receipt to the audit row by timestamp.
	erasedAt := time.Now().UTC()

	// Audit row for the erasure itself. Stored after the DELETE
	// commits so a failed erase doesn't leave a misleading "erased"
	// row in the audit log. Diff carries only the SHA-256 of the
	// email so future auditors can verify a specific subject's
	// erasure without the PII being re-introduced.
	tenantID := actor.TenantID
	resID := targetID
	ev := &repo.AuditEvent{
		TenantID:     &tenantID,
		ActorID:      &actor.ID,
		ActorType:    "user",
		Action:       "user.erase",
		ResourceType: "user",
		ResourceID:   &resID,
		OccurredAt:   erasedAt,
	}
	ev.Diff = marshalDiffJSON(map[string]any{
		"targetEmailSHA256": emailHashHex,
	})
	if err := h.audits.Insert(r.Context(), ev); err != nil {
		// The erasure already committed; audit failure is a
		// logged-but-tolerated case (same policy as every other
		// audit insert path).
		slog.Warn("user erase: audit insert", "target", targetID, "err", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"erasedUserId": targetID.String(),
		"erasedAt":     erasedAt,
	})
}

// auditSessionRevokeAll writes the audit row for an admin-driven
// force-sign-out of a user. Actor = the admin who pressed the
// button; resource = the targeted user. Diff carries the
// pre-bump + post-bump session_version (so a single audit row
// reads "from N to N+1" without cross-referencing earlier rows)
// plus the target email for human-friendly searches.
func auditSessionRevokeAll(ctx context.Context, audits *repo.AuditLogs, tenantID, actorID, targetID uuid.UUID, targetEmail string, oldVersion, newVersion int, ip, userAgent string) {
	if audits == nil {
		return
	}
	diff, _ := json.Marshal(map[string]any{
		"targetEmail":       targetEmail,
		"oldSessionVersion": oldVersion,
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
