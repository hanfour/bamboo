// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// drive sends one request through the middleware and returns the status.
func drive(mw http.Handler, method, path, remoteAddr string) int {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	return rec.Code
}

func TestRateLimitMiddleware(t *testing.T) {
	h := &HTTPServer{}
	h.SetRateLimiting(true)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mw := h.rateLimitMiddleware(next)

	// Hammer relay-token from one IP past its burst (60) — must end in 429.
	var last int
	for i := 0; i < 200; i++ {
		last = drive(mw, http.MethodPost, "/api/v1/relay-token", "9.9.9.9:5555")
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after 200 requests from one IP, status=%d, want 429", last)
	}

	// A different IP is independent — its first request passes.
	if code := drive(mw, http.MethodPost, "/api/v1/relay-token", "8.8.8.8:1"); code != http.StatusOK {
		t.Errorf("different IP got %d, want 200 (limiters must be per-IP)", code)
	}

	// A non-limited path is never throttled, even from the blocked IP.
	if code := drive(mw, http.MethodGet, "/api/v1/peers", "9.9.9.9:5555"); code != http.StatusOK {
		t.Errorf("non-limited path got %d, want 200", code)
	}

	// sign-out is excluded from the /auth/ limiter.
	for i := 0; i < 50; i++ {
		if code := drive(mw, http.MethodPost, "/auth/sign-out", "9.9.9.9:5555"); code != http.StatusOK {
			t.Fatalf("/auth/sign-out throttled at request %d (%d); it must be excluded", i, code)
		}
	}
}

func TestRateLimitMiddleware_DisabledIsNoop(t *testing.T) {
	h := &HTTPServer{} // rateLimit nil ⇒ disabled (the default; e2e fixture path)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mw := h.rateLimitMiddleware(next)
	for i := 0; i < 500; i++ {
		if code := drive(mw, http.MethodPost, "/api/v1/relay-token", "9.9.9.9:5555"); code != http.StatusOK {
			t.Fatalf("disabled limiter throttled request %d (%d)", i, code)
		}
	}
}

func TestRateLimitMiddleware_SetsRetryAfter(t *testing.T) {
	h := &HTTPServer{}
	h.SetRateLimiting(true)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mw := h.rateLimitMiddleware(next)

	var rec *httptest.ResponseRecorder
	for i := 0; i < 200; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/peers/register", nil)
		req.RemoteAddr = "7.7.7.7:9"
		rec = httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 response missing Retry-After header")
	}
}
