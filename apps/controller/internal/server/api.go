// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/handlers"
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
		// "{id}/events" — peer activity timeline.
		if id, found := strings.CutSuffix(rest, "/events"); found && id != "" && !strings.Contains(id, "/") {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			h.apiPeerEvents(w, r, tenant, id)
			return
		}
		// "{id}/approve" — admin gates a pending peer into the mesh
		// (issue #133). POST-only; requireAdmin is enforced inside the
		// handler. Idempotent: re-approving an already-approved peer
		// returns 200 with a noop indicator so the Web UI can refresh
		// without bouncing on race conditions.
		if id, found := strings.CutSuffix(rest, "/approve"); found && id != "" && !strings.Contains(id, "/") {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			h.apiPeerApprove(w, r, authn, tenant, id)
			return
		}
		// "{id}/reject" — admin rejects a pending peer registration
		// (issue #133). POST-only; same admin gate as /approve. The
		// peer row is retained for audit but its approval_status
		// flips to 'rejected' and it never participates in the mesh.
		if id, found := strings.CutSuffix(rest, "/reject"); found && id != "" && !strings.Contains(id, "/") {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			h.apiPeerReject(w, r, authn, tenant, id)
			return
		}
		// "{id}/routes" — admin approves a subset of the peer's
		// advertised_routes (issue #136). Body: {"routes": [...]}
		// with the explicit approved subset; empty list ⇒ revoke
		// all.
		if id, found := strings.CutSuffix(rest, "/routes"); found && id != "" && !strings.Contains(id, "/") {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			h.apiPeerSetApprovedRoutes(w, r, authn, tenant, id)
			return
		}
		// "{id}/exit-node" — admin approves/revokes the peer's
		// exit-node role (issue #137). Body: {"approved": bool}.
		if id, found := strings.CutSuffix(rest, "/exit-node"); found && id != "" && !strings.Contains(id, "/") {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			h.apiPeerSetExitNodeApproved(w, r, authn, tenant, id)
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

	// Tenant-scoped invitations.
	//   GET  /api/v1/invitations               → list (admin)
	//   POST /api/v1/invitations               → mint (admin)
	//   POST /api/v1/invitations/{id}/revoke   → revoke (admin)
	// Accept is handled by the OIDC callback flow (handleCallback in
	// server/http.go), not a REST endpoint.
	if r.URL.Path == "/api/v1/invitations" {
		switch r.Method {
		case http.MethodGet:
			h.apiListInvitations(w, r, authn, tenant)
		case http.MethodPost:
			h.apiCreateInvitation(w, r, authn, tenant)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/invitations/"); ok && rest != "" {
		// {id}/revoke is the only sub-path. Anything else is 404.
		if id, found := strings.CutSuffix(rest, "/revoke"); found && id != "" && !strings.Contains(id, "/") {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			h.apiRevokeInvitation(w, r, authn, tenant, id)
			return
		}
		http.NotFound(w, r)
		return
	}

	// /api/v1/policy accepts GET (read) + PUT (admin update). Lives
	// outside the GET-only switch below so the PUT body parser can
	// run. Other admin-write endpoints follow the same pattern
	// (preauth-keys, invitations).
	if r.URL.Path == "/api/v1/policy" {
		switch r.Method {
		case http.MethodGet:
			h.apiPolicy(w, r, tenant)
		case http.MethodPut:
			h.apiPutPolicy(w, r, authn, tenant)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	// /api/v1/policy/{validate,simulate,revisions,rollback} — admin
	// tooling endpoints powering the ACL editor (issue #134).
	if r.URL.Path == "/api/v1/policy/validate" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.apiValidatePolicy(w, r, authn, tenant)
		return
	}
	if r.URL.Path == "/api/v1/policy/simulate" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.apiSimulatePolicy(w, r, authn, tenant)
		return
	}
	if r.URL.Path == "/api/v1/policy/revisions" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.apiPolicyRevisions(w, r, authn, tenant)
		return
	}
	if r.URL.Path == "/api/v1/policy/rollback" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.apiPolicyRollback(w, r, authn, tenant)
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
	case "/api/v1/recommendations":
		h.apiRecommendations(w, r, tenant)
	case "/api/v1/activity":
		h.apiActivity(w, r, tenant)
	case "/api/v1/dns":
		h.apiDNS(w, r, authn, tenant)
	case "/api/v1/users":
		h.apiUsers(w, r, authn, tenant)
	case "/api/v1/logs":
		h.apiLogs(w, r, tenant)
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
	PeerDNSName        *string    `json:"peerDnsName,omitempty"`
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
	// Owner is populated only by read paths that LEFT JOIN users
	// (list + get). Empty when peer.user_id is NULL — legacy peers
	// registered before owner attribution was wired (dev-fallback or
	// pre-auth keys minted without a session) carry no owner.
	OwnerEmail       string `json:"ownerEmail,omitempty"`
	OwnerDisplayName string `json:"ownerDisplayName,omitempty"`
	// ApprovalStatus is one of pending / approved / rejected (issue
	// #133). The Web UI separates pending rows into their own section
	// at the top of the peers page so admins can act on them.
	// approvedAt + approvedBy are present when an admin acted on the
	// row; pre-#133 peers carry approvedAt=createdAt and approvedBy=nil.
	ApprovalStatus  string     `json:"approvalStatus"`
	ApprovedAt      *time.Time `json:"approvedAt,omitempty"`
	ApprovedByEmail string     `json:"approvedByEmail,omitempty"`
	// Subnet router (issue #136) + exit-node (issue #137).
	// AdvertisedRoutes is what the peer asked for; ApprovedRoutes
	// is the admin-signed subset that the ACL compiler actually
	// pushes. ExitNodeCapable mirrors --advertise-exit-node;
	// ExitNodeApproved mirrors the admin's separate sign-off.
	// UsingExitNodePeerID points at the peer this row is routing
	// default traffic through (empty when no exit node in use).
	AdvertisedRoutes    []string `json:"advertisedRoutes"`
	ApprovedRoutes      []string `json:"approvedRoutes"`
	ExitNodeCapable     bool     `json:"exitNodeCapable"`
	ExitNodeApproved    bool     `json:"exitNodeApproved"`
	UsingExitNodePeerID string   `json:"usingExitNodePeerId,omitempty"`
	// ConnectionPath is "direct" / "relay" / "unknown" (issue #138).
	// Reported by the peer on heartbeat; nil on the wire when the
	// peer hasn't reported yet. The Web UI normalizes nil → "unknown"
	// and shows ⚡ direct / 🔄 relay icons in the status column.
	ConnectionPath      *string    `json:"connectionPath,omitempty"`
	ConnectionPathAt    *time.Time `json:"connectionPathAt,omitempty"`
	ConnectionLatencyMs *int32     `json:"connectionLatencyMs,omitempty"`
}

// emptyIfNil normalizes a nil []string to an empty slice so the
// JSON encoder emits "[]" rather than "null" for the routes
// arrays (issue #136/#137). Empty array is the v1 normal —
// peers without --advertise-routes carry empty slices.
func emptyIfNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
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
		PeerDNSName:        p.PeerDNSName,
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
		OwnerEmail:         p.OwnerEmail,
		OwnerDisplayName:   p.OwnerDisplayName,
		ApprovalStatus:     p.ApprovalStatus,
		ApprovedAt:         p.ApprovedAt,
		AdvertisedRoutes:   emptyIfNil(p.AdvertisedRoutes),
		ApprovedRoutes:     emptyIfNil(p.ApprovedRoutes),
		ExitNodeCapable:    p.ExitNodeCapable,
		ExitNodeApproved:   p.ExitNodeApproved,
		UsingExitNodePeerID: func() string {
			if p.UsingExitNodePeerID == nil {
				return ""
			}
			return p.UsingExitNodePeerID.String()
		}(),
		ConnectionPath:      p.ConnectionPath,
		ConnectionPathAt:    p.ConnectionPathAt,
		ConnectionLatencyMs: p.ConnectionLatencyMs,
		// ApprovedByEmail intentionally left empty here — the
		// admin's email lives on users, and we don't LEFT JOIN
		// twice in the peer queries. The pending-list UI doesn't
		// need it (no admin has acted yet by definition); the
		// approved-peer UI shows the originally-approving admin
		// only if a future query joins users a second time.
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
	Hostname *string `json:"hostname,omitempty"`
	// DNSName lets an admin rename the MagicDNS label. nil = absent
	// (preserve current value). Non-nil pointer with empty string =
	// explicit clear (peer becomes unresolvable until the next
	// register re-runs the auto-slug). Non-nil with a value goes
	// through strict slug validation + collision check.
	DNSName *string   `json:"dnsName,omitempty"`
	Status  *string   `json:"status,omitempty"`
	Tags    *[]string `json:"tags,omitempty"`
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
	// dnsName: nil = absent (preserve), pointer-to-empty = clear,
	// pointer-to-value = must be a clean slug + not already taken
	// by another peer in the tenant. Strict-validate rather than
	// auto-slugify so an admin who types "Mac Mini" gets a clear
	// 400 instead of silently being given "mac-mini".
	var (
		setDNSName bool
		newDNSPtr  *string
	)
	if req.DNSName != nil {
		setDNSName = true
		trimmed := strings.TrimSpace(*req.DNSName)
		if trimmed != "" {
			if trimmed != handlers.SlugifyHostname(trimmed) {
				writeError(w, http.StatusBadRequest, errors.New("dnsName must be lowercase, [a-z0-9-], no leading/trailing dash"))
				return
			}
			if len(trimmed) > 63 {
				writeError(w, http.StatusBadRequest, errors.New("dnsName exceeds 63 chars"))
				return
			}
			taken, terr := h.peers.IsDNSNameTaken(r.Context(), tenant.ID, trimmed)
			if terr != nil {
				writeError(w, http.StatusInternalServerError, fmt.Errorf("check dnsName: %w", terr))
				return
			}
			// Skip the conflict when the row already holds this exact
			// name — re-PATCHing to the same value should be idempotent,
			// not a 409.
			if taken && (current.PeerDNSName == nil || *current.PeerDNSName != trimmed) {
				writeError(w, http.StatusConflict, fmt.Errorf("dnsName %q is already used by another peer in this tenant", trimmed))
				return
			}
			newDNSPtr = &trimmed
		}
		// trimmed == "" => newDNSPtr stays nil; that's the explicit-
		// clear path. The partial unique index treats NULL rows as
		// absent so the column can collide-free transition through
		// NULL to a new value.
	}

	diff := map[string]any{}

	if req.Hostname != nil && newHostname != current.Hostname {
		if _, err := h.peers.UpdateHostname(r.Context(), id, newHostname); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("update hostname: %w", err))
			return
		}
		diff["hostname"] = map[string]string{"from": current.Hostname, "to": newHostname}
	}
	if setDNSName {
		curStr := ""
		if current.PeerDNSName != nil {
			curStr = *current.PeerDNSName
		}
		newStr := ""
		if newDNSPtr != nil {
			newStr = *newDNSPtr
		}
		if curStr != newStr {
			if _, err := h.peers.UpdateDNSName(r.Context(), id, newDNSPtr); err != nil {
				writeError(w, http.StatusInternalServerError, fmt.Errorf("update dnsName: %w", err))
				return
			}
			diff["peer_dns_name"] = map[string]string{"from": curStr, "to": newStr}
		}
	}
	if req.Status != nil && *req.Status != current.Status {
		// Delegate to the coordinator so the WatchPeers event for
		// the status change is published alongside the DB write. The
		// idempotency check inside SetPeerStatus duplicates the
		// outer `*req.Status != current.Status` guard; both are
		// cheap and keep the handlers honest if either side gets
		// reordered later.
		if _, _, err := h.coord.SetPeerStatus(r.Context(), id, *req.Status); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("set status: %w", err))
			return
		}
		diff["status"] = map[string]string{"from": current.Status, "to": *req.Status}
	}
	if req.Tags != nil {
		// Tag-owners RBAC (issue #139). When the tenant policy
		// declares a tagOwners block, an admin can only assign /
		// remove tags whose owner list includes their email (or
		// the "*" wildcard). Dev-fallback (no claims) skips the
		// check — local `make local-up` doesn't have an admin
		// email to compare against, and require_auth=true is the
		// gate that keeps that path off the prod surface.
		if authn != nil && authn.claims != nil {
			if rej, err := h.checkTagAssignPermission(r.Context(), tenant.ID, authn.claims.UserID, current.Tags, *req.Tags); err != nil {
				writeError(w, http.StatusInternalServerError, fmt.Errorf("tag rbac: %w", err))
				return
			} else if rej != "" {
				writeError(w, http.StatusForbidden, fmt.Errorf("not allowed to assign or remove tag %q (tagOwners)", rej))
				return
			}
		}
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

	// Publish a WatchPeers event for non-status field changes
	// (hostname / dnsName / tags) so subscribers refresh their view
	// without waiting for the next heartbeat-driven register cycle.
	// Status transitions are skipped here because SetPeerStatus above
	// already published the appropriate event (PeerRemoved /
	// PeerAdded / PeerUpdated) with the post-update state — a second
	// PeerUpdated here would double-emit and, for the disabled
	// transition, would even mislead subscribers into re-adding a
	// peer the PeerRemoved just told them to drop.
	statusChanged := req.Status != nil && *req.Status != current.Status
	if len(diff) > 0 && !statusChanged {
		h.coord.PublishPeerUpdatedIfVisible(tenant.ID, updated)
	}

	writeJSON(w, http.StatusOK, peerToJSON(updated))
}

// checkTagAssignPermission enforces the policy.tagOwners contract
// (issue #139). Computes the (added, removed) tag delta between
// `current` and `next` and asks the tenant policy whether
// `userID`'s email may modify each. Returns the offending tag name
// when one is denied (caller turns it into a 403); empty string ⇒
// proceed. Internal errors (DB / users repo) are propagated as the
// err return.
//
// No policy authored, or policy has no tagOwners block ⇒ everything
// is allowed (preserves pre-#139 behavior). A tag named in tagOwners
// is restricted; one absent from tagOwners is unrestricted.
func (h *HTTPServer) checkTagAssignPermission(
	ctx context.Context,
	tenantID uuid.UUID,
	userID uuid.UUID,
	current []string,
	next []string,
) (string, error) {
	policyDoc, err := h.loadPolicyForTagRBAC(ctx, tenantID)
	if err != nil {
		return "", err
	}
	if policyDoc == nil || len(policyDoc.TagOwners) == 0 {
		return "", nil
	}
	user, err := h.users.GetByID(ctx, userID)
	if err != nil {
		// User row missing is treated as "permission denied" rather
		// than allow-all — the JWT should never outlive the user.
		return "", fmt.Errorf("lookup user: %w", err)
	}
	curSet := stringSet(current)
	nextSet := stringSet(next)
	// Added tags = nextSet \ curSet; removed = curSet \ nextSet.
	// Both directions need permission — admin can't remove a tag
	// they couldn't have added. Tag names are stored on the peer
	// without the "tag:" prefix (peer.tags is a bare list) but
	// the policy keys carry the prefix because that's how rules
	// reference them ("tag:dev"). Normalize before the lookup.
	for t := range diffSet(nextSet, curSet) {
		if !policyDoc.CanAssignTag(prefixTag(t), user.Email) {
			return t, nil
		}
	}
	for t := range diffSet(curSet, nextSet) {
		if !policyDoc.CanAssignTag(prefixTag(t), user.Email) {
			return t, nil
		}
	}
	return "", nil
}

// prefixTag normalizes a bare peer-tag value into the canonical
// "tag:<value>" form policy keys use. Existing "tag:" prefixes are
// preserved so the helper is idempotent.
func prefixTag(t string) string {
	if strings.HasPrefix(t, "tag:") {
		return t
	}
	return "tag:" + t
}

// loadPolicyForTagRBAC is the same code path the data-plane enforcer
// uses to fetch + parse the current policy, but here we only need
// the parsed shape (no revision tracking). Errors degrade to nil so
// a borked policy row doesn't block tag PATCHes entirely; the data
// plane already logs the parse failure.
func (h *HTTPServer) loadPolicyForTagRBAC(ctx context.Context, tenantID uuid.UUID) (*policy.Policy, error) {
	rec, err := h.policies.Get(ctx, tenantID)
	if errors.Is(err, repo.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parsed, perr := policy.Parse("policy.hcl", rec.HCLSource)
	if perr != nil {
		return nil, nil
	}
	return parsed, nil
}

func stringSet(s []string) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for _, v := range s {
		out[v] = struct{}{}
	}
	return out
}

func diffSet(a, b map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{})
	for k := range a {
		if _, in := b[k]; !in {
			out[k] = struct{}{}
		}
	}
	return out
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
// apiPeerApprove implements POST /api/v1/peers/{id}/approve (issue #133).
//
// Admin-only. Idempotent: re-approving an already-approved peer
// returns the same shape with `changed:false`. Rejecting an already-
// rejected peer is handled by apiPeerReject below — flipping rejected
// → approved is intentionally not supported here (the audit trail
// gets confusing if we let an admin un-reject through this endpoint;
// the workaround is to delete + re-register).
func (h *HTTPServer) apiPeerApprove(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, idStr string) {
	if !h.requireAdmin(w, r, authn, tenant, "peer.approve") {
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
	// Pre-condition: can only approve from 'pending'. Approve()
	// returns rows=0 for already-approved (success, no event); but a
	// 'rejected' row should refuse explicitly so the admin sees a
	// clean 409 instead of silent no-op.
	if current.ApprovalStatus == "rejected" {
		writeError(w, http.StatusConflict, errors.New("peer is rejected; delete + re-register instead of un-rejecting"))
		return
	}
	updated, changed, err := h.coord.ApprovePeer(r.Context(), id, adminUserIDFromAuth(authn))
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("approve peer: %w", err))
		return
	}
	if changed {
		writePeerAudit(r.Context(), h.audits, authn, tenant.ID, id, "peer.approve", map[string]any{
			"hostname":        updated.Hostname,
			"approval_status": map[string]any{"from": "pending", "to": "approved"},
		})
	}
	writeJSON(w, http.StatusOK, peerToJSON(updated))
}

// apiPeerReject implements POST /api/v1/peers/{id}/reject (issue #133).
//
// Admin-only. Sibling of apiPeerApprove: same shape, opposite
// outcome. Rejected peers are NOT deleted — the row is retained so
// the audit timeline has somewhere to land. Admin can follow up with
// DELETE /api/v1/peers/{id} to free the pubkey for re-registration.
func (h *HTTPServer) apiPeerReject(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, idStr string) {
	if !h.requireAdmin(w, r, authn, tenant, "peer.reject") {
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
	if current.ApprovalStatus == "approved" {
		writeError(w, http.StatusConflict, errors.New("peer is already approved; use DELETE to remove it"))
		return
	}
	updated, changed, err := h.coord.RejectPeer(r.Context(), id, adminUserIDFromAuth(authn))
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("reject peer: %w", err))
		return
	}
	if changed {
		writePeerAudit(r.Context(), h.audits, authn, tenant.ID, id, "peer.reject", map[string]any{
			"hostname":        updated.Hostname,
			"approval_status": map[string]any{"from": "pending", "to": "rejected"},
		})
	}
	writeJSON(w, http.StatusOK, peerToJSON(updated))
}

// adminUserIDFromAuth extracts the JWT user ID for the admin who is
// driving an approve / reject action. requireAdmin allows the
// dev-fallback path (no JWT) to pass through with a warn log; in
// that case we return nil so the repo layer writes
// approved_by_user_id = NULL rather than synthesizing a zero UUID
// that the users(id) FK would reject.
func adminUserIDFromAuth(authn *authnContext) *uuid.UUID {
	if authn == nil || authn.claims == nil {
		return nil
	}
	uid := authn.claims.UserID
	return &uid
}

// apiPeerSetApprovedRoutesReq is the request shape for the
// admin-approve subnet routes handler (issue #136).
type apiPeerSetApprovedRoutesReq struct {
	Routes []string `json:"routes"`
}

// apiPeerSetApprovedRoutes implements POST /api/v1/peers/{id}/routes
// (issue #136). Admin replaces the peer's approved_routes list with
// the explicit subset they sign off on. The list MUST be a subset
// of the peer's advertised_routes — admin can't conjure CIDRs the
// peer didn't ask to expose. Empty list ⇒ revoke all.
//
// CIDR validation is light here: just netip.ParsePrefix sanity. The
// admin is trusted to pick non-overlapping ranges; conflict
// detection between peers is a v2 follow-up.
func (h *HTTPServer) apiPeerSetApprovedRoutes(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, idStr string) {
	if !h.requireAdmin(w, r, authn, tenant, "peer.routes.approve") {
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
	var req apiPeerSetApprovedRoutesReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	// Validate CIDR shapes + the subset-of-advertised constraint.
	advertised := make(map[string]struct{}, len(current.AdvertisedRoutes))
	for _, c := range current.AdvertisedRoutes {
		advertised[c] = struct{}{}
	}
	for _, cidr := range req.Routes {
		if _, perr := netip.ParsePrefix(cidr); perr != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid CIDR %q: %w", cidr, perr))
			return
		}
		if _, ok := advertised[cidr]; !ok {
			writeError(w, http.StatusBadRequest, fmt.Errorf("route %q is not advertised by peer", cidr))
			return
		}
	}
	if err := h.peers.SetApprovedRoutes(r.Context(), id, req.Routes); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("set approved_routes: %w", err))
		return
	}
	// #170: bump policy_revision + publish PolicyChanged so peers
	// re-pull allowed_ips without a manual reconnect.
	if _, err := h.coord.BumpPolicyRevision(r.Context(), tenant.ID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("bump policy revision: %w", err))
		return
	}
	writePeerAudit(r.Context(), h.audits, authn, tenant.ID, id, "peer.routes.approve", map[string]any{
		"hostname": current.Hostname,
		"approved_routes": map[string]any{
			"from": current.ApprovedRoutes,
			"to":   req.Routes,
		},
	})
	updated, err := h.peers.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, peerToJSON(updated))
}

// apiPeerSetExitNodeApprovedReq is the request shape for
// approving/revoking exit-node role (issue #137).
type apiPeerSetExitNodeApprovedReq struct {
	Approved bool `json:"approved"`
}

// apiPeerSetExitNodeApproved implements POST /api/v1/peers/{id}/exit-node
// (issue #137). Admin flips the peer's exit_node_approved bit.
// Setting true requires the peer to also be exit_node_capable —
// approving an exit node that doesn't claim to support the role
// would be misleading.
func (h *HTTPServer) apiPeerSetExitNodeApproved(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, idStr string) {
	if !h.requireAdmin(w, r, authn, tenant, "peer.exit-node.approve") {
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
	var req apiPeerSetExitNodeApprovedReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	if req.Approved && !current.ExitNodeCapable {
		writeError(w, http.StatusBadRequest, errors.New("peer is not exit-node-capable; client must register with --advertise-exit-node first"))
		return
	}
	if err := h.peers.SetExitNodeApproved(r.Context(), id, req.Approved); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("set exit_node_approved: %w", err))
		return
	}
	// #170: bump policy_revision + publish PolicyChanged so peers
	// re-pull allowed_ips without a manual reconnect.
	if _, err := h.coord.BumpPolicyRevision(r.Context(), tenant.ID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("bump policy revision: %w", err))
		return
	}
	writePeerAudit(r.Context(), h.audits, authn, tenant.ID, id, "peer.exit-node.approve", map[string]any{
		"hostname": current.Hostname,
		"exit_node_approved": map[string]any{
			"from": current.ExitNodeApproved,
			"to":   req.Approved,
		},
	})
	updated, err := h.peers.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, peerToJSON(updated))
}

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
// apiLogJSON is one connection-event row from /api/v1/logs. Maps
// 1:1 to clickhouse.EvaluationTrace — every policy evaluation the
// controller wrote during peer registration / heartbeat. The Web
// UI renders this as a Tailscale-style /admin/logs timeline.
type apiLogJSON struct {
	ID            string    `json:"id"`
	OccurredAt    time.Time `json:"occurredAt"`
	Source        string    `json:"source"`
	Destination   string    `json:"destination"`
	Port          uint16    `json:"port"`
	Protocol      string    `json:"protocol"`
	Action        string    `json:"action"` // "allow" | "deny"
	MatchedRuleID string    `json:"matchedRuleId,omitempty"`
}

// apiLogs returns recent policy-evaluation traces for the tenant.
// Query params:
//
//	?sinceHours=N  (default 24) — window backward from now
//	?limit=N       (default 100, cap 500) — max rows
//
// Member-readable (any authed caller in the tenant can see what was
// allowed / denied); writes don't exist for this resource. When
// ClickHouse is unconfigured the Traces.ListRecent path returns nil
// and we ship an empty list — UI renders the empty state rather
// than a hard error, so dev environments without ClickHouse remain
// usable.
func (h *HTTPServer) apiLogs(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant) {
	sinceHours := 24
	if v := r.URL.Query().Get("sinceHours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 7*24 {
			sinceHours = n
		}
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	since := time.Now().UTC().Add(-time.Duration(sinceHours) * time.Hour)

	events, err := h.traces.ListRecent(r.Context(), tenant.ID, since, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]apiLogJSON, 0, len(events))
	for _, e := range events {
		out = append(out, apiLogJSON{
			ID:            e.ID.String(),
			OccurredAt:    e.OccurredAt,
			Source:        e.Source,
			Destination:   e.Destination,
			Port:          e.Port,
			Protocol:      e.Protocol,
			Action:        e.Action,
			MatchedRuleID: e.MatchedRuleID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": out})
}

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
	// AutoApprove opts the minted key out of the device-approval
	// queue (issue #133). Defaults false so the safer manual-approval
	// path is the path of least resistance; CI / kiosk admins flip
	// this to true when they want fleets of identical runners to
	// onboard without per-device clicks.
	AutoApprove bool `json:"autoApprove,omitempty"`
}

// apiPreAuthKeyJSON is the response shape. Secret is the plaintext
// value the user types into a bamboo client; it's shown ONCE, here,
// and never again — the DB stores only the bcrypt hash.
type apiPreAuthKeyJSON struct {
	ID          string    `json:"id"`
	Description string    `json:"description,omitempty"`
	Reusable    bool      `json:"reusable"`
	Ephemeral   bool      `json:"ephemeral"`
	AutoApprove bool      `json:"autoApprove"`
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
		AutoApprove: req.AutoApprove,
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
			"description":  created.Description,
			"reusable":     created.Reusable,
			"ephemeral":    created.Ephemeral,
			"auto_approve": created.AutoApprove,
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
		AutoApprove: created.AutoApprove,
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
	AutoApprove bool       `json:"autoApprove"`
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
		AutoApprove: k.AutoApprove,
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

// apiPutPolicyReq mirrors the gRPC PutPolicy contract. expectedRevision
// is optional optimistic-concurrency control: when non-zero, the
// repo's Put returns ErrRevisionMismatch if the persisted revision
// has advanced under us. The UI either supplies the revision from
// the row it loaded, or sends 0 to force-write.
type apiPutPolicyReq struct {
	HCLSource        string `json:"hclSource"`
	ExpectedRevision int64  `json:"expectedRevision,omitempty"`
}

// apiPutPolicy validates + persists a new policy revision. Admin-only.
// HCL is parsed BEFORE the DB write so invalid policies surface as
// 400 with the parser error (the Web ACL editor renders it inline).
// The events-bus PolicyChanged notification is NOT published here —
// the gRPC PutPolicy is what real clients hit and already publishes;
// keeping the REST side simple avoids a dual-publish race when both
// surfaces run for the same tenant.
func (h *HTTPServer) apiPutPolicy(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) {
	if !h.requireAdmin(w, r, authn, tenant, "policy.update") {
		return
	}
	var req apiPutPolicyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	parsed, parseErr := policy.Parse("policy.hcl", req.HCLSource)
	if parseErr != nil {
		// 400 with the bare parser message so the editor can show line /
		// column info verbatim.
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid policy: %v", parseErr))
		return
	}

	var updatedBy *uuid.UUID
	if authn != nil && authn.claims != nil {
		uid := authn.claims.UserID
		updatedBy = &uid
	}

	rec, err := h.policies.Put(r.Context(), tenant.ID, req.HCLSource, updatedBy, req.ExpectedRevision)
	if errors.Is(err, repo.ErrRevisionMismatch) {
		// 409 means "someone else saved while you were editing".
		// The Web UI should refetch and ask the operator to merge.
		writeError(w, http.StatusConflict, fmt.Errorf("expected_revision does not match current"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if h.audits != nil {
		_ = h.audits.Insert(r.Context(), &repo.AuditEvent{
			TenantID:     &tenant.ID,
			ActorType:    "user",
			ActorID:      pointerIfNonZero(claimsUserID(authn)),
			Action:       "policy.update",
			ResourceType: "policy",
			Diff: marshalDiffJSON(map[string]any{
				"revision":  rec.Revision,
				"rules":     len(parsed.Rules),
				"hcl_bytes": len(rec.HCLSource),
			}),
		})
	}

	rules := []apiACLRuleJSON{}
	for _, ru := range parsed.Rules {
		rules = append(rules, apiACLRuleJSON{
			ID:           ru.ID,
			Action:       ru.Action.String(),
			Description:  ru.Description,
			Sources:      formatPolicySources(ru.Sources),
			Destinations: formatPolicyDestinations(ru.Destinations),
		})
	}
	writeJSON(w, http.StatusOK, apiPolicyJSON{
		TenantID:  rec.TenantID.String(),
		Revision:  rec.Revision,
		HCLSource: rec.HCLSource,
		UpdatedAt: &rec.UpdatedAt,
		Rules:     rules,
	})
}

// apiValidatePolicy parses a candidate HCL body and reports whether
// the controller would accept it on a PUT (issue #134). No DB write
// happens — this powers the editor's "Validate" affordance so an
// operator can iterate against the controller's parser before they
// commit a revision that bumps every client's heartbeat.
//
// Admin-only on the wire: the parser is cheap but a non-admin should
// not be able to probe HCL syntax errors against the controller's
// version; that's a small leak vector for inferring parser internals.
// Returns 200 with {ok:true,rules:N} on success; 400 with
// {error: "<parser message>"} on failure.
func (h *HTTPServer) apiValidatePolicy(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) {
	if !h.requireAdmin(w, r, authn, tenant, "policy.validate") {
		return
	}
	var req apiPutPolicyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	parsed, parseErr := policy.Parse("policy.hcl", req.HCLSource)
	if parseErr != nil {
		// Mirror apiPutPolicy's contract: 400 with the bare message
		// so the editor can show line/column info verbatim. No audit
		// row — validate is a no-op on the data plane.
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid policy: %v", parseErr))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"rules": len(parsed.Rules),
	})
}

// apiSimulatePolicyReq is the request shape for the "Preview" tab.
// HCLSource is the draft the operator is considering; the simulator
// parses it locally + evaluates the allow/deny matrix against the
// current peer registry and returns the per-pair verdict.
type apiSimulatePolicyReq struct {
	HCLSource string `json:"hclSource"`
}

// apiSimulatePolicyEdge is one (src, dst, allow) triple in the
// simulator response. We surface IDs + hostnames so the UI can
// render a readable matrix without a second round-trip; allowedIps
// is included so the same compile path the data plane uses (issue
// #132) doubles as the explanation here.
type apiSimulatePolicyEdge struct {
	SourceID       string   `json:"sourceId"`
	SourceHostname string   `json:"sourceHostname"`
	DestID         string   `json:"destId"`
	DestHostname   string   `json:"destHostname"`
	Allow          bool     `json:"allow"`
	AllowedIPs     []string `json:"allowedIps,omitempty"`
}

// apiSimulatePolicyResp packages the matrix together with the parsed
// rule count so the editor can title the preview ("4 rules · 12 of
// 20 pairs allowed"). Peers field carries the IDs the matrix is
// indexed on so the renderer doesn't need a separate peer fetch.
type apiSimulatePolicyResp struct {
	Rules int                     `json:"rules"`
	Edges []apiSimulatePolicyEdge `json:"edges"`
}

// apiSimulatePolicy evaluates a draft HCL policy against the
// tenant's current peer registry and returns the per-(src,dst)
// verdict (issue #134). Admin-only — the matrix is membership-
// inferring.
//
// Self-pairs (src == dst) are omitted; the simulator answers "does
// the policy let peer A reach peer B" for A != B.
func (h *HTTPServer) apiSimulatePolicy(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) {
	if !h.requireAdmin(w, r, authn, tenant, "policy.simulate") {
		return
	}
	var req apiSimulatePolicyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	parsed, parseErr := policy.Parse("policy.hcl", req.HCLSource)
	if parseErr != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid policy: %v", parseErr))
		return
	}
	peers, err := h.peers.ListByTenant(r.Context(), tenant.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("list peers: %w", err))
		return
	}
	// Only approved peers participate in the mesh (issue #133), so
	// only approved peers participate in the simulator. Pending /
	// rejected rows would skew the matrix toward "you can't reach
	// anyone" without explaining why.
	approved := make([]*repo.Peer, 0, len(peers))
	for _, p := range peers {
		if p.ApprovalStatus == "approved" {
			approved = append(approved, p)
		}
	}
	resp := apiSimulatePolicyResp{
		Rules: len(parsed.Rules),
		Edges: make([]apiSimulatePolicyEdge, 0, len(approved)*len(approved)),
	}
	for _, src := range approved {
		for _, dst := range approved {
			if src.ID == dst.ID {
				continue
			}
			edge := apiSimulatePolicyEdge{
				SourceID:       src.ID.String(),
				SourceHostname: src.Hostname,
				DestID:         dst.ID.String(),
				DestHostname:   dst.Hostname,
				Allow:          simulateAllow(parsed, src, dst),
			}
			if edge.Allow {
				edge.AllowedIPs = []string{dst.IP + dstPrefixSuffix(dst.IP)}
			}
			resp.Edges = append(resp.Edges, edge)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// simulateAllow is a thin wrapper that maps repo.Peer onto the
// policy.PeerView the evaluator wants, keeping the api-side handler
// readable. Duplicates handlers/coordinator.go:peerView intentionally
// — that one is hot-path (called on every Register) and lives in the
// handlers package; this one is admin-tooling and would create an
// awkward import cycle if reused across.
func simulateAllow(p *policy.Policy, src, dst *repo.Peer) bool {
	srcView := policy.PeerView{Tags: src.Tags}
	dstView := policy.PeerView{Tags: dst.Tags}
	if addr, err := netip.ParseAddr(src.IP); err == nil {
		srcView.IP = addr
	}
	if addr, err := netip.ParseAddr(dst.IP); err == nil {
		dstView.IP = addr
	}
	return policy.Allow(p, srcView, dstView)
}

// dstPrefixSuffix picks /32 for v4 and /128 for v6 so the simulator
// renders allowed_ips the same way the coordinator's enforcement
// path does.
func dstPrefixSuffix(ip string) string {
	if addr, err := netip.ParseAddr(ip); err == nil && addr.Is6() {
		return "/128"
	}
	return "/32"
}

// apiHistoryRecordJSON is one row of the Versions tab.
type apiHistoryRecordJSON struct {
	Revision       int64     `json:"revision"`
	HCLSource      string    `json:"hclSource"`
	AppliedAt      time.Time `json:"appliedAt"`
	AppliedByEmail string    `json:"appliedByEmail,omitempty"`
}

// apiPolicyRevisions lists past policy revisions for the Web UI's
// Versions tab (issue #134). Admin-only. Returns the 20 most-recent
// rows, newest first.
func (h *HTTPServer) apiPolicyRevisions(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) {
	if !h.requireAdmin(w, r, authn, tenant, "policy.revisions") {
		return
	}
	rows, err := h.policies.ListHistory(r.Context(), tenant.ID, 20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]apiHistoryRecordJSON, 0, len(rows))
	for _, h := range rows {
		out = append(out, apiHistoryRecordJSON{
			Revision:       h.Revision,
			HCLSource:      h.HCLSource,
			AppliedAt:      h.AppliedAt,
			AppliedByEmail: h.AppliedByEmail,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": out})
}

// apiRollbackPolicyReq targets a specific past revision. The rollback
// is implemented as a Put of that revision's HCL — a new revision
// gets minted, the audit row records the rollback, and the history
// preserves both the original revision and the rollback row.
type apiRollbackPolicyReq struct {
	Revision int64 `json:"revision"`
}

// apiPolicyRollback re-applies a past revision as a fresh current
// (issue #134). Conceptually "restore", implementation-wise just a
// Put of the older HCL source. Admin-only.
//
// Why a new revision instead of swapping the current row's revision
// number back: the history table is append-only and the rollback's
// timestamp/actor must record who pressed the button, not who
// originally authored the rule. Treating rollback as "write the old
// HCL as if it were new" makes the audit trail honest.
func (h *HTTPServer) apiPolicyRollback(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) {
	if !h.requireAdmin(w, r, authn, tenant, "policy.rollback") {
		return
	}
	var req apiRollbackPolicyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	if req.Revision <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("revision must be positive"))
		return
	}
	rows, err := h.policies.ListHistory(r.Context(), tenant.ID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var target *repo.HistoryRecord
	for _, h := range rows {
		if h.Revision == req.Revision {
			target = h
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("revision %d not found in history", req.Revision))
		return
	}
	var updatedBy *uuid.UUID
	if authn != nil && authn.claims != nil {
		uid := authn.claims.UserID
		updatedBy = &uid
	}
	// expectedRevision=0 because the operator's intent is "make this
	// the current, regardless of what got written since they opened
	// the Versions tab". The audit row makes the override visible.
	rec, err := h.policies.Put(r.Context(), tenant.ID, target.HCLSource, updatedBy, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("rollback put: %w", err))
		return
	}
	if h.audits != nil {
		_ = h.audits.Insert(r.Context(), &repo.AuditEvent{
			TenantID:     &tenant.ID,
			ActorType:    "user",
			ActorID:      pointerIfNonZero(claimsUserID(authn)),
			Action:       "policy.rollback",
			ResourceType: "policy",
			Diff: marshalDiffJSON(map[string]any{
				"rolled_back_to": target.Revision,
				"new_revision":   rec.Revision,
			}),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"revision":    rec.Revision,
		"appliedFrom": target.Revision,
		"appliedAt":   rec.UpdatedAt,
	})
}

// claimsUserID returns the claimed user uuid or uuid.Nil when authn
// is empty (dev-fallback path). Mirrors pointerIfNonZero's
// boilerplate-avoidance role for the audit-row callers.
func claimsUserID(authn *authnContext) uuid.UUID {
	if authn == nil || authn.claims == nil {
		return uuid.Nil
	}
	return authn.claims.UserID
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

// apiInvitationJSON is the wire shape for /api/v1/invitations rows.
// Mint response includes the plaintext token; subsequent list reads
// omit it (we only have the bcrypt hash on disk). Status is derived
// in the renderer from accepted_at / revoked_at / expires_at — same
// pattern as PreAuthKeys.
type apiInvitationJSON struct {
	ID         string     `json:"id"`
	Email      string     `json:"email"`
	IsAdmin    bool       `json:"isAdmin,omitempty"`
	InvitedBy  string     `json:"invitedBy,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	AcceptedAt *time.Time `json:"acceptedAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	// Token is set only on the mint response. The plaintext is shown
	// once to the inviter, then thrown away — only the bcrypt hash
	// persists. List rows leave this empty.
	Token string `json:"token,omitempty"`
	// EmailSent reports whether the invitation email was delivered
	// to the relay. False either because SMTP is unconfigured (no-op
	// sender) or delivery failed; in both cases the admin can copy
	// Token and share it out-of-band. Set only on the mint response.
	EmailSent bool `json:"emailSent,omitempty"`
}

type apiCreateInvitationReq struct {
	Email   string `json:"email"`
	IsAdmin bool   `json:"isAdmin,omitempty"`
	TTLDays int    `json:"ttlDays,omitempty"` // optional; default 7
}

// apiCreateInvitation mints a tenant invitation. Admin-only. No email
// delivery yet — this Phase A stub stores the row and returns the
// plaintext token in the response so an admin can deliver it out-of-
// band (Slack DM, paste). SMTP integration + a /invitations/{id}/
// accept endpoint land in follow-ups.
func (h *HTTPServer) apiCreateInvitation(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) {
	if !h.requireAdmin(w, r, authn, tenant, "user.invite") {
		return
	}
	var req apiCreateInvitationReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	if req.Email == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("email is required"))
		return
	}
	ttl := 7
	if req.TTLDays > 0 {
		ttl = req.TTLDays
	}
	expiresAt := time.Now().Add(time.Duration(ttl) * 24 * time.Hour)

	id := uuid.New()
	plaintext, hash, err := auth.GenerateInviteToken(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("generate token: %w", err))
		return
	}

	var invitedBy *uuid.UUID
	if authn != nil && authn.claims != nil {
		uid := authn.claims.UserID
		invitedBy = &uid
	}

	inv := &repo.UserInvitation{
		TenantID:  tenant.ID,
		Email:     req.Email,
		IsAdmin:   req.IsAdmin,
		TokenHash: hash,
		InvitedBy: invitedBy,
		ExpiresAt: expiresAt,
	}
	// Pre-assign the ID so it matches the token's embedded id; Create
	// will return the persisted row (id columns will match).
	inv.ID = id
	created, err := h.invitations.Create(r.Context(), inv)
	if err != nil {
		// Duplicate active invitation surfaces as a unique-index
		// violation. Surface as 409 so the UI can render "already
		// invited" without polling.
		writeError(w, http.StatusConflict, fmt.Errorf("create invitation: %w", err))
		return
	}
	out := invitationToJSON(created)
	out.Token = plaintext

	// Best-effort email delivery. Failures don't block the response —
	// the admin always has the plaintext token in `out.Token` to copy
	// + share manually. EmailSent on the wire lets the UI render the
	// right copy ("we sent them an email" vs "copy this token").
	if h.mailer != nil && h.mailer.Enabled() {
		inviteURL := h.inviteLinkFor(plaintext)
		inviterEmail := ""
		if invitedBy != nil {
			if u, ferr := h.users.GetByID(r.Context(), *invitedBy); ferr == nil {
				inviterEmail = u.Email
			}
		}
		sent, err := h.mailer.SendInvitation(created.Email, inviteURL, tenant.Slug, inviterEmail)
		if err != nil {
			slog.Warn("send invitation email", "err", err, "invitation_id", created.ID, "to", created.Email)
		}
		out.EmailSent = sent
	}

	writeJSON(w, http.StatusCreated, out)
}

// inviteLinkFor builds the /invite landing URL the email body links
// to. We prefer the SMTP-specific publicURL when set; otherwise fall
// back to the OIDC base URL (single-domain deploys make them the
// same value). Locale prefix isn't applied here — the Web's
// next-intl middleware handles language detection on the visit.
func (h *HTTPServer) inviteLinkFor(token string) string {
	base := h.publicURL
	if base == "" {
		base = h.baseURL
	}
	return fmt.Sprintf("%s/invite?token=%s", strings.TrimRight(base, "/"), token)
}

// apiListInvitations returns every invitation in the tenant. Admin-
// only. Token field is empty for every row — the plaintext is gone
// after the mint response is sent.
func (h *HTTPServer) apiListInvitations(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) {
	if !h.requireAdmin(w, r, authn, tenant, "user.invite.list") {
		return
	}
	invs, err := h.invitations.ListByTenant(r.Context(), tenant.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]apiInvitationJSON, 0, len(invs))
	for _, inv := range invs {
		out = append(out, invitationToJSON(inv))
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitations": out})
}

// apiRevokeInvitation flips a pending invitation to revoked. Admin-
// only. Idempotency: ErrNotFound from the repo means either the row
// is unknown OR it's already terminal (accepted / revoked). We
// surface that as 404 so the UI can fall back to a refetch; the
// distinction between "never existed" and "already terminal" isn't
// useful to the operator and would require an extra round-trip to
// disambiguate.
func (h *HTTPServer) apiRevokeInvitation(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, idStr string) {
	if !h.requireAdmin(w, r, authn, tenant, "user.invite.revoke") {
		return
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Cross-tenant guard: re-fetch the row and reject when it belongs
	// to a different tenant. Without this, an admin in tenant A could
	// revoke an invite in tenant B by guessing its id (low probability
	// since uuids, but defense-in-depth).
	inv, err := h.invitations.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if inv.TenantID != tenant.ID {
		http.NotFound(w, r)
		return
	}

	// MarkRevoked's WHERE only flips pending rows. Already-accepted or
	// already-revoked rows return ErrNotFound — we surface 404 so the
	// caller refetches and sees the current state.
	var actorID uuid.UUID
	if authn != nil && authn.claims != nil {
		actorID = authn.claims.UserID
	}
	if err := h.invitations.MarkRevoked(r.Context(), id, actorID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Audit row mirrors user.invite.accept; activity feed groups them.
	if h.audits != nil {
		_ = h.audits.Insert(r.Context(), &repo.AuditEvent{
			TenantID:     &tenant.ID,
			ActorType:    "user",
			ActorID:      pointerIfNonZero(actorID),
			Action:       "user.invite.revoke",
			ResourceType: "user_invitation",
			ResourceID:   &id,
			Diff:         marshalDiffJSON(map[string]any{"email": inv.Email}),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// pointerIfNonZero returns &id when id is non-zero, nil otherwise.
// Audit rows want NULL actor_id for dev-fallback callers (no JWT)
// rather than the zero uuid, so the activity feed doesn't render
// "actor: 00000000-..." for system-issued mutations.
func pointerIfNonZero(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// marshalDiffJSON is a thin wrapper around json.Marshal that drops
// the error — audit Diff is best-effort and we'd rather record a
// nil diff than fail the whole write.
func marshalDiffJSON(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func invitationToJSON(inv *repo.UserInvitation) apiInvitationJSON {
	invitedBy := ""
	if inv.InvitedBy != nil {
		invitedBy = inv.InvitedBy.String()
	}
	return apiInvitationJSON{
		ID:         inv.ID.String(),
		Email:      inv.Email,
		IsAdmin:    inv.IsAdmin,
		InvitedBy:  invitedBy,
		CreatedAt:  inv.CreatedAt,
		ExpiresAt:  inv.ExpiresAt,
		AcceptedAt: inv.AcceptedAt,
		RevokedAt:  inv.RevokedAt,
	}
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
