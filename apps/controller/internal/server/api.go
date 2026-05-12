// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
	"github.com/hanfour/bamboo/apps/controller/internal/policy/recommend"
)

// routeAPI dispatches /api/v1/* to JSON endpoints.
//
// Auth precedence (per ADR 0012, Phase 2 starting state):
//  1. Authorization: Bearer <jwt>
//  2. Cookie: bamboo_session=<jwt>
//  3. X-Tenant-Slug header (dev fallback; tenant only, no user)
//  4. "default" tenant (dev convenience)
//
// Read endpoints accept GET; the peer-mutation endpoints under
// /api/v1/peers/{register,heartbeat} accept POST. The
// /api/v1/peers/watch endpoint is GET (Server-Sent Events stream).
func (h *HTTPServer) routeAPI(w http.ResponseWriter, r *http.Request) {
	// Peer mutation + watch endpoints have non-GET methods or stream
	// responses; route them before the read-only switch below.
	switch r.URL.Path {
	case "/api/v1/peers/register":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.apiPeersRegister(w, r)
		return
	case "/api/v1/peers/heartbeat":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.apiPeersHeartbeat(w, r)
		return
	case "/api/v1/peers/watch":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.apiPeersWatch(w, r)
		return
	}

	authn, err := h.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}

	// Prod-mode gate. /api/v1/me is exempt so the Web can render its
	// signed-out landing state without seeing a 401; everything else
	// requires a valid JWT once require_auth is on. Peer-id-only paths
	// (/peers/register, /peers/heartbeat, /peers/watch) short-circuited
	// before this block, so this check covers only the read+admin REST
	// surface.
	if h.requireAuth && authn.claims == nil && r.URL.Path != "/api/v1/me" {
		writeError(w, http.StatusUnauthorized, errors.New("authentication required"))
		return
	}

	tenant, err := h.resolveTenant(r, authn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// /api/v1/peers/{id} accepts GET / PATCH / DELETE; the
	// /api/v1/peers/{id}/events sub-path is a read-only timeline.
	// Both are routed here before the GET-only block below.
	// The reserved sub-paths (register / heartbeat / watch) are
	// exact-matched in the early switch above and never reach this
	// prefix check.
	if rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/peers/"); ok && rest != "" {
		// "{id}/events" — the only two-segment shape supported.
		if id, found := strings.CutSuffix(rest, "/events"); found && id != "" && !strings.Contains(id, "/") {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			h.apiPeerEvents(w, r, tenant, id)
			return
		}
		// "{id}" — single peer GET/PATCH/DELETE.
		if !strings.Contains(rest, "/") {
			switch r.Method {
			case http.MethodGet:
				h.apiPeer(w, r, tenant, rest)
			case http.MethodPatch:
				h.apiPeerPatch(w, r, authn, tenant, rest)
			case http.MethodDelete:
				h.apiPeerDelete(w, r, authn, tenant, rest)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}
		writeRouteMissing(w)
		return
	}

	// Tenant-scoped pre-auth-key endpoints. Sit between the
	// /peers/{id} block (which handles its own method dispatch)
	// and the GET-only block below.
	//   GET  /api/v1/preauth-keys             → list (admin)
	//   POST /api/v1/preauth-keys             → mint (admin)
	//   POST /api/v1/preauth-keys/{id}/revoke → revoke (admin)
	if r.URL.Path == "/api/v1/preauth-keys" {
		switch r.Method {
		case http.MethodGet:
			h.apiListPreAuthKeys(w, r, authn, tenant)
		case http.MethodPost:
			h.apiCreatePreAuthKey(w, r, authn, tenant)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/preauth-keys/"); ok && rest != "" {
		// {id}/revoke is the only sub-path. Anything else is 404.
		if id, found := strings.CutSuffix(rest, "/revoke"); found && id != "" && !strings.Contains(id, "/") {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			h.apiRevokePreAuthKey(w, r, authn, tenant, id)
			return
		}
		writeRouteMissing(w)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	switch r.URL.Path {
	case "/api/v1/me":
		h.apiMe(w, r, authn, tenant)
	case "/api/v1/overview":
		h.apiOverview(w, r, tenant)
	case "/api/v1/peers":
		h.apiPeers(w, r, tenant)
	case "/api/v1/policy":
		h.apiPolicy(w, r, tenant)
	case "/api/v1/recommendations":
		h.apiRecommendations(w, r, tenant)
	case "/api/v1/activity":
		h.apiActivity(w, r, tenant)
	case "/api/v1/dns":
		h.apiDNS(w, r, authn, tenant)
	case "/api/v1/users":
		h.apiUsers(w, r, authn, tenant)
	default:
		writeRouteMissing(w)
	}
}

// writeRouteMissing writes the canonical "no REST route matched"
// response. Distinct from handler-level 404s (which are still
// http.NotFound, used for resource lookups under a known route)
// because clients need to tell "controller doesn't know this URL"
// apart from "controller knows the URL but the resource is gone".
// The contract test in test/e2e/web_route_contract_test.go pins
// this distinction so Web→Controller route drift surfaces in CI.
func writeRouteMissing(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, errors.New("route not registered"))
}

// authnContext is what authenticate produces. claims is non-nil when a
// JWT was successfully validated; otherwise the request is in the
// dev-fallback path and the caller resolves the tenant via header.
type authnContext struct {
	claims *auth.SessionClaims
}

// authenticate validates a session JWT from the Authorization header
// (preferred) or the bamboo_session cookie. Returns an authnContext
// with nil claims when no credentials are present so the caller can
// fall back to header-based dev mode. Returns an error only when a
// credential is present but invalid.
//
// When a JWT is present and verified, this also validates tenant
// membership: the user's persisted users.tenant_id must equal the
// token's TenantID claim. A mismatch indicates a stale JWT issued
// before the user was moved to (or removed from) the claimed tenant,
// and is rejected so the old token cannot grant cross-tenant access
// for the rest of its TTL.
func (h *HTTPServer) authenticate(r *http.Request) (*authnContext, error) {
	var claims *auth.SessionClaims
	if tok := bearerToken(r); tok != "" {
		c, err := auth.VerifySessionToken(h.secret, tok)
		if err != nil {
			return nil, errors.New("invalid bearer token")
		}
		claims = c
	} else if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie.Value != "" {
		c, err := auth.VerifySessionToken(h.secret, cookie.Value)
		if err != nil {
			return nil, errors.New("invalid session cookie")
		}
		claims = c
	}
	if claims == nil {
		// No credential — dev fallback path. The caller resolves tenant
		// from X-Tenant-Slug or "default".
		return &authnContext{}, nil
	}
	// Unit tests construct HTTPServer directly without a users repo to
	// exercise JWT parsing in isolation; skip the membership check in
	// that case. Production always wires users via NewHTTPServer.
	if h.users != nil {
		user, err := h.users.GetByID(r.Context(), claims.UserID)
		if err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				return nil, errors.New("user not found")
			}
			return nil, fmt.Errorf("resolve user: %w", err)
		}
		if user.TenantID != claims.TenantID {
			return nil, errors.New("tenant membership mismatch")
		}
	}
	return &authnContext{claims: claims}, nil
}

func bearerToken(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if v == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(v, prefix) {
		return ""
	}
	return strings.TrimSpace(v[len(prefix):])
}

// peerCredentialAllows reports whether the request carries a
// credential that proves the caller may act for the given peer_id.
// Two paths are accepted:
//
//   - A peer-session bearer whose claim's peer_id equals expectedPeerID.
//     Also re-validates the peer still exists, hasn't rotated pubkey,
//     and hasn't been moved tenants (via usePeerSessionTenant).
//   - A user-session JWT — admin driving a peer manually through the
//     API. The user need only be authenticated; per-peer admin scope
//     is enforced separately by requireAdmin where applicable.
//
// Returns false when no credential is present so callers can write
// the 401. Designed to be called only when h.requireAuth is true;
// callers must not skip the check on the assumption that "the dev
// fallback still works" — that's exactly the hole Finding #1 closes.
func (h *HTTPServer) peerCredentialAllows(r *http.Request, expectedPeerID string) bool {
	if claims := h.peerSessionFromRequest(r); claims != nil {
		// A peer-session bearer must carry the same peer_id the
		// caller is acting on. A leaked token from one peer cannot
		// be used to drive heartbeat / watch on a different peer.
		if claims.PeerID.String() == expectedPeerID {
			// Re-validate state (pubkey rotation, tenant move, etc.).
			tenant, _ := h.usePeerSessionTenant(r)
			if tenant != nil {
				return true
			}
		}
	}
	authn, err := h.authenticate(r)
	if err == nil && authn.claims != nil {
		return true
	}
	return false
}

// peerSessionFromRequest extracts a peer-session bearer token from
// the Authorization header and returns its verified claims. Returns
// (nil, nil) when no bearer is present so callers can fall back to
// other auth paths (user-session JWT, X-Tenant-Slug). Returns (nil,
// nil) also when the bearer fails peer-session verification — the
// same header is shared with user-session JWTs, and only one of the
// two should succeed for a given token; the caller's authenticate()
// path handles the user-session case independently.
//
// A token whose signature is valid but whose embedded peer is missing
// or has been moved to a different tenant produces a non-nil claims
// pointer; callers must validate the peer still exists and matches
// before trusting the claim (see usePeerSessionTenant).
func (h *HTTPServer) peerSessionFromRequest(r *http.Request) *auth.PeerSessionClaims {
	tok := bearerToken(r)
	if tok == "" {
		return nil
	}
	claims, err := auth.VerifyPeerSessionToken(h.secret, tok)
	if err != nil {
		return nil
	}
	return claims
}

// usePeerSessionTenant returns the tenant identified by a peer-
// session bearer, after verifying the claimed peer still exists and
// still belongs to the claimed tenant with the claimed wireguard
// pubkey. This is the path callers like heartbeat / watch / relay-
// token take when an explicit user session is absent: they trust the
// peer-bound credential the controller itself minted at Register
// time, rather than the unauthenticated X-Tenant-Slug header.
//
// Returns (nil, nil) when no peer session bearer is present so
// callers can fall through to resolveTenant's existing behavior. A
// token whose embedded peer no longer matches the database state
// (deleted, re-keyed, or moved tenants) is treated as absent — the
// controller refuses to trust a stale credential.
func (h *HTTPServer) usePeerSessionTenant(r *http.Request) (*repo.Tenant, *auth.PeerSessionClaims) {
	claims := h.peerSessionFromRequest(r)
	if claims == nil {
		return nil, nil
	}
	peer, err := h.peers.GetByID(r.Context(), claims.PeerID)
	if err != nil || peer == nil {
		return nil, nil
	}
	if peer.TenantID != claims.TenantID {
		return nil, nil
	}
	if claims.WGPublicKey != "" && peer.WireGuardPublicKey != claims.WGPublicKey {
		return nil, nil
	}
	tenant, err := h.tenants.GetByID(r.Context(), claims.TenantID)
	if err != nil || tenant == nil {
		return nil, nil
	}
	return tenant, claims
}

// resolveTenant uses the JWT claims when present (production path) and
// otherwise falls back to the X-Tenant-Slug header (dev path).
func (h *HTTPServer) resolveTenant(r *http.Request, authn *authnContext) (*repo.Tenant, error) {
	if authn != nil && authn.claims != nil {
		return h.tenants.GetByID(r.Context(), authn.claims.TenantID)
	}
	slug := r.Header.Get("X-Tenant-Slug")
	if slug == "" {
		slug = "default"
	}
	return h.tenants.GetOrCreate(r.Context(), slug, "Default Tenant", "100.64.0.0/24")
}

// apiMeJSON is the wire shape for /api/v1/me.
type apiMeJSON struct {
	Authenticated bool       `json:"authenticated"`
	UserID        string     `json:"userId,omitempty"`
	Email         string     `json:"email,omitempty"`
	DisplayName   string     `json:"displayName,omitempty"`
	OIDCProvider  string     `json:"oidcProvider,omitempty"`
	IsAdmin       bool       `json:"isAdmin,omitempty"`
	TenantID      string     `json:"tenantId"`
	TenantSlug    string     `json:"tenantSlug"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
}

func (h *HTTPServer) apiMe(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) {
	out := apiMeJSON{
		Authenticated: authn != nil && authn.claims != nil,
		TenantID:      tenant.ID.String(),
		TenantSlug:    tenant.Slug,
	}
	if !out.Authenticated {
		writeJSON(w, http.StatusOK, out)
		return
	}
	user, err := h.users.GetByID(r.Context(), authn.claims.UserID)
	if err != nil && !errors.Is(err, repo.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if user != nil {
		out.UserID = user.ID.String()
		out.Email = user.Email
		out.DisplayName = user.DisplayName
		out.OIDCProvider = user.OIDCProvider
		out.IsAdmin = user.IsAdmin
	}
	exp := time.Unix(authn.claims.ExpiresAt, 0)
	out.ExpiresAt = &exp
	writeJSON(w, http.StatusOK, out)
}

// apiPeerJSON is the wire shape for the peers endpoint.
type apiPeerJSON struct {
	ID                 string     `json:"id"`
	TenantID           string     `json:"tenantId"`
	Hostname           string     `json:"hostname"`
	IP                 string     `json:"ip"`
	Tags               []string   `json:"tags"`
	OS                 string     `json:"os"`
	ClientVersion      string     `json:"clientVersion"`
	Status             string     `json:"status"`
	WireGuardPublicKey string     `json:"wireguardPublicKey,omitempty"`
	Endpoints          []string   `json:"endpoints"`
	WGEndpoint         *string    `json:"wgEndpoint,omitempty"`
	RxBytes            int64      `json:"rxBytes"`
	TxBytes            int64      `json:"txBytes"`
	CreatedAt          time.Time  `json:"createdAt"`
	LastSeenAt         *time.Time `json:"lastSeenAt,omitempty"`
	LastHandshakeAt    *time.Time `json:"lastHandshakeAt,omitempty"`
}

func peerToJSON(p *repo.Peer) apiPeerJSON {
	endpoints := p.Endpoints
	if endpoints == nil {
		endpoints = []string{}
	}
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	return apiPeerJSON{
		ID:                 p.ID.String(),
		TenantID:           p.TenantID.String(),
		Hostname:           p.Hostname,
		IP:                 p.IP,
		Tags:               tags,
		OS:                 p.OS,
		ClientVersion:      p.ClientVersion,
		Status:             p.Status,
		WireGuardPublicKey: p.WireGuardPublicKey,
		Endpoints:          endpoints,
		WGEndpoint:         p.WGEndpoint,
		RxBytes:            p.RxBytes,
		TxBytes:            p.TxBytes,
		CreatedAt:          p.CreatedAt,
		LastSeenAt:         p.LastSeenAt,
		LastHandshakeAt:    p.LastHandshakeAt,
	}
}

func (h *HTTPServer) apiPeers(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant) {
	peers, err := h.peers.ListByTenant(r.Context(), tenant.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]apiPeerJSON, 0, len(peers))
	for _, p := range peers {
		out = append(out, peerToJSON(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": out})
}

// apiPeer returns a single peer by id, scoped to the request tenant.
// A peer whose tenant does not match is reported as 404 so callers
// cannot probe peer existence across tenants.
func (h *HTTPServer) apiPeer(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant, idStr string) {
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := h.peers.GetByID(r.Context(), id)
	if errors.Is(err, repo.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if p.TenantID != tenant.ID {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, peerToJSON(p))
}

// apiPeerPatchReq is the request shape for PATCH /api/v1/peers/{id}.
// Each field is a pointer so the handler can distinguish "field
// absent" (preserve) from "field set to its zero value" (e.g. empty
// tag list = clear all tags).
type apiPeerPatchReq struct {
	Hostname *string   `json:"hostname,omitempty"`
	Status   *string   `json:"status,omitempty"`
	Tags     *[]string `json:"tags,omitempty"`
}

// apiPeerPatch implements PATCH /api/v1/peers/{id}. Any subset of
// hostname / status / tags can be updated in one call; the audit
// row records before/after only for the fields that actually
// changed. Tenant scoping is identical to apiPeer — a peer in a
// different tenant returns 404.
func (h *HTTPServer) apiPeerPatch(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, idStr string) {
	if !h.requireAdmin(w, r, authn, tenant, "peer.update") {
		return
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var req apiPeerPatchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}

	current, err := h.peers.GetByID(r.Context(), id)
	if errors.Is(err, repo.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current.TenantID != tenant.ID {
		http.NotFound(w, r)
		return
	}

	// Validate before any write so we never half-apply.
	var newHostname string
	if req.Hostname != nil {
		newHostname = strings.TrimSpace(*req.Hostname)
		if newHostname == "" {
			writeError(w, http.StatusBadRequest, errors.New("hostname must be non-empty"))
			return
		}
		if len(newHostname) > 253 {
			writeError(w, http.StatusBadRequest, errors.New("hostname exceeds 253 chars"))
			return
		}
	}
	if req.Status != nil {
		switch *req.Status {
		case "online", "offline", "disabled":
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("status must be online | offline | disabled, got %q", *req.Status))
			return
		}
	}

	diff := map[string]any{}

	if req.Hostname != nil && newHostname != current.Hostname {
		if _, err := h.peers.UpdateHostname(r.Context(), id, newHostname); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("update hostname: %w", err))
			return
		}
		diff["hostname"] = map[string]string{"from": current.Hostname, "to": newHostname}
	}
	if req.Status != nil && *req.Status != current.Status {
		if _, err := h.peers.SetStatus(r.Context(), id, *req.Status); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("set status: %w", err))
			return
		}
		diff["status"] = map[string]string{"from": current.Status, "to": *req.Status}
	}
	if req.Tags != nil {
		newTags, err := h.peers.SetTags(r.Context(), id, *req.Tags)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("set tags: %w", err))
			return
		}
		if !stringSlicesEqual(current.Tags, newTags) {
			diff["tags"] = map[string][]string{"from": current.Tags, "to": newTags}
		}
	}

	if len(diff) > 0 {
		writePeerAudit(r.Context(), h.audits, authn, tenant.ID, id, "peer.update", diff)
	}

	// Re-read so the response carries the canonical post-update state
	// (including the de-duped, sorted tag set from SetTags).
	updated, err := h.peers.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, peerToJSON(updated))
}

// apiPeerDelete implements DELETE /api/v1/peers/{id}. peer_tags rows
// cascade via FK; the audit row captures the deleted peer's
// identifying attributes so the timeline still shows what was
// removed after the row is gone.
//
// Missing peer / cross-tenant / bad uuid all collapse to 404 with
// the same response shape. This is deliberately *not* idempotent
// (a re-delete will 404, not 204): treating "already gone" as
// success would let an outside caller distinguish "exists in
// another tenant" from "doesn't exist anywhere" and probe
// existence across tenants.
func (h *HTTPServer) apiPeerDelete(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, idStr string) {
	if !h.requireAdmin(w, r, authn, tenant, "peer.delete") {
		return
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	current, err := h.peers.GetByID(r.Context(), id)
	if errors.Is(err, repo.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current.TenantID != tenant.ID {
		http.NotFound(w, r)
		return
	}

	if _, err := h.peers.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("delete peer: %w", err))
		return
	}
	writePeerAudit(r.Context(), h.audits, authn, tenant.ID, id, "peer.delete", map[string]any{
		"hostname":           current.Hostname,
		"ip":                 current.IP,
		"wireguardPublicKey": current.WireGuardPublicKey,
		"tags":               current.Tags,
	})
	w.WriteHeader(http.StatusNoContent)
}

// apiAuditEventJSON is the wire shape for a single audit-log entry.
// Used by both the per-peer timeline (where ResourceType/ResourceID
// are implied by the URL and omitted via omitempty) and the
// tenant-wide /activity feed (where they're populated and let the
// UI route to the right resource view).
//
// Diff is forwarded verbatim — the Web UI renders peer.update's
// {field: {from, to}} structure specially and pretty-prints the
// rest as JSON.
type apiAuditEventJSON struct {
	ID           string          `json:"id"`
	ActorType    string          `json:"actorType"`
	ActorID      string          `json:"actorId,omitempty"`
	ActorEmail   string          `json:"actorEmail,omitempty"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resourceType,omitempty"`
	ResourceID   string          `json:"resourceId,omitempty"`
	Diff         json.RawMessage `json:"diff,omitempty"`
	OccurredAt   time.Time       `json:"occurredAt"`
}

// auditEventToJSON centralizes the AuditEvent → wire-shape mapping
// so the per-peer timeline and the tenant-wide activity feed
// produce identical row shapes. The per-peer caller doesn't include
// resourceType/resourceId in the output today, but since
// AuditEvent carries them, it costs nothing to forward — omitempty
// handles the nil-ResourceID case for system events that aren't
// tied to a resource.
func auditEventToJSON(e *repo.AuditEvent) apiAuditEventJSON {
	row := apiAuditEventJSON{
		ID:           e.ID.String(),
		ActorType:    e.ActorType,
		ActorEmail:   e.ActorEmail,
		Action:       e.Action,
		ResourceType: e.ResourceType,
		Diff:         e.Diff,
		OccurredAt:   e.OccurredAt,
	}
	if e.ActorID != nil {
		row.ActorID = e.ActorID.String()
	}
	if e.ResourceID != nil {
		row.ResourceID = e.ResourceID.String()
	}
	return row
}

// apiPeerEvents implements GET /api/v1/peers/{id}/events. Returns
// the most recent audit_log rows targeting this peer, newest first.
// Tenant scoping mirrors apiPeer / apiPeerPatch — a peer in another
// tenant collapses to 404 before we even look at the audit table,
// so events for that peer never leak across tenants.
func (h *HTTPServer) apiPeerEvents(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant, idStr string) {
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Verify the peer exists in this tenant first. Skipping this
	// check would let a caller fish for events targeting a peer
	// they shouldn't see — the audit row carries tenant_id, but
	// the ResourceID alone (a uuid) isn't tenant-scoped on its own.
	p, err := h.peers.GetByID(r.Context(), id)
	if errors.Is(err, repo.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if p.TenantID != tenant.ID {
		http.NotFound(w, r)
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	events, err := h.audits.ListByResource(r.Context(), tenant.ID, "peer", id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	out := make([]apiAuditEventJSON, 0, len(events))
	for _, e := range events {
		out = append(out, auditEventToJSON(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// apiActivity implements GET /api/v1/activity?limit=N. Tenant-wide
// audit feed, newest first; powers the dashboard's recentActivity
// section. Distinct from /peers/{id}/events because activity here
// spans all resource types (peer, policy, pre_auth_key, ...), so
// the wire shape includes resourceType + resourceId.
func (h *HTTPServer) apiActivity(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	events, err := h.audits.ListByTenant(r.Context(), tenant.ID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]apiAuditEventJSON, 0, len(events))
	for _, e := range events {
		out = append(out, auditEventToJSON(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// apiCreatePreAuthKeyReq is the request shape for POST
// /api/v1/preauth-keys. TTL is intentionally absent — the MVP
// surface in the Web UI doesn't expose it; bootstrap script and
// gRPC callers can still set ExpiresAt via the proto path.
type apiCreatePreAuthKeyReq struct {
	Description string `json:"description,omitempty"`
	Reusable    bool   `json:"reusable,omitempty"`
	Ephemeral   bool   `json:"ephemeral,omitempty"`
}

// apiPreAuthKeyJSON is the response shape. Secret is the plaintext
// value the user types into a bamboo client; it's shown ONCE, here,
// and never again — the DB stores only the bcrypt hash.
type apiPreAuthKeyJSON struct {
	ID          string    `json:"id"`
	Description string    `json:"description,omitempty"`
	Reusable    bool      `json:"reusable"`
	Ephemeral   bool      `json:"ephemeral"`
	CreatedAt   time.Time `json:"createdAt"`
	Secret      string    `json:"secret"`
}

// apiCreatePreAuthKey implements POST /api/v1/preauth-keys. Mints a
// new pre-auth key for the request tenant and returns the plaintext
// secret so the Web UI can show it once. Mirrors the gRPC
// CreatePreAuthKey handler in handlers/auth.go — the secret
// generation + hash storage contract has to match because both
// surfaces feed the same redeem path.
func (h *HTTPServer) apiCreatePreAuthKey(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) {
	if !h.requireAdmin(w, r, authn, tenant, "preauthkey.create") {
		return
	}

	var req apiCreatePreAuthKeyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}

	id := uuid.New()
	plaintext, hash, err := auth.GenerateSecret(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("generate secret: %w", err))
		return
	}

	created, err := h.keys.Create(r.Context(), &repo.PreAuthKey{
		ID:          id,
		TenantID:    tenant.ID,
		Description: req.Description,
		SecretHash:  hash,
		Reusable:    req.Reusable,
		Ephemeral:   req.Ephemeral,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("insert key: %w", err))
		return
	}

	// Audit row uses the same shape as the gRPC handler so the
	// dashboard activity feed sees a uniform preauthkey.create
	// regardless of which surface minted the key.
	auditEv := &repo.AuditEvent{
		TenantID:     &tenant.ID,
		Action:       "preauthkey.create",
		ResourceType: "pre_auth_key",
		ResourceID:   &created.ID,
		Diff: marshalDiff(map[string]any{
			"description": created.Description,
			"reusable":    created.Reusable,
			"ephemeral":   created.Ephemeral,
		}),
	}
	if authn != nil && authn.claims != nil {
		auditEv.ActorType = "user"
		uid := authn.claims.UserID
		auditEv.ActorID = &uid
	} else {
		auditEv.ActorType = "system"
	}
	if err := h.audits.Insert(r.Context(), auditEv); err != nil {
		slog.Warn("preauthkey audit insert failed", "key_id", created.ID, "err", err)
	}

	writeJSON(w, http.StatusOK, apiPreAuthKeyJSON{
		ID:          created.ID.String(),
		Description: created.Description,
		Reusable:    created.Reusable,
		Ephemeral:   created.Ephemeral,
		CreatedAt:   created.CreatedAt,
		Secret:      plaintext,
	})
}

// requireAdmin enforces the admin-RBAC contract every protected REST
// endpoint shares: authenticated callers must have IsAdmin=true,
// dev-fallback (no JWT) is allowed with a warn log so local dev keeps
// working. Returns true when the caller may proceed; writes the error
// response and returns false otherwise. action is a short label
// included in the warn log + the 403 body (e.g. "preauthkey.create",
// "peer.update").
//
// In prod mode (require_auth=true) the top-level routeAPI gate has
// already returned 401 for unauthenticated requests, so the
// dev-fallback warn branch is unreachable.
func (h *HTTPServer) requireAdmin(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, action string) bool {
	if authn != nil && authn.claims != nil {
		user, err := h.users.GetByID(r.Context(), authn.claims.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("resolve user: %w", err))
			return false
		}
		if !user.IsAdmin {
			writeError(w, http.StatusForbidden, fmt.Errorf("admin role required for %s", action))
			return false
		}
		return true
	}
	slog.Warn("admin path via dev-fallback auth (no JWT) — configure OIDC + an admin user in production",
		"tenant", tenant.Slug,
		"action", action,
		"method", r.Method,
		"path", r.URL.Path,
	)
	return true
}

// apiPreAuthKeyListJSON is the wire shape for list rows. No Secret
// — once minted, the plaintext is gone forever (DB stores only the
// bcrypt hash). The UI derives a display status from RevokedAt /
// ExpiresAt / UseCount / Reusable; the controller forwards raw
// columns rather than committing to a status enum here.
type apiPreAuthKeyListJSON struct {
	ID          string     `json:"id"`
	Description string     `json:"description,omitempty"`
	Reusable    bool       `json:"reusable"`
	Ephemeral   bool       `json:"ephemeral"`
	Tags        []string   `json:"tags"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
	UseCount    int64      `json:"useCount"`
}

func preAuthKeyToListJSON(k *repo.PreAuthKey) apiPreAuthKeyListJSON {
	tags := k.Tags
	if tags == nil {
		tags = []string{}
	}
	return apiPreAuthKeyListJSON{
		ID:          k.ID.String(),
		Description: k.Description,
		Reusable:    k.Reusable,
		Ephemeral:   k.Ephemeral,
		Tags:        tags,
		CreatedAt:   k.CreatedAt,
		ExpiresAt:   k.ExpiresAt,
		RevokedAt:   k.RevokedAt,
		UseCount:    k.UseCount,
	}
}

// apiListPreAuthKeys implements GET /api/v1/preauth-keys. Tenant-
// scoped, newest first. Same admin gate as the create path —
// listing exposes nothing secret on its own, but the keys are an
// admin-only resource so we keep the surface uniform.
func (h *HTTPServer) apiListPreAuthKeys(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) {
	if !h.requireAdmin(w, r, authn, tenant, "preauthkey.list") {
		return
	}
	keys, err := h.keys.ListByTenant(r.Context(), tenant.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]apiPreAuthKeyListJSON, 0, len(keys))
	for _, k := range keys {
		out = append(out, preAuthKeyToListJSON(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// apiRevokePreAuthKey implements POST /api/v1/preauth-keys/{id}/revoke.
// Idempotent at the repo layer — already-revoked keys keep their
// original revoked_at. Cross-tenant probe protection: a GetByID +
// tenant check 404s on a key belonging to another tenant before we
// even attempt the revoke, matching the /peers/{id} contract.
func (h *HTTPServer) apiRevokePreAuthKey(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, idStr string) {
	if !h.requireAdmin(w, r, authn, tenant, "preauthkey.revoke") {
		return
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	key, err := h.keys.GetByID(r.Context(), id)
	if errors.Is(err, repo.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if key.TenantID != tenant.ID {
		http.NotFound(w, r)
		return
	}
	if err := h.keys.Revoke(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("revoke key: %w", err))
		return
	}

	auditEv := &repo.AuditEvent{
		TenantID:     &tenant.ID,
		Action:       "preauthkey.revoke",
		ResourceType: "pre_auth_key",
		ResourceID:   &id,
		Diff: marshalDiff(map[string]any{
			"description": key.Description,
			"reusable":    key.Reusable,
			"useCount":    key.UseCount,
		}),
	}
	if authn != nil && authn.claims != nil {
		auditEv.ActorType = "user"
		uid := authn.claims.UserID
		auditEv.ActorID = &uid
	} else {
		auditEv.ActorType = "system"
	}
	if err := h.audits.Insert(r.Context(), auditEv); err != nil {
		slog.Warn("preauthkey.revoke audit insert failed", "key_id", id, "err", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

// writePeerAudit centralizes the actor + tenant + resource binding
// for peer mutation audit rows. authn==nil or authn.claims==nil
// happens in dev-fallback mode (no JWT); we record actor_type=system
// in that case rather than refusing the write — the alternative
// would block all admin actions outside a logged-in browser.
func writePeerAudit(ctx context.Context, audits *repo.AuditLogs, authn *authnContext, tenantID, peerID uuid.UUID, action string, diffMap map[string]any) {
	if audits == nil {
		return
	}
	ev := &repo.AuditEvent{
		TenantID:     &tenantID,
		Action:       action,
		ResourceType: "peer",
		ResourceID:   &peerID,
		Diff:         marshalDiff(diffMap),
	}
	if authn != nil && authn.claims != nil {
		ev.ActorType = "user"
		uid := authn.claims.UserID
		ev.ActorID = &uid
	} else {
		ev.ActorType = "system"
	}
	if err := audits.Insert(ctx, ev); err != nil {
		slog.Warn("peer audit insert failed", "action", action, "peer_id", peerID, "err", err)
	}
}

func marshalDiff(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type apiACLRuleJSON struct {
	ID           string   `json:"id"`
	Action       string   `json:"action"`
	Description  string   `json:"description,omitempty"`
	Sources      []string `json:"sources"`
	Destinations []string `json:"destinations"`
}

type apiPolicyJSON struct {
	TenantID  string           `json:"tenantId"`
	Revision  int64            `json:"revision"`
	HCLSource string           `json:"hclSource"`
	UpdatedAt *time.Time       `json:"updatedAt,omitempty"`
	Rules     []apiACLRuleJSON `json:"rules"`
}

func (h *HTTPServer) apiPolicy(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant) {
	rec, err := h.policies.Get(r.Context(), tenant.ID)
	if errors.Is(err, repo.ErrNotFound) {
		writeJSON(w, http.StatusOK, apiPolicyJSON{
			TenantID: tenant.ID.String(),
			Revision: 0,
			Rules:    []apiACLRuleJSON{},
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	parsed, _ := policy.Parse("policy.hcl", rec.HCLSource)
	rules := []apiACLRuleJSON{}
	if parsed != nil {
		for _, ru := range parsed.Rules {
			rules = append(rules, apiACLRuleJSON{
				ID:           ru.ID,
				Action:       ru.Action.String(),
				Description:  ru.Description,
				Sources:      formatPolicySources(ru.Sources),
				Destinations: formatPolicyDestinations(ru.Destinations),
			})
		}
	}
	writeJSON(w, http.StatusOK, apiPolicyJSON{
		TenantID:  rec.TenantID.String(),
		Revision:  rec.Revision,
		HCLSource: rec.HCLSource,
		UpdatedAt: &rec.UpdatedAt,
		Rules:     rules,
	})
}

type apiRecommendationJSON struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Summary     string    `json:"summary"`
	Diff        string    `json:"diff"`
	Evidence    []string  `json:"evidence"`
	Confidence  float32   `json:"confidence"`
	GeneratedAt time.Time `json:"generatedAt"`
}

func (h *HTTPServer) apiRecommendations(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant) {
	rec, err := h.policies.Get(r.Context(), tenant.ID)
	var parsed *policy.Policy
	switch {
	case errors.Is(err, repo.ErrNotFound):
		parsed = &policy.Policy{}
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
		return
	default:
		parsed, err = policy.Parse("policy.hcl", rec.HCLSource)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	since := time.Now().Add(-30 * 24 * time.Hour)
	hits, _ := h.traces.RuleHitCounts(r.Context(), tenant.ID, since)
	chObs, _ := h.traces.RuleObservations(r.Context(), tenant.ID, since)
	obs := make(map[string]*recommend.RuleObservation, len(chObs))
	for id, o := range chObs {
		obs[id] = &recommend.RuleObservation{
			RuleID:    o.RuleID,
			Ports:     o.Ports,
			TotalHits: o.TotalHits,
		}
	}
	chFlows, _ := h.traces.TopDeniedFlows(r.Context(), tenant.ID, since, 10, 5)
	flows := make([]recommend.DeniedFlow, len(chFlows))
	for i, f := range chFlows {
		flows[i] = recommend.DeniedFlow{
			Source:      f.Source,
			Destination: f.Destination,
			Port:        f.Port,
			Hits:        f.Hits,
		}
	}
	chFindings, _ := h.anomalies.RecentByTenant(r.Context(), tenant.ID, time.Now().Add(-24*time.Hour), 20)
	findings := make([]recommend.AnomalyFinding, len(chFindings))
	for i, f := range chFindings {
		findings[i] = recommend.AnomalyFinding{
			ID:           f.ID,
			OccurredAt:   f.OccurredAt,
			GeneratedAt:  f.GeneratedAt,
			Score:        f.Score,
			ModelVersion: f.ModelVersion,
			EventSummary: f.EventSummary,
		}
	}

	recs := recommend.UnusedRules(parsed, hits, since)
	recs = append(recs, recommend.OverPrivilegedRules(parsed, obs, since)...)
	recs = append(recs, recommend.BroadenNeeded(parsed, flows, since)...)
	recs = append(recs, recommend.Anomalies(findings, 0.6)...)

	out := make([]apiRecommendationJSON, 0, len(recs))
	for _, x := range recs {
		out = append(out, apiRecommendationJSON{
			ID:          x.ID.String(),
			Kind:        kindString(x.Kind),
			Summary:     x.Summary,
			Diff:        x.Diff,
			Evidence:    x.Evidence,
			Confidence:  x.Confidence,
			GeneratedAt: x.GeneratedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"recommendations": out})
}

type apiOverviewJSON struct {
	TenantID       string `json:"tenantId"`
	TotalPeers     int    `json:"totalPeers"`
	OnlinePeers    int    `json:"onlinePeers"`
	OfflinePeers   int    `json:"offlinePeers"`
	PolicyRevision int64  `json:"policyRevision"`
	RecommendCount int    `json:"recommendationCount"`
}

// apiDNSJSON is the wire shape for /api/v1/dns. tailnetName is a
// derived display field (tenant.slug for now) so the UI can show a
// stable identifier without us committing to a generated name like
// Tailscale's tail{N}.ts.net format. magicDnsEnabled / nameservers /
// searchDomains map 1:1 to tenant_dns_config columns. updatedBy is
// the user-id string when set; empty when defaults are surfaced for
// an unwritten row.
type apiDNSJSON struct {
	TenantID           string    `json:"tenantId"`
	TenantSlug         string    `json:"tenantSlug"`
	TailnetName        string    `json:"tailnetName"`
	MagicDNSEnabled    bool      `json:"magicDnsEnabled"`
	GlobalNameservers  []string  `json:"globalNameservers"`
	SearchDomains      []string  `json:"searchDomains"`
	OverrideDNSServers bool      `json:"overrideDnsServers"`
	UpdatedAt          time.Time `json:"updatedAt"`
	UpdatedBy          string    `json:"updatedBy,omitempty"`
}

// apiDNS returns the tenant's DNS settings. Read is member-readable
// (any authed caller in the tenant can see the resolver config);
// mutation lives in a future PUT handler that lands once the data-
// plane MagicDNS implementation is real. Today we just expose the
// stored values so the admin Settings → DNS page can render them.
//
// When no row exists yet, Get returns a defaults-populated record
// with UpdatedAt zero-valued; the handler still 200s.
func (h *HTTPServer) apiDNS(w http.ResponseWriter, r *http.Request, _ *authnContext, tenant *repo.Tenant) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	cfg, err := h.dns.Get(r.Context(), tenant.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	updatedBy := ""
	if cfg.UpdatedBy != nil {
		updatedBy = cfg.UpdatedBy.String()
	}
	writeJSON(w, http.StatusOK, apiDNSJSON{
		TenantID:           tenant.ID.String(),
		TenantSlug:         tenant.Slug,
		TailnetName:        tenant.Slug,
		MagicDNSEnabled:    cfg.MagicDNSEnabled,
		GlobalNameservers:  cfg.GlobalNameservers,
		SearchDomains:      cfg.SearchDomains,
		OverrideDNSServers: cfg.OverrideDNSServers,
		UpdatedAt:          cfg.UpdatedAt,
		UpdatedBy:          updatedBy,
	})
}

// apiUserListJSON is the wire shape for /api/v1/users rows. We expose
// the columns the Users admin page needs (identity + role + most-
// recent activity proxy) and omit OIDC subject — that's an internal
// linkage, not useful to the operator.
type apiUserListJSON struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"displayName,omitempty"`
	OIDCProvider string    `json:"oidcProvider,omitempty"`
	IsAdmin      bool      `json:"isAdmin,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// apiUsers returns every user in the request tenant. Admin-gated:
// member-role accounts get 403, matching the same RBAC contract as
// PreAuthKey listing. Mutation endpoints (invite / role-change /
// delete) are out of scope for this Phase A skeleton and will land
// in a follow-up PR once the email + audit pieces are designed.
func (h *HTTPServer) apiUsers(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	if !h.requireAdmin(w, r, authn, tenant, "user.list") {
		return
	}
	users, err := h.users.ListByTenant(r.Context(), tenant.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]apiUserListJSON, 0, len(users))
	for _, u := range users {
		out = append(out, apiUserListJSON{
			ID:           u.ID.String(),
			Email:        u.Email,
			DisplayName:  u.DisplayName,
			OIDCProvider: u.OIDCProvider,
			IsAdmin:      u.IsAdmin,
			CreatedAt:    u.CreatedAt,
			UpdatedAt:    u.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (h *HTTPServer) apiOverview(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant) {
	peers, err := h.peers.ListByTenant(r.Context(), tenant.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	online := 0
	for _, p := range peers {
		if p.Status == "online" {
			online++
		}
	}

	revision := int64(0)
	rec, err := h.policies.Get(r.Context(), tenant.ID)
	if err == nil {
		revision = rec.Revision
	}

	recommendCount := countRecommendations(r.Context(), h, tenant)

	writeJSON(w, http.StatusOK, apiOverviewJSON{
		TenantID:       tenant.ID.String(),
		TotalPeers:     len(peers),
		OnlinePeers:    online,
		OfflinePeers:   len(peers) - online,
		PolicyRevision: revision,
		RecommendCount: recommendCount,
	})
}

func countRecommendations(ctx context.Context, h *HTTPServer, tenant *repo.Tenant) int {
	rec, err := h.policies.Get(ctx, tenant.ID)
	if err != nil {
		return 0
	}
	parsed, err := policy.Parse("policy.hcl", rec.HCLSource)
	if err != nil {
		return 0
	}
	since := time.Now().Add(-30 * 24 * time.Hour)
	hits, _ := h.traces.RuleHitCounts(ctx, tenant.ID, since)
	chObs, _ := h.traces.RuleObservations(ctx, tenant.ID, since)
	obs := make(map[string]*recommend.RuleObservation, len(chObs))
	for id, o := range chObs {
		obs[id] = &recommend.RuleObservation{
			RuleID:    o.RuleID,
			Ports:     o.Ports,
			TotalHits: o.TotalHits,
		}
	}
	chFlows, _ := h.traces.TopDeniedFlows(ctx, tenant.ID, since, 10, 5)
	flows := make([]recommend.DeniedFlow, len(chFlows))
	for i, f := range chFlows {
		flows[i] = recommend.DeniedFlow{
			Source: f.Source, Destination: f.Destination, Port: f.Port, Hits: f.Hits,
		}
	}
	chFindings, _ := h.anomalies.RecentByTenant(ctx, tenant.ID, time.Now().Add(-24*time.Hour), 20)
	findings := make([]recommend.AnomalyFinding, len(chFindings))
	for i, f := range chFindings {
		findings[i] = recommend.AnomalyFinding{
			ID:           f.ID,
			OccurredAt:   f.OccurredAt,
			GeneratedAt:  f.GeneratedAt,
			Score:        f.Score,
			ModelVersion: f.ModelVersion,
			EventSummary: f.EventSummary,
		}
	}
	return len(recommend.UnusedRules(parsed, hits, since)) +
		len(recommend.OverPrivilegedRules(parsed, obs, since)) +
		len(recommend.BroadenNeeded(parsed, flows, since)) +
		len(recommend.Anomalies(findings, 0.6))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// formatPolicySources / Destinations duplicate the small renderer the
// gRPC handler uses; keeping them in this file avoids cross-package
// imports and keeps the JSON wire shape obvious.
func formatPolicySources(ms []policy.Matcher) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, formatPolicyMatcher(m))
	}
	return out
}

func formatPolicyDestinations(ms []policy.Matcher) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, formatPolicyMatcher(m)+":"+m.Ports.String())
	}
	return out
}

func formatPolicyMatcher(m policy.Matcher) string {
	switch m.Kind {
	case policy.MatcherWildcard:
		return "*"
	case policy.MatcherTag:
		return "tag:" + m.Name
	case policy.MatcherGroup:
		return "group:" + m.Name
	case policy.MatcherUser:
		return "user:" + m.Name
	case policy.MatcherCIDR:
		return "cidr:" + m.CIDR.String()
	default:
		return "?"
	}
}

func kindString(k recommend.Kind) string {
	switch k {
	case recommend.KindRemoveUnusedRule:
		return "REMOVE_UNUSED_RULE"
	case recommend.KindTightenOverPrivileged:
		return "TIGHTEN_OVERPRIVILEGED"
	case recommend.KindBroadenNeeded:
		return "BROADEN_NEEDED"
	case recommend.KindFlagAnomalous:
		return "FLAG_ANOMALOUS"
	default:
		return "UNKNOWN"
	}
}

// _ keeps uuid imported (used elsewhere if api.go grows). Removed once
// uuid is referenced directly in this file.
var _ uuid.UUID
