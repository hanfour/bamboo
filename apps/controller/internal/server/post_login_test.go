// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A browser sign-in must end on the Web UI. It used to end on a bare page
// that printed the session token and linked nowhere, so people stalled
// there. The cookie is already set by then, so the redirect is enough.
func TestFinishBrowserLogin_RedirectsToTheWebUI(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		webURL  string
		want    string
	}{
		{"same host behind the proxy", "https://bamboo.example.com", "", "https://bamboo.example.com/"},
		{"trailing slash is not doubled", "https://bamboo.example.com/", "", "https://bamboo.example.com/"},
		{"separate web origin in development", "http://localhost:8081", "http://localhost:3000", "http://localhost:3000/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &HTTPServer{baseURL: tc.baseURL, webURL: tc.webURL}
			rec := httptest.NewRecorder()
			h.finishBrowserLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/google/callback", nil))

			if rec.Code != http.StatusFound {
				t.Fatalf("status %d, want 302", rec.Code)
			}
			if got := rec.Header().Get("Location"); got != tc.want {
				t.Errorf("Location %q, want %q", got, tc.want)
			}
		})
	}
}

// The destination comes from server config only. Nothing on the request,
// such as a query parameter, may change it, or the callback becomes an
// open redirect that carries a fresh session cookie.
func TestFinishBrowserLogin_IgnoresRequestInput(t *testing.T) {
	h := &HTTPServer{baseURL: "https://bamboo.example.com"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/google/callback?next=https://evil.example&return=//evil.example", nil)
	req.Header.Set("Referer", "https://evil.example/")
	h.finishBrowserLogin(rec, req)

	if loc := rec.Header().Get("Location"); strings.Contains(loc, "evil") {
		t.Fatalf("redirect followed request input: %q", loc)
	}
}
