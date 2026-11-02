// SPDX-License-Identifier: AGPL-3.0-or-later

// Package proxy is bamboo-ingress's Host-routed reverse proxy
// (docs/design/2026-11-02-public-services-ingress.md §5).
package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanfour/bamboo/apps/ingress/internal/ratelimit"
	"github.com/hanfour/bamboo/apps/ingress/internal/routes"
)

// Limits are the spec §5 limits.
type Limits struct {
	PerServicePerMinute int
	PerClientPerMinute  int
	MaxBodyBytes        int64
	DialTimeout         time.Duration
	IdleTimeout         time.Duration
}

// DefaultLimits: 120/min per service, 300/min per client IP, 8 MiB body,
// 5 s dial, 10 min idle stream.
func DefaultLimits() Limits {
	return Limits{
		PerServicePerMinute: 120,
		PerClientPerMinute:  300,
		MaxBodyBytes:        8 << 20,
		DialTimeout:         5 * time.Second,
		IdleTimeout:         10 * time.Minute,
	}
}

// Record is per-request metadata. It has no field that could carry a
// body or a header, so telemetry cannot leak either.
type Record struct {
	ServiceID string
	Label     string
	ClientIP  string
	Status    int
	BytesIn   int64
	BytesOut  int64
	Duration  time.Duration
	At        time.Time
}

// Recorder receives one Record per routed request. Must not block.
type Recorder interface {
	Record(Record)
}

// Options configure a Handler. Recorder and Transport are optional.
type Options struct {
	Table     *routes.Table
	Limits    Limits
	Recorder  Recorder
	Transport http.RoundTripper
}

// Handler is the public-facing http.Handler. It never logs: the only
// per-request output is a Record.
type Handler struct {
	table      *routes.Table
	limits     Limits
	rec        Recorder
	perService *ratelimit.Limiter
	perClient  *ratelimit.Limiter
	rp         *httputil.ReverseProxy
}

type stateKey struct{}

// reqState travels in the outbound request context from ServeHTTP to the
// ReverseProxy callbacks.
type reqState struct {
	entry        *routes.Entry
	publicHost   string
	clientIP     string
	forwardedFor string
	body         *countingBody
}

// New builds a Handler.
func New(o Options) *Handler {
	h := &Handler{
		table:      o.Table,
		limits:     o.Limits,
		rec:        o.Recorder,
		perService: ratelimit.New(o.Limits.PerServicePerMinute, o.Limits.PerServicePerMinute),
		perClient:  ratelimit.New(o.Limits.PerClientPerMinute, o.Limits.PerClientPerMinute),
	}
	transport := o.Transport
	if transport == nil {
		transport = &http.Transport{
			DialContext:         (&net.Dialer{Timeout: o.Limits.DialTimeout, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
		}
	}
	h.rp = &httputil.ReverseProxy{
		Rewrite:       rewrite,
		Transport:     transport,
		FlushInterval: -1, // flush every write: SSE and chunked MCP responses
		ErrorHandler:  errorHandler,
	}
	return h
}

// rewrite runs after ReverseProxy has stripped hop-by-hop headers and the
// inbound Forwarded / X-Forwarded-* headers from pr.Out.
func rewrite(pr *httputil.ProxyRequest) {
	st := pr.In.Context().Value(stateKey{}).(*reqState)
	pr.Out.URL.Scheme = "http"
	pr.Out.URL.Host = st.entry.Route.Upstream()
	pr.Out.Host = st.publicHost
	for k := range pr.Out.Header {
		if strings.HasPrefix(k, "X-Bamboo-") {
			pr.Out.Header.Del(k)
		}
	}
	pr.Out.Header.Set("X-Forwarded-For", st.forwardedFor)
	pr.Out.Header.Set("X-Forwarded-Proto", "https")
	pr.Out.Header.Set("X-Forwarded-Host", st.publicHost)
	pr.Out.Header.Set("X-Bamboo-Ingress-Service", st.entry.Route.ServiceID)
}

func errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	st, _ := r.Context().Value(stateKey{}).(*reqState)
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) || (st != nil && st.body.tooLarge.Load()) {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "service unavailable", http.StatusBadGateway)
}

// ServeHTTP routes one request.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	entry, ok := h.table.Lookup(r.Host)
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}
	start := time.Now()
	rw := &recorder{ResponseWriter: w}
	clientIP := ClientIP(r)
	st := &reqState{
		entry:        entry,
		publicHost:   entry.Route.Label + "." + h.table.Suffix(),
		clientIP:     clientIP,
		forwardedFor: forwardedFor(r, clientIP),
	}
	defer h.report(st, rw, start)

	if !h.allow(rw, h.perClient, "ip:"+clientIP) || !h.allow(rw, h.perService, "svc:"+entry.Route.ServiceID) {
		return
	}
	if r.ContentLength > h.limits.MaxBodyBytes {
		http.Error(rw, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cutter := newRequestCutter(w, cancel)
	defer cutter.finish()
	stopRoute := context.AfterFunc(entry.Context(), cutter.cut) // route retired => end the request
	defer stopRoute()
	idle := newIdleTimer(h.limits.IdleTimeout, cutter.cut)
	defer idle.Stop()
	rw.idle = idle

	// MaxBytesReader gets the server's writer so it can mark the
	// connection for close when the cap trips.
	st.body = &countingBody{rc: http.MaxBytesReader(w, r.Body, h.limits.MaxBodyBytes), idle: idle}
	out := r.WithContext(context.WithValue(ctx, stateKey{}, st))
	out.Body = st.body
	h.rp.ServeHTTP(rw, out)
}

// requestCutter ends a request from outside the handler goroutine.
// Cancelling the context stops the upstream round trip, but a client that
// stalls mid-upload would still pin the server goroutine in a blocked body
// read (and in net/http's post-handler drain), so the inbound read
// deadline is also pulled to now. Once the handler has returned, cut is a
// no-op so a late callback never touches the connection's next request.
type requestCutter struct {
	mu     sync.Mutex
	done   bool
	cancel context.CancelFunc
	rc     *http.ResponseController
}

func newRequestCutter(w http.ResponseWriter, cancel context.CancelFunc) *requestCutter {
	return &requestCutter{cancel: cancel, rc: http.NewResponseController(w)}
}

func (c *requestCutter) cut() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return
	}
	c.cancel()
	// Fails only when the writer has no connection (a test recorder);
	// cancellation still applies then.
	_ = c.rc.SetReadDeadline(time.Now())
}

// finish disarms cut; call it before the handler returns.
func (c *requestCutter) finish() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.done = true
}

func (h *Handler) allow(w http.ResponseWriter, lim *ratelimit.Limiter, key string) bool {
	if lim.Allow(key) {
		return true
	}
	secs := int(lim.RetryAfter(key).Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	http.Error(w, "too many requests", http.StatusTooManyRequests)
	return false
}

func (h *Handler) report(st *reqState, rw *recorder, start time.Time) {
	if h.rec == nil {
		return
	}
	status := rw.status
	if status == 0 {
		status = 499 // client went away before any response
	}
	var in int64
	if st.body != nil {
		in = st.body.n.Load()
	}
	h.rec.Record(Record{
		ServiceID: st.entry.Route.ServiceID, Label: st.entry.Route.Label, ClientIP: st.clientIP,
		Status: status, BytesIn: in, BytesOut: rw.n.Load(), Duration: time.Since(start), At: start,
	})
}

// CleanupLimiters bounds limiter memory; call it periodically.
func (h *Handler) CleanupLimiters(idle time.Duration) {
	h.perService.Cleanup(idle)
	h.perClient.Cleanup(idle)
}

// ClientIP is the client address. The listener sits on the docker bridge
// and is reachable only from Caddy, which puts the address it saw last in
// X-Forwarded-For, so the rightmost entry is trusted; anything to its left
// is client-supplied. RemoteAddr is the fallback.
func ClientIP(r *http.Request) string {
	parts := xffEntries(r)
	if len(parts) > 0 {
		if ip := net.ParseIP(parts[len(parts)-1]); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// forwardedFor is the outbound X-Forwarded-For: the inbound chain with
// clientIP as its rightmost entry. Caddy's own address is never added.
func forwardedFor(r *http.Request, clientIP string) string {
	parts := xffEntries(r)
	if n := len(parts); n == 0 || canonicalIP(parts[n-1]) != clientIP {
		parts = append(parts, clientIP)
	}
	return strings.Join(parts, ", ")
}

// xffEntries flattens every X-Forwarded-For line into trimmed, non-empty
// entries, in order.
func xffEntries(r *http.Request) []string {
	var out []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		for _, p := range strings.Split(line, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func canonicalIP(s string) string {
	if ip := net.ParseIP(s); ip != nil {
		return ip.String()
	}
	return ""
}
