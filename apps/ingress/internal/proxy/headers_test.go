// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureUpstream records a clone of the last request it saw.
type captureUpstream struct {
	mu  sync.Mutex
	got *http.Request
}

func (c *captureUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	c.mu.Lock()
	c.got = r.Clone(r.Context())
	c.mu.Unlock()
}

func (c *captureUpstream) last() *http.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.got
}

func get(t *testing.T, front *httptest.Server, host string, hdr map[string]string) *http.Response {
	t.Helper()
	resp := do(t, front, http.MethodGet, host, "/", nil, hdr)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func TestProxy_XFFKeepsChainWithCaddyClientRightmost(t *testing.T) {
	up := &captureUpstream{}
	front, _, rec := setup(t, up, DefaultLimits())

	get(t, front, testHost, map[string]string{"X-Forwarded-For": "spoofed, 192.0.2.9"})
	if v := up.last().Header.Get("X-Forwarded-For"); v != "spoofed, 192.0.2.9" {
		t.Errorf("upstream X-Forwarded-For = %q, want %q", v, "spoofed, 192.0.2.9")
	}
	if v := up.last().Header.Values("X-Forwarded-For"); len(v) != 1 {
		t.Errorf("upstream saw %d X-Forwarded-For lines, want 1", len(v))
	}
	recs := rec.all()
	if len(recs) != 1 || recs[0].ClientIP != "192.0.2.9" {
		t.Errorf("telemetry client ip = %+v, want 192.0.2.9", recs)
	}
}

func TestProxy_XFFWithoutHeaderUsesRemoteAddr(t *testing.T) {
	up := &captureUpstream{}
	front, _, rec := setup(t, up, DefaultLimits())
	get(t, front, testHost, nil)
	if v := up.last().Header.Get("X-Forwarded-For"); v != "127.0.0.1" {
		t.Errorf("upstream X-Forwarded-For = %q, want 127.0.0.1", v)
	}
	if recs := rec.all(); len(recs) != 1 || recs[0].ClientIP != "127.0.0.1" {
		t.Errorf("record = %+v", recs)
	}
}

func TestProxy_XFFInvalidRightmostAppendsRemoteAddr(t *testing.T) {
	up := &captureUpstream{}
	front, _, _ := setup(t, up, DefaultLimits())
	get(t, front, testHost, map[string]string{"X-Forwarded-For": "junk"})
	if v := up.last().Header.Get("X-Forwarded-For"); v != "junk, 127.0.0.1" {
		t.Errorf("upstream X-Forwarded-For = %q, want rightmost to be the client ip", v)
	}
}

func TestProxy_RateLimitKeysOnRightmostXFF(t *testing.T) {
	lim := DefaultLimits()
	lim.PerClientPerMinute = 1
	front, _, _ := setup(t, &captureUpstream{}, lim)

	if r := get(t, front, testHost, map[string]string{"X-Forwarded-For": "1.1.1.1, 192.0.2.9"}); r.StatusCode != 200 {
		t.Fatalf("first: %d", r.StatusCode)
	}
	// A different spoofed prefix does not buy a fresh bucket.
	if r := get(t, front, testHost, map[string]string{"X-Forwarded-For": "2.2.2.2, 192.0.2.9"}); r.StatusCode != http.StatusTooManyRequests {
		t.Errorf("same real client, new spoofed prefix: %d, want 429", r.StatusCode)
	}
	// A different real client with the same spoofed prefix is its own bucket.
	if r := get(t, front, testHost, map[string]string{"X-Forwarded-For": "1.1.1.1, 192.0.2.10"}); r.StatusCode != 200 {
		t.Errorf("other real client: %d, want 200", r.StatusCode)
	}
}

func TestProxy_PerServiceLimitSetsRetryAfter(t *testing.T) {
	lim := DefaultLimits()
	lim.PerServicePerMinute = 1
	front, _, _ := setup(t, &captureUpstream{}, lim)
	get(t, front, testHost, map[string]string{"X-Forwarded-For": "192.0.2.1"})
	r := get(t, front, testHost, map[string]string{"X-Forwarded-For": "192.0.2.2"})
	if r.StatusCode != http.StatusTooManyRequests || r.Header.Get("Retry-After") == "" {
		t.Errorf("status=%d retry-after=%q", r.StatusCode, r.Header.Get("Retry-After"))
	}
}

func TestProxy_StripsClientBambooHeaders(t *testing.T) {
	up := &captureUpstream{}
	front, _, _ := setup(t, up, DefaultLimits())
	get(t, front, testHost, map[string]string{
		"X-Bamboo-Ingress-Service": "spoofed",
		"X-Bamboo-Tenant":          "other-tenant",
		"X-Forwarded-Proto":        "http",
		"X-Forwarded-Host":         "evil.example",
	})
	got := up.last()
	for k := range got.Header {
		if strings.HasPrefix(k, "X-Bamboo-") && k != "X-Bamboo-Ingress-Service" {
			t.Errorf("client header %s reached the upstream", k)
		}
	}
	if v := got.Header.Values("X-Bamboo-Ingress-Service"); len(v) != 1 || v[0] != "svc-1" {
		t.Errorf("X-Bamboo-Ingress-Service = %q", v)
	}
	if got.Header.Get("X-Forwarded-Proto") != "https" || got.Header.Get("X-Forwarded-Host") != testHost {
		t.Errorf("proto=%q host=%q", got.Header.Get("X-Forwarded-Proto"), got.Header.Get("X-Forwarded-Host"))
	}
}

func TestProxy_PublicHostIsNormalized(t *testing.T) {
	up := &captureUpstream{}
	front, _, _ := setup(t, up, DefaultLimits())
	if r := get(t, front, "APP.Svc.Test.:8443", nil); r.StatusCode != 200 {
		t.Fatalf("status=%d", r.StatusCode)
	}
	got := up.last()
	if got.Host != testHost || got.Header.Get("X-Forwarded-Host") != testHost {
		t.Errorf("upstream host=%q x-forwarded-host=%q, want %q", got.Host, got.Header.Get("X-Forwarded-Host"), testHost)
	}
}

func TestProxy_StripsHopByHopKeepsClientAuthorization(t *testing.T) {
	up := &captureUpstream{}
	front, _, _ := setup(t, up, DefaultLimits())
	get(t, front, testHost, map[string]string{
		"Connection":          "X-Hop",
		"X-Hop":               "secret-hop",
		"Proxy-Authorization": "Basic Zm9vOmJhcg==",
		"Te":                  "trailers, deflate",
		"Authorization":       "Bearer client-token",
	})
	got := up.last()
	for _, k := range []string{"X-Hop", "Proxy-Authorization"} {
		if v := got.Header.Get(k); v != "" {
			t.Errorf("hop-by-hop %s = %q reached the upstream", k, v)
		}
	}
	if v := got.Header.Get("Te"); v != "" && v != "trailers" {
		t.Errorf("Te = %q", v)
	}
	// The client's own credentials for the service pass through untouched;
	// the proxy has no credential of its own to add.
	if v := got.Header.Values("Authorization"); len(v) != 1 || v[0] != "Bearer client-token" {
		t.Errorf("Authorization = %q", v)
	}
}

func TestDefaultLimits(t *testing.T) {
	want := Limits{PerServicePerMinute: 120, PerClientPerMinute: 300, MaxBodyBytes: 8 << 20, DialTimeout: 5 * time.Second, IdleTimeout: 10 * time.Minute}
	if got := DefaultLimits(); got != want {
		t.Errorf("DefaultLimits = %+v, want %+v", got, want)
	}
}

func TestProxy_RouteRemovalEndsInFlightUpload(t *testing.T) {
	started := make(chan struct{})
	front, tb, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1)
		_, _ = r.Body.Read(buf)
		close(started)
		_, _ = io.Copy(io.Discard, r.Body)
	}), DefaultLimits())

	pr, pw := io.Pipe()
	defer pw.Close()
	go func() { _, _ = pw.Write([]byte("x")) }()
	done := make(chan *http.Response, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, front.URL+"/", pr)
		req.Host = testHost
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		done <- resp
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never saw the upload")
	}
	tb.Replace(nil)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("upload kept running after the route was removed")
	}
}

// TestProxy_StripsClientIdentityHeaders: upstream frameworks often trust
// X-Real-IP, True-Client-IP, or Forwarded for the client address; a
// client must not be able to set them through the ingress.
func TestProxy_StripsClientIdentityHeaders(t *testing.T) {
	up := &captureUpstream{}
	front, _, _ := setup(t, up, DefaultLimits())
	get(t, front, testHost, map[string]string{
		"X-Real-IP":      "6.6.6.6",
		"True-Client-IP": "6.6.6.6",
		"Forwarded":      "for=6.6.6.6;proto=http",
	})
	got := up.last()
	for _, k := range []string{"X-Real-Ip", "True-Client-Ip", "Forwarded"} {
		if v := got.Header.Values(k); len(v) != 0 {
			t.Errorf("client header %s reached the upstream: %q", k, v)
		}
	}
}
