// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// routeAdminUsersSub is the prefix-handler dispatcher for
// /api/v1/admin/users/*. Today only /erase is wired; future
// admin user surfaces (list, role-flip) plug in alongside.
// Unknown sub-paths surface as 404 so a typo isn't silently
// proxied to a different endpoint via fall-through.
func (h *HTTPServer) routeAdminUsersSub(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/erase") {
		h.routeAdminUserErase(w, r)
		return
	}
	http.NotFound(w, r)
}

// routeAdminUserErase serves
// POST /api/v1/admin/users/{id}/erase — GDPR Article 17
// right-to-erasure for one user in the calling admin's tenant.
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
func (h *HTTPServer) routeAdminUserErase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Path: /api/v1/admin/users/{id}/erase
	idStr, ok := strings.CutPrefix(r.URL.Path, "/api/v1/admin/users/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	idStr, ok = strings.CutSuffix(idStr, "/erase")
	if !ok {
		http.NotFound(w, r)
		return
	}
	targetID, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid user id"))
		return
	}

	authn, err := h.authenticate(r)
	if err != nil || authn == nil || authn.claims == nil {
		writeError(w, http.StatusUnauthorized, errors.New("admin auth required"))
		return
	}
	admin, err := h.users.GetByID(r.Context(), authn.claims.UserID)
	if err != nil || admin == nil || !admin.IsAdmin {
		writeError(w, http.StatusForbidden, errors.New("admin only"))
		return
	}
	if admin.ID == targetID {
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
	if target.TenantID != admin.TenantID {
		// Cross-tenant erasure blocked. Don't reveal whether the
		// user exists in another tenant — 404 is the right shape
		// for "your tenant doesn't have this user."
		writeError(w, http.StatusNotFound, errors.New("user not found"))
		return
	}

	emailHash := sha256.Sum256([]byte(target.Email))
	emailHashHex := hex.EncodeToString(emailHash[:])

	if err := h.users.Erase(r.Context(), targetID); err != nil {
		slog.Warn("user erase", "target", targetID, "admin", admin.ID, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Audit row for the erasure itself. Stored after the DELETE
	// commits so a failed erase doesn't leave a misleading "erased"
	// row in the audit log. Diff carries only the SHA-256 of the
	// email so future auditors can verify a specific subject's
	// erasure without the PII being re-introduced.
	tenantID := admin.TenantID
	resID := targetID
	ev := &repo.AuditEvent{
		TenantID:     &tenantID,
		ActorID:      &admin.ID,
		ActorType:    "user",
		Action:       "user.erase",
		ResourceType: "user",
		ResourceID:   &resID,
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
		"erasedAt":     ev.OccurredAt.UTC(),
	})
}
