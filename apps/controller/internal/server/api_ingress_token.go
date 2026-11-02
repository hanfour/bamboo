// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

// ingressTokenTTL bounds a token stored on the VPS. Rotation (issuing a
// new one) revokes the old one immediately via ingress_tokens.jti.
const ingressTokenTTL = 365 * 24 * time.Hour

var errIngressPeerNotEligible = errors.New("ingress peer must be approved and carry tag:ingress")

type apiIssueIngressTokenReq struct {
	PeerID string `json:"peer_id"`
}

type apiIssueIngressTokenResp struct {
	Token     string    `json:"token"`
	PeerID    string    `json:"peerId"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// apiIssueIngressToken implements POST /api/v1/ingress/token. The token
// is returned once and never stored; only its jti is kept.
func (h *HTTPServer) apiIssueIngressToken(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) {
	if !h.requireAdmin(w, r, authn, tenant, "ingress.token.issue") {
		return
	}
	if len(h.ingressSigningKey) == 0 {
		writeError(w, http.StatusServiceUnavailable, errors.New("public services ingress is not configured (BAMBOO_INGRESS_SIGNING_KEY)"))
		return
	}
	var req apiIssueIngressTokenReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, ingressReqMaxBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	peerID, err := uuid.Parse(req.PeerID)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("peer_id must be a UUID"))
		return
	}
	now := time.Now()
	claims := auth.IngressClaims{
		TenantID: tenant.ID, PeerID: peerID, Role: auth.IngressRole, JTI: uuid.New(),
		IssuedAt: now.Unix(), ExpiresAt: now.Add(ingressTokenTTL).Unix(),
	}
	token, err := auth.IssueIngressToken(h.ingressSigningKey, claims, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ctx := r.Context()
	expires := time.Unix(claims.ExpiresAt, 0).UTC()
	var (
		auditEv *repo.AuditEvent
		auditOK bool
	)
	txErr := db.WithTenant(ctx, h.pool, tenant.ID, func(q db.Querier) error {
		peer, ok, gerr := getPeerInTenant(ctx, q, tenant.ID, peerID)
		if gerr != nil {
			return gerr
		}
		if !ok {
			return errIngressNotFound
		}
		if peer.ApprovalStatus != "approved" || !slices.Contains(peer.Tags, policy.IngressTag) {
			return errIngressPeerNotEligible
		}
		if err := repo.NewIngressTokens(q).Put(ctx, repo.IngressToken{
			TenantID: tenant.ID, PeerID: peerID, JTI: claims.JTI, IssuedBy: ingressActorID(authn), ExpiresAt: expires,
		}); err != nil {
			return err
		}
		// Never put the token itself in the audit diff.
		auditEv, auditOK = writePeerAuditTx(ctx, q, authn, tenant.ID, peerID, "ingress.token.issue", map[string]any{
			"jti": claims.JTI, "expires_at": expires,
		})
		return nil
	})
	switch {
	case errors.Is(txErr, errIngressNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(txErr, errIngressPeerNotEligible):
		writeError(w, http.StatusBadRequest, txErr)
		return
	case txErr != nil:
		writeError(w, http.StatusInternalServerError, txErr)
		return
	}
	h.emitAuditHook(ctx, auditEv, auditOK)
	writeJSON(w, http.StatusCreated, apiIssueIngressTokenResp{Token: token, PeerID: peerID.String(), ExpiresAt: expires})
}
