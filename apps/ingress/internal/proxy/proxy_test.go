// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanfour/bamboo/apps/ingress/internal/routes"
)

const testHost = "app.svc.test"

type memRecorder struct {
	mu   sync.Mutex
	recs []Record
}

func (m *memRecorder) Record(r Record) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, r)
}

func (m *memRecorder) all() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Record(nil), m.recs...)
}

func routeTo(t *testing.T, upstreamURL string) routes.Route {
	t.Helper()
	hostPort := strings.TrimPrefix(upstreamURL, "http://")
	host, portStr, err := net.SplitHostPort(hostPort)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return routes.Route{ServiceID: "svc-1", Label: "app", PeerIP: host, TargetPort: port}
}

// loopback is the test client's socket address range; setup trusts it
// as if it were Caddy, so X-Forwarded-For in tests is honoured.
var loopback = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}

// setup returns a front server running the proxy for one route, with the
// test client trusted as the TLS-terminating proxy.
func setup(t *testing.T, upstream http.Handler, limits Limits) (*httptest.Server, *routes.Table, *memRecorder) {
	t.Helper()
	return setupTrusting(t, upstream, limits, loopback)
}

func setupTrusting(t *testing.T, upstream http.Handler, limits Limits, trusted []netip.Prefix) (*httptest.Server, *routes.Table, *memRecorder) {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	tb := routes.NewTable("svc.test")
	tb.Replace([]routes.Route{routeTo(t, up.URL)})
	rec := &memRecorder{}
	front := httptest.NewServer(New(Options{Table: tb, Limits: limits, Recorder: rec, TrustedProxies: trusted}))
	t.Cleanup(front.Close)
	return front, tb, rec
}

func do(t *testing.T, front *httptest.Server, method, host, path string, body io.Reader, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, front.URL+path, body)
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func TestProxy_RoutesByHostAndSetsHeaders(t *testing.T) {
	var got *http.Request
	front, _, rec := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte("hello"))
	}), DefaultLimits())

	resp := do(t, front, http.MethodPost, testHost, "/mcp?x=1", strings.NewReader("ping"), map[string]string{
		"X-Forwarded-For":          "203.0.113.7",
		"X-Bamboo-Ingress-Service": "spoofed",
	})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "hello" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	if got.URL.Path != "/mcp" || got.URL.RawQuery != "x=1" || got.Host != testHost {
		t.Errorf("upstream saw path=%q query=%q host=%q", got.URL.Path, got.URL.RawQuery, got.Host)
	}
	for k, want := range map[string]string{
		"X-Forwarded-For":          "203.0.113.7",
		"X-Forwarded-Proto":        "https",
		"X-Forwarded-Host":         testHost,
		"X-Bamboo-Ingress-Service": "svc-1",
	} {
		if v := got.Header.Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
	recs := rec.all()
	if len(recs) != 1 || recs[0].ServiceID != "svc-1" || recs[0].ClientIP != "203.0.113.7" ||
		recs[0].Status != 200 || recs[0].BytesIn != 4 || recs[0].BytesOut != 5 {
		t.Errorf("record = %+v", recs)
	}
}

func TestProxy_UnknownHost404(t *testing.T) {
	front, _, rec := setup(t, http.NotFoundHandler(), DefaultLimits())
	resp := do(t, front, http.MethodGet, "nope.svc.test", "/", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
	if len(rec.all()) != 0 {
		t.Error("unknown hosts are not recorded")
	}
}

func TestProxy_UpstreamDown502(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	tb := routes.NewTable("svc.test")
	tb.Replace([]routes.Route{routeTo(t, "http://"+addr)})
	front := httptest.NewServer(New(Options{Table: tb, Limits: DefaultLimits()}))
	defer front.Close()
	resp := do(t, front, http.MethodGet, testHost, "/", nil, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "service unavailable") {
		t.Errorf("status=%d body=%q, want 502 service unavailable", resp.StatusCode, body)
	}
}

func TestProxy_PerClientAndPerServiceLimits(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	lim := DefaultLimits()
	lim.PerClientPerMinute = 2
	front, _, _ := setup(t, ok, lim)
	for i := 0; i < 2; i++ {
		r := do(t, front, http.MethodGet, testHost, "/", nil, map[string]string{"X-Forwarded-For": "198.51.100.1"})
		r.Body.Close()
	}
	r := do(t, front, http.MethodGet, testHost, "/", nil, map[string]string{"X-Forwarded-For": "198.51.100.1"})
	r.Body.Close()
	if r.StatusCode != http.StatusTooManyRequests || r.Header.Get("Retry-After") == "" {
		t.Errorf("per-client: status=%d retry-after=%q", r.StatusCode, r.Header.Get("Retry-After"))
	}

	lim = DefaultLimits()
	lim.PerServicePerMinute = 2
	front2, _, _ := setup(t, ok, lim)
	for i := 0; i < 2; i++ {
		r := do(t, front2, http.MethodGet, testHost, "/", nil, map[string]string{"X-Forwarded-For": "198.51.100." + strconv.Itoa(10+i)})
		r.Body.Close()
	}
	r = do(t, front2, http.MethodGet, testHost, "/", nil, map[string]string{"X-Forwarded-For": "198.51.100.99"})
	r.Body.Close()
	if r.StatusCode != http.StatusTooManyRequests {
		t.Errorf("per-service: status=%d, want 429", r.StatusCode)
	}
}

func TestProxy_BodyCap413(t *testing.T) {
	var calls int
	var mu sync.Mutex
	front, _, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
	}), Limits{PerServicePerMinute: 100, PerClientPerMinute: 100, MaxBodyBytes: 16, DialTimeout: time.Second, IdleTimeout: time.Minute})

	big := strings.Repeat("x", 32)
	r := do(t, front, http.MethodPost, testHost, "/", strings.NewReader(big), nil)
	r.Body.Close()
	if r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("known length: status=%d, want 413", r.StatusCode)
	}
	mu.Lock()
	if calls != 0 {
		t.Errorf("upstream called %d times for an over-cap known-length body", calls)
	}
	mu.Unlock()

	// Unknown length (chunked): the cap trips while streaming.
	pr, pw := io.Pipe()
	go func() { _, _ = pw.Write([]byte(big)); pw.Close() }()
	r = do(t, front, http.MethodPost, testHost, "/", pr, nil)
	r.Body.Close()
	if r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("chunked: status=%d, want 413", r.StatusCode)
	}
}

// sseUpstream writes one event, then waits for release before the next.
func sseUpstream(release <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: one\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
			_, _ = w.Write([]byte("data: two\n\n"))
		case <-r.Context().Done():
		}
	})
}

func readLine(t *testing.T, br *bufio.Reader, within time.Duration) (string, error) {
	t.Helper()
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() { s, err := br.ReadString('\n'); ch <- res{s, err} }()
	select {
	case r := <-ch:
		return r.s, r.err
	case <-time.After(within):
		t.Fatalf("no line within %v", within)
		return "", nil
	}
}

func TestProxy_StreamsSSEUnbuffered(t *testing.T) {
	release := make(chan struct{})
	front, _, _ := setup(t, sseUpstream(release), DefaultLimits())
	resp := do(t, front, http.MethodGet, testHost, "/events", nil, nil)
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if line, err := readLine(t, br, 2*time.Second); err != nil || line != "data: one\n" {
		t.Fatalf("first event = %q, %v (buffered?)", line, err)
	}
	close(release)
	_, _ = readLine(t, br, 2*time.Second) // blank separator
	if line, _ := readLine(t, br, 2*time.Second); line != "data: two\n" {
		t.Errorf("second event = %q", line)
	}
}

func TestProxy_IdleTimeoutClosesStream(t *testing.T) {
	lim := DefaultLimits()
	lim.IdleTimeout = 150 * time.Millisecond
	front, _, _ := setup(t, sseUpstream(make(chan struct{})), lim)
	resp := do(t, front, http.MethodGet, testHost, "/events", nil, nil)
	defer resp.Body.Close()
	start := time.Now()
	_, err := io.ReadAll(resp.Body)
	if time.Since(start) > 3*time.Second {
		t.Errorf("idle stream lived %v", time.Since(start))
	}
	_ = err // EOF or unexpected EOF are both fine: the stream ended.
}

func TestProxy_RouteRemovalEndsStream(t *testing.T) {
	front, tb, _ := setup(t, sseUpstream(make(chan struct{})), DefaultLimits())
	resp := do(t, front, http.MethodGet, testHost, "/events", nil, nil)
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if _, err := readLine(t, br, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	tb.Replace(nil)
	done := make(chan struct{})
	go func() { _, _ = io.ReadAll(br); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream kept running after the route was removed")
	}
}
