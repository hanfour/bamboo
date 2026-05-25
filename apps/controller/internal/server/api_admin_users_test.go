// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRouteAdminUsersSub_404sUnknownSubpath pins the dispatcher
// default-deny: anything under /api/v1/admin/users/ that doesn't
// end in /erase returns 404. A future "list users" / "set admin"
// surface goes here too; until then a typo (e.g. /eras instead
// of /erase) must not silently fall through to nil-handler land.
func TestRouteAdminUsersSub_404sUnknownSubpath(t *testing.T) {
	h := &HTTPServer{}
	cases := []string{
		"/api/v1/admin/users/",
		"/api/v1/admin/users/abc",
		"/api/v1/admin/users/00000000-0000-0000-0000-000000000000",
		"/api/v1/admin/users/00000000-0000-0000-0000-000000000000/eras", // typo
		"/api/v1/admin/users/00000000-0000-0000-0000-000000000000/role",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			rec := httptest.NewRecorder()
			h.routeAdminUsersSub(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("got %d, want 404 (%s)", rec.Code, path)
			}
		})
	}
}

// TestRouteAdminUserErase_RejectsNonPOST pins the method gate —
// a GET to the erase endpoint must surface as 405, not run the
// data plane. Audit log is append-only; the rejection happens
// BEFORE any DB work so a confused tooling that fires GETs
// doesn't accidentally probe behaviour by side-effect.
func TestRouteAdminUserErase_RejectsNonPOST(t *testing.T) {
	h := &HTTPServer{}
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(m,
			"/api/v1/admin/users/00000000-0000-0000-0000-000000000000/erase", nil)
		rec := httptest.NewRecorder()
		h.routeAdminUserErase(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: got %d, want 405", m, rec.Code)
		}
	}
}

// TestRouteAdminUserErase_RejectsMalformedID pins the
// path-parse-before-auth ordering: a non-UUID `{id}` returns
// 400 without hitting the auth + DB layers. Otherwise an
// attacker probing for valid ID shapes could distinguish
// "valid UUID but unauth" from "invalid UUID" via response
// timing.
func TestRouteAdminUserErase_RejectsMalformedID(t *testing.T) {
	h := &HTTPServer{}
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/users/not-a-uuid/erase", nil)
	rec := httptest.NewRecorder()
	h.routeAdminUserErase(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid user id") {
		t.Errorf("body = %q, want 'invalid user id' substring", rec.Body.String())
	}
}

// TestNormalizeRoute_AdminUserErase pins the metrics-label
// shape: the erase path collapses to /api/v1/admin/users/{id}/erase
// so a tenant doing 50 erasures doesn't blow up the
// http_requests_total time-series count with one series per UUID.
func TestNormalizeRoute_AdminUserErase(t *testing.T) {
	const uid = "11111111-2222-3333-4444-555555555555"
	cases := map[string]string{
		"/api/v1/admin/users/" + uid + "/erase":  "/api/v1/admin/users/{id}/erase",
		"/api/v1/admin/users/" + uid + "/other": "/api/v1/admin/users/" + uid + "/other",
	}
	for input, want := range cases {
		if got := normalizeRoute(input); got != want {
			t.Errorf("normalizeRoute(%q) = %q, want %q", input, got, want)
		}
	}
}
