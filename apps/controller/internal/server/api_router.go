// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"net/http"
	"strings"

	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// This file holds the per-resource dispatch helpers that routeAPI (in
// api.go) delegates to. Each returns true iff it OWNS the request path
// (and has therefore written the response) so routeAPI can try them in
// order and fall through to the read-only + no-route handling. The
// dispatch order and every method/404/405 branch are byte-for-byte the
// same as the previous single-function if-chain — the split is purely to
// shrink the 4000-line api.go god-file. The full routing table is pinned
// by test/e2e/router_contract_test.go, so a regression in this dispatch
// (a dropped route, a shadowed method) fails CI.

// routePeersSub handles every /api/v1/peers/{id}[/subpath] request.
// Reserved exact paths (register / heartbeat / watch) are matched in
// routeAPI's early switch and never reach here.
func (h *HTTPServer) routePeersSub(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) bool {
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/peers/")
	if !ok || rest == "" {
		return false
	}
	// "{id}/events" — peer activity timeline.
	if id, found := strings.CutSuffix(rest, "/events"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiPeerEvents(w, r, tenant, id)
		return true
	}
	// "{id}/connection-events" — per-peer connection-path transition
	// timeline (issue #138 v2), backed by ClickHouse. Read-only.
	if id, found := strings.CutSuffix(rest, "/connection-events"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiPeerConnectionEvents(w, r, tenant, id)
		return true
	}
	// "{id}/route-conflicts" — informational approved-CIDR overlaps.
	if id, found := strings.CutSuffix(rest, "/route-conflicts"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiPeerRouteConflicts(w, r, tenant, id)
		return true
	}
	// "{id}/bandwidth" — cumulative bandwidth_sample time series.
	if id, found := strings.CutSuffix(rest, "/bandwidth"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiPeerBandwidth(w, r, tenant, id)
		return true
	}
	// "{id}/approve" — admin gates a pending peer into the mesh (#133).
	if id, found := strings.CutSuffix(rest, "/approve"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiPeerApprove(w, r, authn, tenant, id)
		return true
	}
	// "{id}/reject" — admin rejects a pending peer registration (#133).
	if id, found := strings.CutSuffix(rest, "/reject"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiPeerReject(w, r, authn, tenant, id)
		return true
	}
	// "{id}/routes" — admin approves a subset of advertised routes (#136).
	if id, found := strings.CutSuffix(rest, "/routes"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiPeerSetApprovedRoutes(w, r, authn, tenant, id)
		return true
	}
	// "{id}/use-exit-node" — admin selects the exit node this peer routes
	// through (the consume side of #137). Distinct suffix from
	// "/exit-node" ("/use-" vs "/"), so order is irrelevant.
	if id, found := strings.CutSuffix(rest, "/use-exit-node"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiPeerUseExitNode(w, r, authn, tenant, id)
		return true
	}
	// "{id}/exit-node" — admin approves/revokes the peer's exit-node role (#137).
	if id, found := strings.CutSuffix(rest, "/exit-node"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiPeerSetExitNodeApproved(w, r, authn, tenant, id)
		return true
	}
	// "{id}/nat64-egress" — admin approves/revokes the NAT64 egress role.
	if id, found := strings.CutSuffix(rest, "/nat64-egress"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiPeerSetNAT64EgressApproved(w, r, authn, tenant, id)
		return true
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
		return true
	}
	writeRouteMissing(w)
	return true
}

// routePreAuthKeys handles /api/v1/preauth-keys[/{id}/revoke].
func (h *HTTPServer) routePreAuthKeys(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) bool {
	if r.URL.Path == "/api/v1/preauth-keys" {
		switch r.Method {
		case http.MethodGet:
			h.apiListPreAuthKeys(w, r, authn, tenant)
		case http.MethodPost:
			h.apiCreatePreAuthKey(w, r, authn, tenant)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return true
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/preauth-keys/")
	if !ok || rest == "" {
		return false
	}
	if id, found := strings.CutSuffix(rest, "/revoke"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiRevokePreAuthKey(w, r, authn, tenant, id)
		return true
	}
	writeRouteMissing(w)
	return true
}

// routeInvitations handles /api/v1/invitations[/{id}/revoke]. Note the
// unknown-sub-path miss is http.NotFound here (not writeRouteMissing) —
// preserved deliberately to match the pre-refactor behavior.
func (h *HTTPServer) routeInvitations(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) bool {
	if r.URL.Path == "/api/v1/invitations" {
		switch r.Method {
		case http.MethodGet:
			h.apiListInvitations(w, r, authn, tenant)
		case http.MethodPost:
			h.apiCreateInvitation(w, r, authn, tenant)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return true
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/invitations/")
	if !ok || rest == "" {
		return false
	}
	if id, found := strings.CutSuffix(rest, "/revoke"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiRevokeInvitation(w, r, authn, tenant, id)
		return true
	}
	http.NotFound(w, r)
	return true
}

// routeAPITokens handles /api/v1/api-tokens[/{id}/revoke].
func (h *HTTPServer) routeAPITokens(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) bool {
	if r.URL.Path == "/api/v1/api-tokens" {
		switch r.Method {
		case http.MethodGet:
			h.apiListAPITokens(w, r, authn, tenant)
		case http.MethodPost:
			h.apiMintAPIToken(w, r, authn, tenant)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return true
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/api-tokens/")
	if !ok || rest == "" {
		return false
	}
	if id, found := strings.CutSuffix(rest, "/revoke"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiRevokeAPIToken(w, r, authn, tenant, id)
		return true
	}
	writeRouteMissing(w)
	return true
}

// routeWebhooks handles /api/v1/webhooks[/{id}[/test]].
func (h *HTTPServer) routeWebhooks(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) bool {
	if r.URL.Path == "/api/v1/webhooks" {
		switch r.Method {
		case http.MethodGet:
			h.apiListWebhooks(w, r, authn, tenant)
		case http.MethodPost:
			h.apiCreateWebhook(w, r, authn, tenant)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return true
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/webhooks/")
	if !ok || rest == "" {
		return false
	}
	// "{id}/test" — synchronous probe with inline outcome.
	if id, found := strings.CutSuffix(rest, "/test"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiTestWebhook(w, r, authn, tenant, id)
		return true
	}
	if !strings.Contains(rest, "/") {
		switch r.Method {
		case http.MethodPatch:
			h.apiPatchWebhook(w, r, authn, tenant, rest)
		case http.MethodDelete:
			h.apiDeleteWebhook(w, r, authn, tenant, rest)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return true
	}
	writeRouteMissing(w)
	return true
}

// routePolicy handles /api/v1/policy and the ACL-editor tooling paths
// (/policy/{validate,simulate,revisions,rollback}). These sit outside
// the GET-only read block so their non-GET bodies parse (#134).
func (h *HTTPServer) routePolicy(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) bool {
	switch r.URL.Path {
	case "/api/v1/policy":
		switch r.Method {
		case http.MethodGet:
			h.apiPolicy(w, r, tenant)
		case http.MethodPut:
			h.apiPutPolicy(w, r, authn, tenant)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return true
	case "/api/v1/policy/validate":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiValidatePolicy(w, r, authn, tenant)
		return true
	case "/api/v1/policy/simulate":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiSimulatePolicy(w, r, authn, tenant)
		return true
	case "/api/v1/policy/revisions":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiPolicyRevisions(w, r, authn, tenant)
		return true
	case "/api/v1/policy/rollback":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiPolicyRollback(w, r, authn, tenant)
		return true
	}
	return false
}

// routeReadOnlyAndDNS is the terminal dispatch: DNS (GET+PATCH), then the
// blanket GET-only guard, then the read-only collection switch, then the
// canonical no-route 404. It always handles the request.
func (h *HTTPServer) routeReadOnlyAndDNS(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) {
	// /api/v1/dns dispatches GET+PATCH inside apiDNS; must be routed
	// BEFORE the GET-only guard so the PATCH branch isn't shadowed (#252).
	if r.URL.Path == "/api/v1/dns" {
		h.apiDNS(w, r, authn, tenant)
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
	case "/api/v1/users":
		h.apiUsers(w, r, authn, tenant)
	case "/api/v1/logs":
		h.apiLogs(w, r, tenant)
	default:
		writeRouteMissing(w)
	}
}
