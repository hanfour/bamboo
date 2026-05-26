// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
)

func TestAuthenticate_Unauthenticated_FallsThrough(t *testing.T) {
	h := &HTTPServer{secret: []byte("test-secret-with-at-least-32-bytes-padding")}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)

	authn, err := h.authenticate(r)
	if err != nil {
		t.Fatalf("expected nil error for missing creds, got %v", err)
	}
	if authn == nil {
		t.Fatal("authn is nil; expected sentinel struct")
	}
	if authn.claims != nil {
		t.Errorf("claims should be nil on dev fallback, got %+v", authn.claims)
	}
}

func TestAuthenticate_BearerHeader_HappyPath(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	tenantID := uuid.New()
	userID := uuid.New()
	tok, err := auth.IssueSessionToken(secret, auth.SessionClaims{
		UserID:   userID,
		TenantID: tenantID,
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueSessionToken: %v", err)
	}

	h := &HTTPServer{secret: secret}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+tok)

	authn, err := h.authenticate(r)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if authn.claims == nil {
		t.Fatal("expected non-nil claims for valid bearer")
	}
	if authn.claims.UserID != userID {
		t.Errorf("UserID = %v, want %v", authn.claims.UserID, userID)
	}
	if authn.claims.TenantID != tenantID {
		t.Errorf("TenantID = %v, want %v", authn.claims.TenantID, tenantID)
	}
}

func TestAuthenticate_BearerHeader_InvalidRejects(t *testing.T) {
	h := &HTTPServer{secret: []byte("test-secret-with-at-least-32-bytes-padding")}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+"definitely-not-a-real-jwt")

	if _, err := h.authenticate(r); err == nil {
		t.Error("expected error on invalid bearer; got nil")
	}
}

func TestAuthenticate_Cookie_HappyPath(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	tenantID := uuid.New()
	tok, err := auth.IssueSessionToken(secret, auth.SessionClaims{
		UserID:   uuid.New(),
		TenantID: tenantID,
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueSessionToken: %v", err)
	}

	h := &HTTPServer{secret: secret}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: tok})

	authn, err := h.authenticate(r)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if authn.claims == nil || authn.claims.TenantID != tenantID {
		t.Errorf("claims = %+v; expected TenantID %v", authn.claims, tenantID)
	}
}

func TestAuthenticate_Cookie_InvalidRejects(t *testing.T) {
	h := &HTTPServer{secret: []byte("test-secret-with-at-least-32-bytes-padding")}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "garbage"})

	if _, err := h.authenticate(r); err == nil {
		t.Error("expected error on invalid cookie; got nil")
	}
}

func TestAuthenticate_BearerPrecedesCookieWhenBothPresent(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	bearerTenant := uuid.New()
	cookieTenant := uuid.New()

	bearerTok, _ := auth.IssueSessionToken(secret, auth.SessionClaims{
		UserID:   uuid.New(),
		TenantID: bearerTenant,
	}, time.Hour)
	cookieTok, _ := auth.IssueSessionToken(secret, auth.SessionClaims{
		UserID:   uuid.New(),
		TenantID: cookieTenant,
	}, time.Hour)

	h := &HTTPServer{secret: secret}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+bearerTok)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookieTok})

	authn, err := h.authenticate(r)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if authn.claims.TenantID != bearerTenant {
		t.Errorf("expected bearer's tenant to win; got %v", authn.claims.TenantID)
	}
}

// TestAugmentUpgradeAvailable pins the per-peer flagging logic
// used by apiPeers — covers the disabled-feed (empty latest),
// behind / equal / ahead, and empty-client-version paths in one
// table. The handler wiring on top is two lines and verified by
// manual / e2e; this test exists to catch a future regression that
// drops the per-peer assignment or misroutes the latest string.
func TestAugmentUpgradeAvailable(t *testing.T) {
	cases := []struct {
		name   string
		peers  []apiPeerJSON
		latest string
		want   []bool // upgrade_available per peer, same order
	}{
		{
			name:   "feed disabled (empty latest) suppresses all flags",
			peers:  []apiPeerJSON{{ClientVersion: "0.1.3"}, {ClientVersion: "0.1.4"}},
			latest: "",
			want:   []bool{false, false},
		},
		{
			name:   "behind / equal / ahead",
			peers:  []apiPeerJSON{{ClientVersion: "0.1.3"}, {ClientVersion: "0.1.4"}, {ClientVersion: "0.1.5"}},
			latest: "0.1.4",
			want:   []bool{true, false, false},
		},
		{
			name:   "empty client_version never flagged",
			peers:  []apiPeerJSON{{ClientVersion: ""}, {ClientVersion: "0.1.3"}},
			latest: "0.1.4",
			want:   []bool{false, true},
		},
		{
			name:   "non-semver peer version never flagged",
			peers:  []apiPeerJSON{{ClientVersion: "dev"}, {ClientVersion: "canary"}},
			latest: "0.1.4",
			want:   []bool{false, false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			augmentUpgradeAvailable(tc.peers, tc.latest)
			for i := range tc.peers {
				if got := tc.peers[i].UpgradeAvailable; got != tc.want[i] {
					t.Errorf("peers[%d].UpgradeAvailable = %v, want %v", i, got, tc.want[i])
				}
			}
		})
	}
}

func TestBearerToken_Helpers(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"basic auth ignored", "Basic abcd", ""},
		{"bearer", "Bearer xyz", "xyz"},
		{"bearer with whitespace", "Bearer   xyz  ", "xyz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.in != "" {
				r.Header.Set("Authorization", tc.in)
			}
			if got := bearerToken(r); got != tc.want {
				t.Errorf("bearerToken = %q, want %q", got, tc.want)
			}
		})
	}
}
