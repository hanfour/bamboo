// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRouteAdminUsers_RequiresAuth pins the admin-gate ordering:
// every path under /api/v1/admin/users/ — known action or not —
// must surface as 401 to an unauthenticated caller, BEFORE any
// path-shape parsing or DB lookups. A regression here would either
// leak existence (404 vs 405 lets an attacker probe known actions)
// or hit the data plane unauthenticated.
func TestRouteAdminUsers_RequiresAuth(t *testing.T) {
	h := &HTTPServer{
		secret: []byte("test-secret-with-at-least-32-bytes-padding"),
	}
	const uid = "00000000-0000-0000-0000-000000000000"
	cases := []string{
		"/api/v1/admin/users/",
		"/api/v1/admin/users/" + uid + "/erase",
		"/api/v1/admin/users/" + uid + "/sign-out-all",
		"/api/v1/admin/users/" + uid + "/other", // unknown action
		"/api/v1/admin/users/not-a-uuid/erase",  // bad uuid
		"/api/v1/admin/users/" + uid + "/eras",  // typo
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			rec := httptest.NewRecorder()
			h.routeAdminUsers(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("got %d, want 401 (%s)", rec.Code, path)
			}
		})
	}
}

// TestNormalizeRoute_AdminUsers pins the metrics-label shape:
// the erase + sign-out-all paths collapse to
// /api/v1/admin/users/{id}/{action} so a tenant doing 50 erasures
// (or 50 sign-outs) doesn't blow up the http_requests_total
// time-series count with one series per UUID.
func TestNormalizeRoute_AdminUsers(t *testing.T) {
	const uid = "11111111-2222-3333-4444-555555555555"
	cases := map[string]string{
		"/api/v1/admin/users/" + uid + "/erase":        "/api/v1/admin/users/{id}/erase",
		"/api/v1/admin/users/" + uid + "/sign-out-all": "/api/v1/admin/users/{id}/sign-out-all",
		"/api/v1/admin/users/" + uid + "/other":        "/api/v1/admin/users/" + uid + "/other",
	}
	for input, want := range cases {
		if got := normalizeRoute(input); got != want {
			t.Errorf("normalizeRoute(%q) = %q, want %q", input, got, want)
		}
	}
}
