// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func okNext() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestParseCORSOrigins(t *testing.T) {
	if got := parseCORSOrigins(""); got != nil {
		t.Errorf("empty ⇒ nil, got %v", got)
	}
	if got := parseCORSOrigins("   "); got != nil {
		t.Errorf("blank ⇒ nil, got %v", got)
	}
	got := parseCORSOrigins("https://a.example.com, https://b.example.com ,")
	if len(got) != 2 || !got["https://a.example.com"] || !got["https://b.example.com"] {
		t.Errorf("parsed = %v, want the two trimmed origins", got)
	}
}

func TestWithCORS_EmptyAllowlistIsWildcard(t *testing.T) {
	mw := withCORS(okNext(), nil) // dev default
	req := httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("ACAO = %q, want *", got)
	}
}

func TestWithCORS_AllowlistReflectsOnlyAllowed(t *testing.T) {
	allowed := map[string]bool{"https://bamboo.example.com": true}
	mw := withCORS(okNext(), allowed)

	// Allowed origin is reflected back (not "*") with Vary: Origin.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil)
	req.Header.Set("Origin", "https://bamboo.example.com")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://bamboo.example.com" {
		t.Errorf("allowed origin ACAO = %q, want the echoed origin", got)
	}
	if rec.Header().Get("Vary") != "Origin" {
		t.Error("reflected origin should set Vary: Origin")
	}

	// A disallowed origin gets NO ACAO header — the browser blocks it.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil)
	req2.Header.Set("Origin", "https://evil.example.com")
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, req2)
	if got := rec2.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("disallowed origin got ACAO %q, want none", got)
	}
}

func TestWithCORS_OptionsShortCircuits(t *testing.T) {
	mw := withCORS(okNext(), nil)
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/peers", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("OPTIONS status = %d, want 204", rec.Code)
	}
}

func TestRequestIsHTTPS(t *testing.T) {
	// Direct TLS.
	tlsReq := httptest.NewRequest(http.MethodGet, "/", nil)
	tlsReq.TLS = &tls.ConnectionState{}
	if !requestIsHTTPS(tlsReq) {
		t.Error("r.TLS set should be HTTPS")
	}

	// Behind a proxy that forwarded HTTPS.
	fwd := httptest.NewRequest(http.MethodGet, "/", nil)
	fwd.Header.Set("X-Forwarded-Proto", "https")
	if !requestIsHTTPS(fwd) {
		t.Error("X-Forwarded-Proto=https should be HTTPS")
	}

	// Plain HTTP (no TLS, XFF http or absent) → not HTTPS.
	plain := httptest.NewRequest(http.MethodGet, "/", nil)
	if requestIsHTTPS(plain) {
		t.Error("no TLS + no XFF should NOT be HTTPS")
	}
	plain.Header.Set("X-Forwarded-Proto", "http")
	if requestIsHTTPS(plain) {
		t.Error("XFF=http should NOT be HTTPS")
	}
}
