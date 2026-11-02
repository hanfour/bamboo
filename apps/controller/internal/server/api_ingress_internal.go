// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

// maxIngressDomainLen is the longest DNS name (RFC 1035). Anything longer
// is rejected before any string work or database lookup.
const maxIngressDomainLen = 253

// errIngressTokenRevoked covers every "valid signature, but no longer
// honoured" case: rotated out, or the ingress peer lost eligibility.
var errIngressTokenRevoked = errors.New("ingress token revoked")

// /internal/v1/ingress/* is the ingress ↔ controller surface
// (docs/design/2026-11-02-public-services-ingress.md §4.3). It is never
// routed by the public Caddy sites: the control-plane site sends only
// /api/*, /auth/*, /healthz, /install.sh to the controller, and both
// public sites answer /internal/* with 404.

type ingressRouteJSON struct {
	ServiceID  string `json:"service_id"`
	Label      string `json:"label"`
	PeerIP     string `json:"peer_ip"`
	TargetPort int    `json:"target_port"`
}

func (h *HTTPServer) routeInternalIngress(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/internal/v1/ingress/ask":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.internalIngressAsk(w, r)
	case "/internal/v1/ingress/routes":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.internalIngressRoutes(w, r)
	default:
		http.NotFound(w, r)
	}
}

// ingressLabelFromDomain extracts <label> from "<label>.<suffix>".
// Exactly one DNS label is allowed in front of the suffix. Case is folded
// and one trailing dot (FQDN form) is stripped; ingress_label_live only
// matches lowercase labels.
func ingressLabelFromDomain(domain, suffix string) (string, bool) {
	if suffix == "" || len(domain) > maxIngressDomainLen+1 {
		return "", false
	}
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	label, ok := strings.CutSuffix(d, "."+suffix)
	if !ok || label == "" || strings.Contains(label, ".") {
		return "", false
	}
	if repo.ValidateIngressLabel(label) != nil {
		return "", false
	}
	return label, true
}

// internalIngressAsk answers Caddy's on-demand TLS permission check. No
// token (Caddy's ask cannot send headers); it reveals one bit per name:
// a single boolean lookup, and 404 bodies never name a tenant or peer. A
// missing, oversized, or malformed domain is 404 without touching the
// database.
func (h *HTTPServer) internalIngressAsk(w http.ResponseWriter, r *http.Request) {
	label, ok := ingressLabelFromDomain(r.URL.Query().Get("domain"), h.ingressSuffix)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// RLS bootstrap: SECURITY DEFINER function, no tenant GUC needed.
	live, err := repo.NewIngressServices(h.pool).LabelLive(r.Context(), label)
	if err != nil {
		slog.Warn("ingress ask lookup", "label", label, "err", err)
		http.Error(w, "lookup failed", http.StatusServiceUnavailable)
		return
	}
	if !live {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte("ok"))
}

// authenticateIngress verifies the bearer ingress token, that its jti is
// the tenant's current one (rotation revokes the previous token), and
// that its peer is still an approved tag:ingress peer of that tenant.
// Writes the error response itself.
func (h *HTTPServer) authenticateIngress(w http.ResponseWriter, r *http.Request) (*auth.IngressClaims, bool) {
	if len(h.ingressSigningKey) == 0 {
		writeError(w, http.StatusServiceUnavailable, errors.New("public services ingress is not configured"))
		return nil, false
	}
	tok := bearerToken(r)
	if tok == "" {
		writeError(w, http.StatusUnauthorized, errors.New("ingress token required"))
		return nil, false
	}
	pub, _ := h.ingressSigningKey.Public().(ed25519.PublicKey)
	claims, err := auth.VerifyIngressToken(pub, tok)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errors.New("invalid ingress token"))
		return nil, false
	}
	err = db.WithTenant(r.Context(), h.pool, claims.TenantID, func(q db.Querier) error {
		return checkIngressTokenCurrent(r.Context(), q, claims)
	})
	if errors.Is(err, errIngressTokenRevoked) {
		writeError(w, http.StatusUnauthorized, errIngressTokenRevoked)
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return nil, false
	}
	return claims, true
}

// checkIngressTokenCurrent runs inside the claims' tenant tx. A missing
// row, a different jti or peer, or an ineligible peer is
// errIngressTokenRevoked.
func checkIngressTokenCurrent(ctx context.Context, q db.Querier, claims *auth.IngressClaims) error {
	current, err := repo.NewIngressTokens(q).Get(ctx, claims.TenantID)
	if errors.Is(err, repo.ErrNotFound) {
		return errIngressTokenRevoked
	}
	if err != nil {
		return err
	}
	if !uuidEqualConstTime(current.JTI, claims.JTI) || !uuidEqualConstTime(current.PeerID, claims.PeerID) {
		return errIngressTokenRevoked
	}
	peer, ok, err := getPeerInTenant(ctx, q, claims.TenantID, claims.PeerID)
	if err != nil {
		return err
	}
	if !ok || peer.ApprovalStatus != "approved" || !slices.Contains(peer.Tags, policy.IngressTag) {
		return errIngressTokenRevoked
	}
	return nil
}

func uuidEqualConstTime(a, b uuid.UUID) bool {
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// ingressETagMatches implements If-None-Match (RFC 9110 §13.1.2): a list
// of entity tags compared weakly, or "*".
func ingressETagMatches(header, etag string) bool {
	for _, cand := range strings.Split(header, ",") {
		cand = strings.TrimSpace(cand)
		if cand == "*" || strings.TrimPrefix(cand, "W/") == etag {
			return true
		}
	}
	return false
}

// internalIngressRoutes returns the token tenant's live routes. The ETag
// is a strong validator: a hash of the exact response bytes, so an
// unchanged table costs a 304 with no body.
func (h *HTTPServer) internalIngressRoutes(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticateIngress(w, r)
	if !ok {
		return
	}
	var routes []repo.IngressRoute
	err := db.WithTenant(r.Context(), h.pool, claims.TenantID, func(q db.Querier) error {
		rt, lerr := repo.NewIngressServices(q).ListLiveRoutes(r.Context(), claims.TenantID)
		routes = rt
		return lerr
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]ingressRouteJSON, 0, len(routes))
	for _, rt := range routes {
		out = append(out, ingressRouteJSON{ServiceID: rt.ServiceID.String(), Label: rt.Label, PeerIP: rt.PeerIP, TargetPort: rt.TargetPort})
	}
	body, err := json.Marshal(out)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if ingressETagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
