// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestWebRouteContract pins the wire-level contract between the Web
// app's lib/api.ts + lib/actions.ts and the controller's REST router.
// The doc's Finding #2 calls out the failure mode this guards: a Web
// PR adds a fetch to /api/v1/X, the controller never gets a matching
// route, typecheck doesn't catch it, and the page errors silently
// in production.
//
// Mechanism:
//   - Send each (method, path) the Web hits at the in-process
//     controller fixture.
//   - Accept any response code EXCEPT 405 (method not allowed —
//     route exists for some other method) and EXCEPT a "route
//     not found" 404. Handler-level 404s (resource missing under
//     a random UUID) are fine; the route still dispatched.
//
// Maintenance:
//   - Every new fetch in apps/web/src/lib/{api,actions}.ts should
//     get an entry below. The web side reviews this file in tandem
//     with the api.ts diff — the build pipeline can't catch the
//     missing route any other way.
//   - When the stacked Web train (users / invitations / logs PRs)
//     merges its backend counterparts, the commented-out entries
//     below should be uncommented.
func TestWebRouteContract(t *testing.T) {
	f := startFixture(t)

	// Use a syntactically valid UUID for {id} substitutions so the
	// handler's path parsing doesn't trip on a malformed id and
	// return its own 400/404 before route dispatch matters.
	dummyID := uuid.NewString()

	type probe struct {
		method string
		path   string
		// noBody = no JSON body. POST/PATCH without a body should
		// still dispatch (handlers reject as 400, not 404/405).
		noBody bool
	}
	probes := []probe{
		// lib/api.ts read paths.
		{method: http.MethodGet, path: "/api/v1/overview"},
		{method: http.MethodGet, path: "/api/v1/me"},
		{method: http.MethodGet, path: "/api/v1/peers"},
		{method: http.MethodGet, path: "/api/v1/peers/" + dummyID},
		{method: http.MethodGet, path: "/api/v1/peers/" + dummyID + "/events"},
		{method: http.MethodGet, path: "/api/v1/preauth-keys"},
		{method: http.MethodGet, path: "/api/v1/activity"},
		{method: http.MethodGet, path: "/api/v1/policy"},
		{method: http.MethodGet, path: "/api/v1/recommendations"},

		// lib/actions.ts mutation paths.
		{method: http.MethodPatch, path: "/api/v1/peers/" + dummyID, noBody: true},
		{method: http.MethodDelete, path: "/api/v1/peers/" + dummyID, noBody: true},
		{method: http.MethodPost, path: "/api/v1/preauth-keys", noBody: true},
		{method: http.MethodPost, path: "/api/v1/preauth-keys/" + dummyID + "/revoke", noBody: true},

		// Routes added by the still-stacked Web train. Uncomment as
		// each backend PR merges into main so the contract test
		// starts enforcing them.
		// {method: http.MethodGet, path: "/api/v1/users"},
		// {method: http.MethodGet, path: "/api/v1/invitations"},
		// {method: http.MethodPost, path: "/api/v1/invitations", noBody: true},
		// {method: http.MethodPost, path: "/api/v1/invitations/" + dummyID + "/revoke", noBody: true},
		// {method: http.MethodGet, path: "/api/v1/logs"},
		// {method: http.MethodPut, path: "/api/v1/policy", noBody: true},
	}

	// First probe a synthetic path that definitely doesn't exist so
	// we know what a "no route" response looks like in this fixture.
	// routeAPI calls http.NotFound which writes the text/plain body
	// "404 page not found\n"; that's the signature we use below to
	// distinguish a missing route from a handler-level 404.
	noRouteBody := probeBody(t, f.httpURL+"/api/v1/__definitely_not_a_route__", http.MethodGet, true)

	for _, p := range probes {
		t.Run(p.method+" "+p.path, func(t *testing.T) {
			body := probeBody(t, f.httpURL+p.path, p.method, p.noBody)
			if body.status == http.StatusMethodNotAllowed {
				t.Errorf("controller returned 405 — route exists for a different method; web is calling the wrong verb")
				return
			}
			if body.status == http.StatusNotFound && bytes.Equal(body.bytes, noRouteBody.bytes) {
				t.Errorf("controller returned the canonical \"no route\" 404 — route is missing entirely (body=%q)", body.bytes)
			}
		})
	}
}

type probeResult struct {
	status int
	bytes  []byte
}

func probeBody(t *testing.T, url, method string, noBody bool) probeResult {
	t.Helper()
	var rdr io.Reader
	if !noBody {
		rdr = strings.NewReader(`{}`)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	if !noBody {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return probeResult{status: resp.StatusCode, bytes: b}
}
