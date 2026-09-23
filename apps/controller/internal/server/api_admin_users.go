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
	"github.com/hanfour/bamboo/apps/controller/internal/db"
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
	var actor *repo.User
	err = db.WithTenant(r.Context(), h.pool, authn.claims.TenantID, func(q db.Querier) error {
		u, gerr := repo.NewUsers(q).GetByID(r.Context(), authn.claims.UserID)
		if gerr != nil {
			return gerr
		}
		actor = u
		return nil
	})
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
	var (
		target   *repo.User
		next     int
		notFound bool
		auditEv  *repo.AuditEvent
		auditOK  bool
	)
	if txErr := db.WithTenant(r.Context(), h.pool, actor.TenantID, func(q db.Querier) error {
		users := repo.NewUsers(q)
		t, err := users.GetByID(r.Context(), targetID)
		if errors.Is(err, repo.ErrNotFound) {
			notFound = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("resolve user: %w", err)
		}
		if t.TenantID != actor.TenantID {
			// Tenant boundary — do not leak that this id exists.
			notFound = true
			return nil
		}
		target = t
		n, err := users.BumpSessionVersion(r.Context(), targetID)
		if err != nil {
			return fmt.Errorf("bump session_version: %w", err)
		}
		next = n
		auditEv, auditOK = auditSessionRevokeAll(r.Context(), q, actor.TenantID, actor.ID, t.ID, t.Email, t.SessionVersion, n, requestIPString(r), r.UserAgent())
		return nil
	}); txErr != nil {
		writeError(w, http.StatusInternalServerError, txErr)
		return
	}
	if notFound {
		http.NotFound(w, r)
		return
	}
	h.emitAuditHook(r.Context(), auditEv, auditOK)

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

	// Capture erasedAt before the tx so the response and the audit
	// row share one timestamp. The audit insert is a savepoint: a
	// failed audit does not roll back the DELETE, and a failed
	// DELETE never commits the audit row.
	erasedAt := time.Now().UTC()
	var (
		notFound bool
		auditEv  *repo.AuditEvent
		auditOK  bool
	)
	if txErr := db.WithTenant(r.Context(), h.pool, actor.TenantID, func(q db.Querier) error {
		users := repo.NewUsers(q)
		target, err := users.GetByID(r.Context(), targetID)
		if errors.Is(err, repo.ErrNotFound) || (err == nil && target == nil) {
			// Already-erased or never-existed both surface as 404 so a
			// retry doesn't get a weird 5xx. A real DB error is returned
			// so we don't Commit an aborted transaction.
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		if target.TenantID != actor.TenantID {
			// Cross-tenant erasure blocked. Don't reveal whether the
			// user exists in another tenant.
			notFound = true
			return nil
		}
		emailHash := sha256.Sum256([]byte(target.Email))
		emailHashHex := hex.EncodeToString(emailHash[:])
		if err := users.Erase(r.Context(), targetID); err != nil {
			// Racy concurrent erase: another admin deleted the row
			// between GetByID and the in-tx SELECT inside Erase.
			if errors.Is(err, repo.ErrNotFound) {
				notFound = true
				return nil
			}
			return err
		}
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
			Diff: marshalDiffJSON(map[string]any{
				"targetEmailSHA256": emailHashHex,
			}),
		}
		auditEv = ev
		auditOK = insertAuditTx(r.Context(), q, ev)
		return nil
	}); txErr != nil {
		slog.Warn("user erase", "target", targetID, "admin", actor.ID, "err", txErr)
		writeError(w, http.StatusInternalServerError, txErr)
		return
	}
	if notFound {
		writeError(w, http.StatusNotFound, errors.New("user not found"))
		return
	}
	h.emitAuditHook(r.Context(), auditEv, auditOK)

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
func auditSessionRevokeAll(ctx context.Context, q db.Querier, tenantID, actorID, targetID uuid.UUID, targetEmail string, oldVersion, newVersion int, ip, userAgent string) (*repo.AuditEvent, bool) {
	diff, _ := json.Marshal(map[string]any{
		"targetEmail":       targetEmail,
		"oldSessionVersion": oldVersion,
		"newSessionVersion": newVersion,
	})
	tid, aid, rid := tenantID, actorID, targetID
	ev := &repo.AuditEvent{
		TenantID:     &tid,
		ActorType:    "user",
		ActorID:      &aid,
		Action:       "session.revoke_all",
		ResourceType: "user",
		ResourceID:   &rid,
		Diff:         diff,
	}
	if ip != "" {
		ev.IPAddress = &ip
	}
	if userAgent != "" {
		ev.UserAgent = &userAgent
	}
	return ev, insertAuditTx(ctx, q, ev)
}
