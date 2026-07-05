// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// /api/v1/relay-token mints a short-lived JWT a peer presents to the
// relay server in CLIENT_HELLO. Authentication: Bearer / cookie /
// X-Tenant-Slug per the standard middleware. The caller supplies its
// own peer_id + wg_public_key so the relay can match them against the
// CLIENT_HELLO payload.
//
// TTL is intentionally short (1 hour). Clients are expected to renew
// alongside their heartbeat cadence so a compromised token is bounded.

const relayTokenTTL = time.Hour

type relayTokenRequest struct {
	PeerID      string `json:"peerId"`
	WGPublicKey string `json:"wireguardPublicKey"`
}

type relayTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (h *HTTPServer) routeRelayToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Peer-session bearer is the preferred credential here: a token
	// minted at Register time, bound to (tenant_id, peer_id,
	// wireguard_public_key). When present we resolve tenant from the
	// token's claim — no X-Tenant-Slug header trust path. We fall
	// through to authenticate() / resolveTenant() so user-session
	// JWTs (admin minting on behalf of a peer) still work today, and
	// so dev environments without prod gates remain usable.
	var (
		tenant      *repo.Tenant
		peerSession *auth.PeerSessionClaims
	)
	tenant, peerSession = h.usePeerSessionTenant(r)
	if tenant == nil {
		authn, err := h.authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		// Prod-mode gate. Without a peer-session bearer the only
		// acceptable credential is a verified user-session JWT —
		// X-Tenant-Slug fallback is rejected. The /api/v1/relay-token
		// route lives outside routeAPI's switch, so the gate must be
		// re-applied here.
		if h.requireAuth && authn.claims == nil {
			writeError(w, http.StatusUnauthorized, errors.New("relay-token requires a peer-session bearer or user-session credential"))
			return
		}
		tenant, err = h.resolveTenant(r, authn)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	_ = peerSession

	var body relayTokenRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode request: %w", err))
		return
	}
	if body.PeerID == "" || body.WGPublicKey == "" {
		writeError(w, http.StatusBadRequest, errors.New("peerId and wireguardPublicKey are required"))
		return
	}
	peerID, err := uuid.Parse(body.PeerID)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid peerId: %w", err))
		return
	}
	// Sanity check: the peer must belong to the tenant we resolved.
	peer, err := h.peers.GetByID(r.Context(), peerID)
	if err != nil || peer == nil || peer.TenantID != tenant.ID {
		writeError(w, http.StatusForbidden, errors.New("peer not in this tenant"))
		return
	}
	if peer.WireGuardPublicKey != body.WGPublicKey {
		writeError(w, http.StatusForbidden, errors.New("public key does not match peer record"))
		return
	}

	exp := time.Now().Add(relayTokenTTL)
	tok, err := auth.IssueRelayToken(h.relayTokenSecret(), auth.RelayClaims{
		TenantID:    tenant.ID,
		PeerID:      peerID,
		WGPublicKey: body.WGPublicKey,
		ExpiresAt:   exp.Unix(),
	}, relayTokenTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, relayTokenResponse{
		Token:     tok,
		ExpiresAt: exp,
	})
}
