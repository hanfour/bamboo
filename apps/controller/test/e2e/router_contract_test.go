// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// TestRouterContract_AllRoutes pins the FULL /api/v1 routing table so a
// refactor of routeAPI (e.g. the manual CutPrefix/CutSuffix chain →
// net/http pattern mux) can't silently drop, shadow, or mis-method a
// route. It's the regression guard for the class of bug that already bit
// twice (#252 PATCH /dns shadowed by a GET-only guard; #253 a route that
// never surfaced a field).
//
// Assertions are on status CLASS only, never body text, so they hold for
// both the hand-rolled dispatch and a stdlib ServeMux (whose 404/405
// bodies differ):
//   - a route that SHOULD dispatch must not return 405 and must not
//     return the canonical "no route" 404.
//   - a known path hit with the wrong method must return 405.
//   - a genuinely unknown path must return the "no route" 404.
//
// The streaming watch endpoint is excluded — probing it would block on
// the open SSE stream.
func TestRouterContract_AllRoutes(t *testing.T) {
	f := startFixture(t)
	id := uuid.NewString()

	// Capture the fixture's "no route" signature dynamically so the
	// assertions don't hard-code the exact 404 body.
	noRoute := probeBody(t, f.httpURL+"/api/v1/__definitely_missing__", http.MethodGet, true)
	isNoRoute := func(p probeResult) bool {
		return p.status == http.StatusNotFound && bytes.Equal(p.bytes, noRoute.bytes)
	}

	type route struct {
		method string
		path   string
		body   bool // send a {} body
	}

	dispatched := []route{
		// Pre-auth peer lifecycle (watch excluded — it streams).
		{http.MethodPost, "/api/v1/peers/register", true},
		{http.MethodPost, "/api/v1/peers/heartbeat", true},
		// Peer {id} sub-paths.
		{http.MethodGet, "/api/v1/peers/" + id + "/events", false},
		{http.MethodGet, "/api/v1/peers/" + id + "/connection-events", false},
		{http.MethodGet, "/api/v1/peers/" + id + "/route-conflicts", false},
		{http.MethodGet, "/api/v1/peers/" + id + "/bandwidth", false},
		{http.MethodPost, "/api/v1/peers/" + id + "/approve", false},
		{http.MethodPost, "/api/v1/peers/" + id + "/reject", false},
		{http.MethodPost, "/api/v1/peers/" + id + "/routes", true},
		{http.MethodPost, "/api/v1/peers/" + id + "/exit-node", true},
		{http.MethodPost, "/api/v1/peers/" + id + "/use-exit-node", true},
		{http.MethodPost, "/api/v1/peers/" + id + "/nat64-egress", true},
		{http.MethodGet, "/api/v1/peers/" + id, false},
		{http.MethodPatch, "/api/v1/peers/" + id, true},
		{http.MethodDelete, "/api/v1/peers/" + id, false},
		// Collections + admin-write sub-paths.
		{http.MethodGet, "/api/v1/preauth-keys", false},
		{http.MethodPost, "/api/v1/preauth-keys", true},
		{http.MethodPost, "/api/v1/preauth-keys/" + id + "/revoke", false},
		{http.MethodGet, "/api/v1/invitations", false},
		{http.MethodPost, "/api/v1/invitations", true},
		{http.MethodPost, "/api/v1/invitations/" + id + "/revoke", false},
		{http.MethodGet, "/api/v1/api-tokens", false},
		{http.MethodPost, "/api/v1/api-tokens", true},
		{http.MethodPost, "/api/v1/api-tokens/" + id + "/revoke", false},
		{http.MethodGet, "/api/v1/webhooks", false},
		{http.MethodPost, "/api/v1/webhooks", true},
		{http.MethodPost, "/api/v1/webhooks/" + id + "/test", false},
		{http.MethodPatch, "/api/v1/webhooks/" + id, true},
		{http.MethodDelete, "/api/v1/webhooks/" + id, false},
		// Policy + tooling.
		{http.MethodGet, "/api/v1/policy", false},
		{http.MethodPut, "/api/v1/policy", true},
		{http.MethodPost, "/api/v1/policy/validate", true},
		{http.MethodPost, "/api/v1/policy/simulate", true},
		{http.MethodGet, "/api/v1/policy/revisions", false},
		{http.MethodPost, "/api/v1/policy/rollback", true},
		// DNS (GET + PATCH on the same path).
		{http.MethodGet, "/api/v1/dns", false},
		{http.MethodPatch, "/api/v1/dns", true},
		// Read-only collections.
		{http.MethodGet, "/api/v1/me", false},
		{http.MethodGet, "/api/v1/overview", false},
		{http.MethodGet, "/api/v1/peers", false},
		{http.MethodGet, "/api/v1/recommendations", false},
		{http.MethodGet, "/api/v1/activity", false},
		{http.MethodGet, "/api/v1/users", false},
		{http.MethodGet, "/api/v1/logs", false},
	}

	for _, r := range dispatched {
		t.Run("dispatch "+r.method+" "+r.path, func(t *testing.T) {
			got := probeBody(t, f.httpURL+r.path, r.method, !r.body)
			if got.status == http.StatusMethodNotAllowed {
				t.Errorf("405 — route not registered for %s (wrong verb or missing)", r.method)
			}
			if isNoRoute(got) {
				t.Errorf("canonical no-route 404 — route is missing entirely (status=%d)", got.status)
			}
		})
	}

	// Wrong method on a KNOWN path must be 405 (not a dispatch, not a
	// no-route 404). Covers the method-dispatch arms.
	wrongMethod := []route{
		{http.MethodGet, "/api/v1/peers/" + id + "/approve", false},       // POST-only
		{http.MethodDelete, "/api/v1/peers/" + id + "/routes", false},     // POST-only
		{http.MethodGet, "/api/v1/preauth-keys/" + id + "/revoke", false}, // POST-only
		{http.MethodPut, "/api/v1/peers", true},                           // GET-only collection
		{http.MethodPost, "/api/v1/me", true},                             // GET-only
		{http.MethodGet, "/api/v1/policy/validate", false},                // POST-only
		{http.MethodDelete, "/api/v1/policy", false},                      // GET/PUT only
	}
	for _, r := range wrongMethod {
		t.Run("405 "+r.method+" "+r.path, func(t *testing.T) {
			got := probeBody(t, f.httpURL+r.path, r.method, !r.body)
			if got.status != http.StatusMethodNotAllowed {
				t.Errorf("wrong-method %s %s: status=%d, want 405", r.method, r.path, got.status)
			}
		})
	}

	// A genuinely unknown path under /api/v1 must be the no-route 404.
	t.Run("unknown path is no-route 404", func(t *testing.T) {
		got := probeBody(t, f.httpURL+"/api/v1/nonexistent-collection", http.MethodGet, true)
		if !isNoRoute(got) {
			t.Errorf("unknown path: status=%d body=%q, want the canonical no-route 404", got.status, got.bytes)
		}
	})
}
