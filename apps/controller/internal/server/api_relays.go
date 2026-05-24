// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// REST endpoints for the relay-server registry. All paths are under
// /api/v1/admin/relays — the existing routeAPI / authenticate stack
// handles credential validation; this file adds the admin-only
// authorization on top.
//
// Reads (GET): require any authenticated user. Writes (POST / DELETE):
// require an authenticated user with is_admin=true. Tenants do not
// manage their own relays in v1 — relays are shared infrastructure.

type relayJSON struct {
	ID        string `json:"id"`
	Region    string `json:"region"`
	Hostname  string `json:"hostname"`
	Port      int    `json:"port"`
	PublicKey string `json:"publicKey"`
	Enabled   bool   `json:"enabled"`
	// Health fields (§4 P2 multi-relay registry, stage 1).
	// LastHealthStatus is one of "unknown" / "healthy" / "unhealthy".
	// Empty when the row has never been probed (rolled-up to "unknown"
	// on the wire so the admin UI doesn't have to special-case nil).
	// LastHealthError is the short reason text the reaper recorded
	// on the most recent failure; empty when status is not 'unhealthy'.
	// LastHealthCheckAt is the timestamp of the most recent probe;
	// zero ⇒ never probed.
	LastHealthStatus  string `json:"lastHealthStatus,omitempty"`
	LastHealthError   string `json:"lastHealthError,omitempty"`
	LastHealthCheckAt string `json:"lastHealthCheckAt,omitempty"`
}

// relayJSONFromRepo flattens a repo.RelayServer into the admin REST
// wire shape, normalising the "never-probed" case to "unknown" so
// downstream renderers don't have to.
func relayJSONFromRepo(rs *repo.RelayServer) relayJSON {
	status := rs.LastHealthStatus
	if status == "" {
		status = "unknown"
	}
	out := relayJSON{
		ID:               rs.ID.String(),
		Region:           rs.Region,
		Hostname:         rs.Hostname,
		Port:             rs.Port,
		PublicKey:        rs.PublicKey,
		Enabled:          rs.Enabled,
		LastHealthStatus: status,
		LastHealthError:  rs.LastHealthError,
	}
	if !rs.LastHealthCheckAt.IsZero() {
		out.LastHealthCheckAt = rs.LastHealthCheckAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

type relayCreateRequest struct {
	Region    string `json:"region"`
	Hostname  string `json:"hostname"`
	Port      int    `json:"port"`
	PublicKey string `json:"publicKey"`
}

func (h *HTTPServer) routeAdminRelays(w http.ResponseWriter, r *http.Request) {
	authn, err := h.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	// Admin-only.
	if authn == nil || authn.claims == nil {
		writeError(w, http.StatusUnauthorized, errors.New("admin auth required"))
		return
	}
	user, err := h.users.GetByID(r.Context(), authn.claims.UserID)
	if err != nil || user == nil || !user.IsAdmin {
		writeError(w, http.StatusForbidden, errors.New("admin only"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.adminRelaysList(w, r)
	case http.MethodPost:
		h.adminRelaysCreate(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *HTTPServer) adminRelaysList(w http.ResponseWriter, r *http.Request) {
	relays, err := h.relays.ListEnabled(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]relayJSON, 0, len(relays))
	for _, rs := range relays {
		out = append(out, relayJSONFromRepo(rs))
	}
	writeJSON(w, http.StatusOK, map[string]any{"relays": out})
}

func (h *HTTPServer) adminRelaysCreate(w http.ResponseWriter, r *http.Request) {
	var body relayCreateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode request: %w", err))
		return
	}
	if body.Region == "" || body.Hostname == "" || body.PublicKey == "" {
		writeError(w, http.StatusBadRequest, errors.New("region, hostname, and publicKey are required"))
		return
	}
	if body.Port < 1 || body.Port > 65535 {
		writeError(w, http.StatusBadRequest, errors.New("port must be between 1 and 65535"))
		return
	}
	rs, err := h.relays.Insert(r.Context(), &repo.RelayServer{
		Region:    body.Region,
		Hostname:  body.Hostname,
		Port:      body.Port,
		PublicKey: body.PublicKey,
		Enabled:   true,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, relayJSONFromRepo(rs))
}
