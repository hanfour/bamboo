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
	"net/netip"
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

// DefaultLimits returns 120/min per service, 300/min per client IP, 8 MiB body,
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
// TrustedProxies lists the socket peers (Caddy) whose X-Forwarded-For is
// believed; empty trusts nobody, so every client is its socket address.
type Options struct {
	Table          *routes.Table
	Limits         Limits
	Recorder       Recorder
	Transport      http.RoundTripper
	TrustedProxies []netip.Prefix
}

// Handler is the public-facing http.Handler. It never logs: the only
// per-request output is a Record.
type Handler struct {
	table      *routes.Table
	limits     Limits
	rec        Recorder
	perService *ratelimit.Limiter
	perClient  *ratelimit.Limiter
	trusted    []netip.Prefix
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
		trusted:    append([]netip.Prefix(nil), o.TrustedProxies...),
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

// clientIdentityHeaders are client-address headers upstream frameworks
// commonly trust. The ingress sets only X-Forwarded-For, so any inbound
// copy is client-supplied and dropped.
var clientIdentityHeaders = []string{"X-Real-Ip", "True-Client-Ip", "Forwarded"}

// rewrite runs after ReverseProxy has stripped hop-by-hop headers and the
// inbound Forwarded / X-Forwarded-* headers from pr.Out. It deletes
// Forwarded again explicitly so the guarantee does not rest on that.
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
	for _, k := range clientIdentityHeaders {
		pr.Out.Header.Del(k)
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
	if isUpgrade(r) {
		// Spec §5 covers HTTP and SSE only; a hijacked connection would
		// escape the idle timer, byte counts and route retirement.
		http.Error(w, "protocol upgrade not supported", http.StatusBadRequest)
		return
	}
	entry, ok := h.table.Lookup(r.Host)
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}
	start := time.Now()
	rw := &recorder{ResponseWriter: w}
	trusted := h.peerTrusted(r)
	clientIP := ClientIP(r, trusted)
	st := &reqState{
		entry:        entry,
		publicHost:   entry.Route.Label + "." + h.table.Suffix(),
		clientIP:     clientIP,
		forwardedFor: forwardedFor(r, clientIP, trusted),
	}
	defer h.report(st, rw, start)

	if !h.allow(rw, h.perClient, limiterKey(clientIP)) || !h.allow(rw, h.perService, "svc:"+entry.Route.ServiceID) {
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

// isUpgrade reports a protocol-switch request: any Upgrade header, or an
// "upgrade" token in Connection.
func isUpgrade(r *http.Request) bool {
	if r.Header.Get("Upgrade") != "" {
		return true
	}
	for _, line := range r.Header.Values("Connection") {
		for _, tok := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
}

// peerTrusted reports whether the socket peer is a trusted proxy.
func (h *Handler) peerTrusted(r *http.Request) bool {
	addr, err := netip.ParseAddr(socketIP(r))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range h.trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// socketIP is the RemoteAddr host.
func socketIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ClientIP is the client address. When the socket peer is a trusted
// proxy (Caddy, which puts the address it saw last in X-Forwarded-For),
// the rightmost entry is the client; anything to its left is
// client-supplied. Otherwise, and as the fallback, it is the socket
// address.
func ClientIP(r *http.Request, trustedPeer bool) string {
	if parts := xffEntries(r); trustedPeer && len(parts) > 0 {
		if ip := net.ParseIP(parts[len(parts)-1]); ip != nil {
			return ip.String()
		}
	}
	return canonicalOr(socketIP(r))
}

// forwardedFor is the outbound X-Forwarded-For. From a trusted peer it is
// the inbound chain with clientIP as its rightmost entry (Caddy's own
// address is never added); from anyone else it is clientIP alone.
func forwardedFor(r *http.Request, clientIP string, trustedPeer bool) string {
	if !trustedPeer {
		return clientIP
	}
	parts := xffEntries(r)
	if n := len(parts); n == 0 || canonicalIP(parts[n-1]) != clientIP {
		parts = append(parts, clientIP)
	}
	return strings.Join(parts, ", ")
}

// limiterKey is the per-client bucket: IPv4 by address, IPv6 by /64 (one
// host commonly holds a whole /64, so per-address keys are free to
// rotate).
func limiterKey(clientIP string) string {
	addr, err := netip.ParseAddr(clientIP)
	if err != nil {
		return "ip:" + clientIP
	}
	addr = addr.WithZone("").Unmap()
	if addr.Is6() {
		return "ip:" + netip.PrefixFrom(addr, 64).Masked().String()
	}
	return "ip:" + addr.String()
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

func canonicalOr(s string) string {
	if c := canonicalIP(s); c != "" {
		return c
	}
	return s
}
