// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import "testing"

// TestNormalizeRoute_CollapsesUUIDPaths pins the cardinality
// contract for the Prometheus middleware: any peer-id-bearing path
// collapses to a {id} pattern so a tenant with thousands of peers
// doesn't blow up the http_requests_total series count.
func TestNormalizeRoute_CollapsesUUIDPaths(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	cases := map[string]string{
		"/api/v1/peers/" + uuid:                        "/api/v1/peers/{id}",
		"/api/v1/peers/" + uuid + "/events":            "/api/v1/peers/{id}/events",
		"/api/v1/peers/" + uuid + "/connection-events": "/api/v1/peers/{id}/connection-events",
		"/api/v1/peers/" + uuid + "/route-conflicts":   "/api/v1/peers/{id}/route-conflicts",
		"/api/v1/peers/" + uuid + "/approve":           "/api/v1/peers/{id}/approve",
		"/api/v1/peers/" + uuid + "/reject":            "/api/v1/peers/{id}/reject",
		"/api/v1/peers/" + uuid + "/routes":            "/api/v1/peers/{id}/routes",
		"/api/v1/peers/" + uuid + "/exit-node":         "/api/v1/peers/{id}/exit-node",
		"/api/v1/preauth-keys/" + uuid + "/revoke":     "/api/v1/preauth-keys/{id}/revoke",
		"/api/v1/peers/register":                       "/api/v1/peers/register",
		"/api/v1/peers/heartbeat":                      "/api/v1/peers/heartbeat",
		"/api/v1/peers/watch":                          "/api/v1/peers/watch",
		"/api/v1/policy":                               "/api/v1/policy",
		"/api/v1/policy/revisions":                     "/api/v1/policy/revisions",
		"/healthz":                                     "/healthz",
		"/metrics":                                     "/metrics",
		"/auth/google/login":                           "/auth/{provider}/login",
		"/auth/google/callback":                        "/auth/{provider}/callback",
		"/auth/sign-out":                               "/auth/sign-out",
	}
	for input, want := range cases {
		if got := normalizeRoute(input); got != want {
			t.Errorf("normalizeRoute(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestNormalizeRoute_UnknownPathPreservedAsIs verifies that a path
// not matching any pattern falls through unchanged. Cardinality is
// bounded by the route count itself, and an unmatched series acts
// as a "missing pattern" signal in the dashboard so the table can
// be updated without a code change deploying first.
func TestNormalizeRoute_UnknownPathPreservedAsIs(t *testing.T) {
	cases := []string{
		"/some/completely/unknown/path",
		"/api/v1/never-shipped",
		"/",
	}
	for _, p := range cases {
		if got := normalizeRoute(p); got != p {
			t.Errorf("normalizeRoute(%q) = %q, want %q (unchanged)", p, got, p)
		}
	}
}
