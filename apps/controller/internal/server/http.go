// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/clickhouse"
	"github.com/hanfour/bamboo/apps/controller/internal/config"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/handlers"
	"github.com/hanfour/bamboo/apps/controller/internal/mail"
	"github.com/hanfour/bamboo/apps/controller/internal/metrics"
	"github.com/hanfour/bamboo/apps/controller/internal/releasefeed"
	"github.com/hanfour/bamboo/apps/controller/internal/webhooks"
)

// HTTPServer hosts the OIDC redirect / callback routes plus a thin
// JSON REST bridge under /api/v1/* that the Web UI consumes. The gRPC
// surface remains the canonical API; REST is a SSR-friendly read-only
// projection in Phase 1.
type HTTPServer struct {
	addr        string
	srv         *http.Server
	pool        *db.Pool // opens per-request WithTenant txs for the RLS backstop (ADR-0014)
	providers   map[string]auth.OIDCProvider
	tenants     *repo.Tenants
	users       *repo.Users
	peers       *repo.Peers
	policies    *repo.Policies
	relays      *repo.Relays
	audits      *repo.AuditLogs
	keys        *repo.PreAuthKeys
	dns         *repo.TenantDNS
	invitations *repo.UserInvitations
	webhooks    *repo.Webhooks
	apiTokens   *repo.APITokens
	revoked     *repo.RevokedSessions
	mailer      *mail.Sender
	publicURL   string // for /invite links in invitation email; falls back to baseURL
	traces      *clickhouse.Traces
	anomalies   *clickhouse.Anomalies
	connEvents  *clickhouse.ConnectionEvents
	coord       *handlers.CoordinatorHandler
	metrics     *metrics.Registry
	publisher   *webhooks.Publisher
	releaseFeed *releasefeed.Feed
	// regionMap resolves a client IP to a region label for the §4 P2
	// stage 5.5 server-side region detection. Populated from the
	// BAMBOO_REGION_CIDRS env var at NewHTTPServer time; empty
	// (zero entries) when no env is set, in which case every
	// resolveRegion call returns "" and the register response
	// omits preferredRegion.
	regionMap *regionMap
	secret    []byte
	// relaySecret signs relay tokens. Kept separate from `secret` so a
	// relay-host compromise cannot forge session JWTs (audit C-1). Nil
	// until SetRelaySecret is called; relayTokenSecret() then falls back
	// to `secret` (single-secret deploys and tests).
	relaySecret []byte
	// relaySigningKey, when non-nil, signs relay tokens with Ed25519
	// instead of HMAC — the relay verifies with only the public half, so
	// a relay-host compromise can't forge tokens (audit C-1 root fix).
	// Nil ⇒ fall back to HMAC (relayTokenSecret). Set via SetRelaySigningKey.
	relaySigningKey ed25519.PrivateKey
	baseURL         string
	ttl             time.Duration
	requireAuth     bool
	// rateLimit holds per-IP token-bucket limiters for the brute-force
	// surfaces (login / register / relay-token). Nil ⇒ limiting off (the
	// default; tests and the e2e fixture never enable it). Set via
	// SetRateLimiting, which the production wiring calls (audit H-4).
	rateLimit *rateLimiters
}

// NewHTTPServer constructs the OIDC + REST HTTP frontend. ch may be nil
// (REST recommendation endpoints degrade gracefully). coord is the
// shared CoordinatorHandler — REST peer endpoints delegate to it so
// the gRPC and REST paths share validation, IP allocation, audit log,
// and the events bus.
func NewHTTPServer(
	addr string,
	pool *db.Pool,
	providers map[string]auth.OIDCProvider,
	ch *clickhouse.Conn,
	secret []byte,
	baseURL string,
	ttl time.Duration,
	coord *handlers.CoordinatorHandler,
	feed *releasefeed.Feed,
) *HTTPServer {
	mux := http.NewServeMux()
	h := &HTTPServer{
		addr:        addr,
		pool:        pool,
		providers:   providers,
		tenants:     repo.NewTenants(pool),
		users:       repo.NewUsers(pool),
		peers:       repo.NewPeers(pool),
		policies:    repo.NewPolicies(pool),
		relays:      repo.NewRelays(pool),
		audits:      repo.NewAuditLogs(pool),
		keys:        repo.NewPreAuthKeys(pool),
		dns:         repo.NewTenantDNS(pool),
		invitations: repo.NewUserInvitations(pool),
		webhooks:    repo.NewWebhooks(pool),
		apiTokens:   repo.NewAPITokens(pool),
		revoked:     repo.NewRevokedSessions(pool),
		// Default mailer is the no-op sender — server boot calls
		// SetMailer to wire in the real SMTP relay when configured.
		mailer:      mail.New(config.SMTPConfig{}),
		traces:      clickhouse.NewTraces(ch),
		anomalies:   clickhouse.NewAnomalies(ch),
		connEvents:  clickhouse.NewConnectionEvents(ch),
		coord:       coord,
		releaseFeed: feed,
		// metrics.NewRegistry constructs a fresh prometheus.Registry
		// scoped to this HTTPServer so tests stay isolated. version
		// + commit default to "unknown"; the production wiring calls
		// SetBuildInfo with the ldflags-injected values once those
		// are plumbed through the cmd entrypoint (follow-up).
		metrics: metrics.NewRegistry("", ""),
		secret:  secret,
		baseURL: baseURL,
		ttl:     ttl,
	}
	// §4 P2 stage 5.5: build the IP→region table from
	// BAMBOO_REGION_CIDRS (e.g.
	// "us-east-1=10.0.0.0/8,ap-northeast-1=172.16.0.0/12").
	// Empty env ⇒ empty matcher, every resolveRegion returns ""
	// and the register response omits preferredRegion.
	rm, warnings := parseRegionMap(os.Getenv("BAMBOO_REGION_CIDRS"))
	for _, w := range warnings {
		slog.Warn(w)
	}
	h.regionMap = rm
	// Webhook publisher (§4 P2). The audit repo hook fires on every
	// successful Insert; the publisher's bounded channel + worker
	// goroutine handle delivery off the audit-write path. The
	// worker itself starts in Run — keeping the construction here
	// means Test fixtures that don't call Run still get audit-hook
	// wiring (events queue up in memory; if the test never reads,
	// they age out when the channel fills + are dropped via the
	// "queue full" warning path).
	h.publisher = webhooks.New(h.webhooks, nil)
	h.audits.SetHook(h.publisher)

	// Per-tenant gauges run on each /metrics scrape via the
	// prometheus.Collector interface. Failure to register a
	// duplicate collector would mean the metrics package itself is
	// double-allocating — fatal at startup, not silent at scrape
	// time. Production NewHTTPServer is called once per process; the
	// e2e fixture (which spins fresh HTTPServers per test) also
	// instantiates a fresh Registry so no collision.
	tenantCollector := metrics.NewTenantCollector(pool)
	if err := tenantCollector.Register(h.metrics); err != nil {
		slog.Warn("metrics: tenant collector registration failed", "err", err)
	}
	mux.HandleFunc("/auth/", h.routeAuth)
	mux.HandleFunc("/auth/sign-out", h.handleSignOut)
	mux.HandleFunc("/api/v1/admin/relays", h.routeAdminRelays)
	mux.HandleFunc("/api/v1/admin/users/", h.routeAdminUsers)
	mux.HandleFunc("/api/v1/admin/audit-log.csv", h.routeAdminAuditExport)
	mux.HandleFunc("/api/v1/relay-token", h.routeRelayToken)
	mux.HandleFunc("/api/v1/", h.routeAPI)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// /metrics serves the controller's Prometheus scrape (§4 P2
	// observability). Mounted alongside /healthz on the existing
	// HTTP port rather than a dedicated metrics port so a single
	// scrape target reaches both — operators running self-hosted
	// don't need to expose a second port through their firewall.
	mux.Handle("/metrics", h.metrics.Handler())
	h.srv = &http.Server{
		Addr:              addr,
		Handler:           withCORS(metricsMiddleware(h.metrics, h.rateLimitMiddleware(mux)), parseCORSOrigins(os.Getenv("BAMBOO_CORS_ORIGINS"))),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	return h
}

// SetBuildInfo records the version + commit on the metrics
// registry's bamboo_build_info gauge. Called from the cmd
// entrypoint once the ldflags-injected build vars are plumbed
// through, so dashboards can group panels by deploy. Idempotent;
// safe to call before or after Start.
func (h *HTTPServer) SetBuildInfo(version, commit string) {
	if h == nil || h.metrics == nil {
		return
	}
	h.metrics.SetBuildInfo(version, commit)
}

// metricsMiddleware wraps the mux so every incoming request bumps
// bamboo_http_requests_total + bamboo_http_request_duration_seconds.
// Route labels are *patterns* (e.g. "/api/v1/peers/{id}") rather
// than raw paths so a busy tenant with many UUIDs doesn't blow up
// the Prometheus time-series count. /metrics scrapes skip
// instrumentation — scraping yourself is a self-reference loop the
// dashboards would have to filter out anyway.
func metricsMiddleware(reg *metrics.Registry, next http.Handler) http.Handler {
	if reg == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := normalizeRoute(r.URL.Path)
		reg.HTTPRequestsTotal.WithLabelValues(r.Method, route, metrics.StatusFamily(rec.status)).Inc()
		reg.HTTPRequestDurationSeconds.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

// statusRecorder wraps http.ResponseWriter to capture the status
// code so the metrics middleware can label by status family.
// Standard pattern, kept private to this file.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

// Flush passes through to the underlying ResponseWriter when it
// supports flushing — needed so the SSE WatchPeers stream keeps
// working through this middleware (without Flush the keepalive
// comments buffer and never reach the client).
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// normalizeRoute collapses UUID-bearing paths to patterns so the
// metrics cardinality stays bounded. The router itself uses
// strings.CutPrefix / CutSuffix to dispatch, so we mirror its
// shape here: /api/v1/peers/{id} → "/api/v1/peers/{id}",
// /api/v1/peers/{id}/events → "/api/v1/peers/{id}/events", etc.
//
// Unknown paths fall through as-is so a new endpoint introduced
// without a matching pattern still shows up in metrics — under its
// own series, which is the right signal that the pattern table
// needs updating.
func normalizeRoute(path string) string {
	// Fast-path the static endpoints first.
	switch path {
	case "/healthz", "/metrics", "/api/v1/", "/api/v1",
		"/api/v1/policy", "/api/v1/policy/validate", "/api/v1/policy/simulate",
		"/api/v1/policy/revisions", "/api/v1/policy/rollback",
		"/api/v1/logs", "/api/v1/activity", "/api/v1/recommendations",
		"/api/v1/me", "/api/v1/users", "/api/v1/invitations",
		"/api/v1/preauth-keys", "/api/v1/dns", "/api/v1/tenant",
		"/api/v1/overview", "/api/v1/peers/register", "/api/v1/peers/heartbeat",
		"/api/v1/peers/watch", "/api/v1/relay-token",
		"/api/v1/admin/relays", "/api/v1/admin/audit-log.csv":
		return path
	}
	// /api/v1/peers/{id}/<subpath> family — collapse the UUID.
	if rest, ok := strings.CutPrefix(path, "/api/v1/peers/"); ok && rest != "" {
		for _, sub := range []string{
			"events", "connection-events", "route-conflicts",
			"approve", "reject", "routes", "exit-node", "nat64-egress",
		} {
			if strings.HasSuffix(rest, "/"+sub) {
				return "/api/v1/peers/{id}/" + sub
			}
		}
		// Bare /api/v1/peers/{id}
		if !strings.Contains(rest, "/") {
			return "/api/v1/peers/{id}"
		}
	}
	// /api/v1/preauth-keys/{id}/revoke
	if rest, ok := strings.CutPrefix(path, "/api/v1/preauth-keys/"); ok && rest != "" {
		if strings.HasSuffix(rest, "/revoke") {
			return "/api/v1/preauth-keys/{id}/revoke"
		}
	}
	// /api/v1/admin/users/{id}/{action} — collapse the UUID slot
	// so Prometheus label cardinality stays bounded across whatever
	// admin user actions live under this prefix.
	if rest, ok := strings.CutPrefix(path, "/api/v1/admin/users/"); ok && rest != "" {
		if strings.HasSuffix(rest, "/erase") {
			return "/api/v1/admin/users/{id}/erase"
		}
		if strings.HasSuffix(rest, "/sign-out-all") {
			return "/api/v1/admin/users/{id}/sign-out-all"
		}
	}
	// /auth/<provider>/{start,callback}, /auth/sign-out
	if strings.HasPrefix(path, "/auth/") {
		if path == "/auth/sign-out" {
			return path
		}
		parts := strings.SplitN(strings.TrimPrefix(path, "/auth/"), "/", 3)
		if len(parts) >= 2 {
			return "/auth/{provider}/" + parts[1]
		}
		return "/auth/{provider}"
	}
	return path
}

// withCORS adds CORS headers. When allowed is empty it echoes the dev-
// friendly wildcard ("*") so a local Next.js dev server can call the
// controller. When allowed is non-empty (BAMBOO_CORS_ORIGINS in prod,
// audit M-5), it reflects the request Origin only if it's on the
// allowlist — any other origin gets no CORS header and the browser
// blocks the cross-origin read.
func withCORS(next http.Handler, allowed map[string]bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acao := ""
		if len(allowed) == 0 {
			acao = "*"
		} else if origin := r.Header.Get("Origin"); origin != "" && allowed[origin] {
			acao = origin
			w.Header().Set("Vary", "Origin")
		}
		if acao != "" {
			w.Header().Set("Access-Control-Allow-Origin", acao)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Tenant-Slug")
			w.Header().Set("Access-Control-Max-Age", "300")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// parseCORSOrigins builds the allowlist from a comma-separated env value
// (e.g. "https://bamboo.example.com,https://admin.example.com"). Empty
// input ⇒ nil map ⇒ withCORS falls back to the "*" dev default.
func parseCORSOrigins(env string) map[string]bool {
	env = strings.TrimSpace(env)
	if env == "" {
		return nil
	}
	out := make(map[string]bool)
	for _, o := range strings.Split(env, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out[o] = true
		}
	}
	return out
}

// Handler returns the wired HTTP handler. Used by the e2e fixture so
// tests can drive /auth/* and /api/v1/* through httptest.NewServer
// without binding a real port. Production callers should use Run.
func (h *HTTPServer) Handler() http.Handler {
	return h.srv.Handler
}

// SetRequireAuth flips the server into prod-mode auth. When enabled,
// REST /api/v1/* endpoints reject unauthenticated requests with 401
// instead of falling back to the X-Tenant-Slug header. Peer-id-only
// paths (/peers/register, /peers/heartbeat, /peers/watch) and
// /api/v1/me are exempt so client onboarding and the Web's signed-out
// landing state still work. Callers wire this from
// cfg.Auth.RequireAuth (env: BAMBOO_REQUIRE_AUTH).
func (h *HTTPServer) SetRequireAuth(require bool) {
	h.requireAuth = require
}

// SetRelaySecret sets the dedicated relay-token signing key (audit
// C-1). When unset or empty, relay tokens are signed with the session
// secret — the backward-compatible single-secret behavior. Callers wire
// this from cfg.Auth.ResolvedRelaySecret (env: BAMBOO_RELAY_SECRET).
func (h *HTTPServer) SetRelaySecret(secret []byte) {
	h.relaySecret = secret
}

// SetRelaySigningKey enables Ed25519 relay-token signing (audit C-1 root
// fix). When set, relay tokens are signed asymmetrically and the relay
// verifies with only the public half. Nil (the default) keeps the HMAC
// path. Callers wire this from cfg.Auth.RelaySigningKey via
// auth.ParseRelaySigningKey (env: BAMBOO_RELAY_SIGNING_KEY).
func (h *HTTPServer) SetRelaySigningKey(key ed25519.PrivateKey) {
	h.relaySigningKey = key
}

// relayTokenSecret returns the key used to sign relay tokens: the
// dedicated relaySecret when configured, else the session secret.
func (h *HTTPServer) relayTokenSecret() []byte {
	if len(h.relaySecret) > 0 {
		return h.relaySecret
	}
	return h.secret
}

// SetMailer wires the SMTP sender used by invitation email delivery
// + the public base URL used to build the /invite link in the email
// body. Called from server boot once SMTP config has been loaded.
// Calling with a nil sender or empty publicURL leaves the prior
// values in place (so a partial config update is a no-op rather
// than a downgrade).
func (h *HTTPServer) SetMailer(sender *mail.Sender, publicURL string) {
	if sender != nil {
		h.mailer = sender
	}
	if publicURL != "" {
		h.publicURL = publicURL
	}
}

// StartPublisher launches the webhook delivery worker goroutine.
// Exposed separately from Run so test fixtures that mount Handler()
// directly (without ListenAndServe) can still drain the audit-hook
// queue. The worker exits when ctx is canceled.
//
// Idempotent: calling twice spawns two workers, both draining the
// same channel. Acceptable because the channel ordering still works
// — Go's channel receive is multi-consumer safe — but production
// only ever wants one. Tests typically call this exactly once.
func (h *HTTPServer) StartPublisher(ctx context.Context) {
	if h == nil || h.publisher == nil {
		return
	}
	go h.publisher.Run(ctx)
}

// inviteReaperInterval is how often the auto-revoke reaper sweeps
// for expired invitations. 5 minutes is short enough that an
// admin re-inviting the same email after a previous expiry sees
// the partial-unique-index slot freed within an admin coffee
// break, and long enough that the DB load stays negligible
// (one query per tenant cluster every 5 min).
//
// Package-scoped non-const so tests can override.
var inviteReaperInterval = 5 * time.Minute

// StartInviteReaper launches the background goroutine that
// expires unredeemed invitations past their expires_at. The
// goroutine exits when ctx is canceled. Exposed separately from
// Run so e2e tests can call it on demand.
//
// Each expired row gets one audit event with actor_type=system,
// action=invitation.expire — webhook subscribers to
// "invitation." get auto-revoke notifications for free.
func (h *HTTPServer) StartInviteReaper(ctx context.Context) {
	if h == nil || h.invitations == nil {
		return
	}
	go func() {
		// One immediate sweep on startup so an operator restarting
		// the controller after a long downtime sees stale invites
		// reaped within seconds rather than waiting for the first
		// tick. Subsequent sweeps follow the interval.
		h.reapExpiredInvitesOnce(ctx)
		t := time.NewTicker(inviteReaperInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.reapExpiredInvitesOnce(ctx)
			}
		}
	}()
}

// reapExpiredInvitesOnce runs one pass of RevokeExpired + audit
// log. Errors are logged but do not abort the reaper goroutine —
// transient DB failures get retried on the next tick.
func (h *HTTPServer) reapExpiredInvitesOnce(ctx context.Context) {
	expired, err := h.invitations.RevokeExpired(ctx, time.Now().UTC())
	if err != nil {
		slog.Warn("invite reaper: RevokeExpired", "err", err)
		return
	}
	if len(expired) == 0 {
		return
	}
	slog.Info("invite reaper: revoked expired invitations", "count", len(expired))
	for _, e := range expired {
		tenantID := e.TenantID
		ev := &repo.AuditEvent{
			TenantID:     &tenantID,
			ActorType:    "system",
			Action:       "invitation.expire",
			ResourceType: "invitation",
			ResourceID:   &e.ID,
		}
		// Diff captures the email so the audit row stays
		// human-readable without forcing the reader to cross-
		// reference the user_invitations row (which still exists
		// — revoked, not deleted).
		ev.Diff = marshalDiffJSON(map[string]any{"email": e.Email})
		if err := h.audits.Insert(ctx, ev); err != nil {
			slog.Warn("invite reaper: audit insert", "invitation_id", e.ID, "err", err)
		}
	}
}

// auditRetentionInterval is how often the audit-log retention
// reaper sweeps for rows past their retention window. Hourly is
// far less frequent than the 5-minute invite reaper because the
// audit table is large + slow-growing — sweeping more often would
// burn IOPS for no benefit. Package-scoped non-const so tests can
// override.
var auditRetentionInterval = 1 * time.Hour

// auditRetentionDays is the default retention window (in days)
// when BAMBOO_AUDIT_RETENTION_DAYS is unset. 365 picked from SOC 2
// recommendation: a year of audit covers an annual audit cycle
// + a quarter of overlap for incident review. Operators with
// stricter requirements (HIPAA 6yr, financial 7yr) set the env
// var; operators wanting aggressive cleanup set a smaller value.
//
// Zero or negative parsed value disables the reaper entirely —
// equivalent to "keep audit forever, I'll manage it out-of-band".
const auditRetentionDaysDefault = 365

// auditRetentionWindow reads BAMBOO_AUDIT_RETENTION_DAYS and
// returns the retention window. Returns 0 (zero duration) when
// retention is explicitly disabled (env=0) or unparseable, so the
// reaper can treat zero as "don't sweep". Logs a warning for an
// unparseable value so the operator sees the typo rather than
// silently falling back to the default.
func auditRetentionWindow() time.Duration {
	raw := os.Getenv("BAMBOO_AUDIT_RETENTION_DAYS")
	if raw == "" {
		return time.Duration(auditRetentionDaysDefault) * 24 * time.Hour
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		slog.Warn("audit retention: BAMBOO_AUDIT_RETENTION_DAYS unparseable; reaper disabled",
			"value", raw, "err", err)
		return 0
	}
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * 24 * time.Hour
}

// StartAuditRetentionReaper launches the background goroutine that
// deletes audit_log rows older than the configured retention
// window. Pairs with #182's append-only triggers — immutability
// + retention together are the SOC 2 audit log story.
//
// Disabled (no-op goroutine) when:
//   - BAMBOO_AUDIT_RETENTION_DAYS=0 (operator explicitly opts out)
//   - the value is unparseable (warned at startup)
//   - h or h.audits is nil (test fixture without a DB)
//
// The goroutine exits when ctx is canceled. Each sweep is one
// `DELETE FROM audit_log WHERE occurred_at < now() - window`
// inside a transaction with SET LOCAL bamboo.allow_audit_delete
// (see #182). Hourly cadence — audit table is large, doesn't
// need more frequent sweeps.
func (h *HTTPServer) StartAuditRetentionReaper(ctx context.Context) {
	if h == nil || h.audits == nil {
		return
	}
	window := auditRetentionWindow()
	if window == 0 {
		slog.Info("audit retention: disabled (BAMBOO_AUDIT_RETENTION_DAYS=0 or unparseable)")
		return
	}
	slog.Info("audit retention: enabled", "window_days", int(window/24/time.Hour))
	go func() {
		// One immediate sweep on startup so a controller restart
		// after a long downtime catches up the backlog within the
		// first minute rather than waiting an hour for the first
		// tick.
		h.reapAuditOnce(ctx, window)
		t := time.NewTicker(auditRetentionInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.reapAuditOnce(ctx, window)
			}
		}
	}()
}

// reapAuditOnce deletes one batch of expired audit rows. Errors
// are logged but don't abort the reaper goroutine — a transient
// DB failure retries on the next tick.
//
// The DELETE itself is unbounded by row count: pgx will buffer a
// large delete plan in memory but the transaction commits or
// rolls back as one unit. At the cadence + window we run, daily
// volumes are O(thousands) — well under any practical concern.
// If audit insert rate ever spikes (e.g. high-volume webhook
// flap), a future PR can add a LIMIT + loop.
func (h *HTTPServer) reapAuditOnce(ctx context.Context, window time.Duration) {
	cutoff := time.Now().UTC().Add(-window)
	deleted, err := h.audits.DeleteOlderThan(ctx, cutoff)
	if err != nil {
		slog.Warn("audit retention: delete", "err", err, "cutoff", cutoff)
		return
	}
	if deleted > 0 {
		slog.Info("audit retention: swept", "deleted", deleted, "cutoff", cutoff)
	}
}

// revokedSessionsInterval governs how often the reaper prunes
// revoked_sessions rows whose underlying JWT has already expired.
// Hourly matches auditRetentionInterval — the table is small (one
// row per sign-out) and the JWT verifier rejects expired tokens on
// its own, so a delayed sweep doesn't widen any window.
// Package-scoped non-const so tests can override.
var revokedSessionsInterval = 1 * time.Hour

// StartRevokedSessionsReaper launches the goroutine that prunes
// revoked_sessions rows whose JWT has passed its natural exp. Pairs
// with the slice-3a denylist — keeping the table bounded so the
// per-request IsRevoked lookup stays cheap.
//
// Disabled (no-op) when h.revoked is nil — applies to unit-test
// fixtures that don't wire DB. The goroutine exits on ctx.Done.
func (h *HTTPServer) StartRevokedSessionsReaper(ctx context.Context) {
	if h == nil || h.revoked == nil {
		return
	}
	go func() {
		// Immediate sweep on startup so a controller restart after a
		// long downtime catches up the backlog within the first
		// minute rather than waiting an hour for the first tick.
		h.reapRevokedSessionsOnce(ctx)
		t := time.NewTicker(revokedSessionsInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.reapRevokedSessionsOnce(ctx)
			}
		}
	}()
}

func (h *HTTPServer) reapRevokedSessionsOnce(ctx context.Context) {
	deleted, err := h.revoked.DeleteExpired(ctx, time.Now().UTC())
	if err != nil {
		slog.Warn("revoked_sessions reaper: delete", "err", err)
		return
	}
	if deleted > 0 {
		slog.Info("revoked_sessions reaper: swept", "deleted", deleted)
	}
}

// StartReleaseFeedPoller spawns the GitHub releases poller goroutine
// in the background. Nil-safe: a disabled deployment (feed=nil from
// NewHTTPServer) gets a silent no-op. Idempotent in correctness but
// not in resource cost — calling twice spawns two pollers both
// writing under the Feed's internal RWMutex; harmless but wasteful.
// Production only calls this once via HTTPServer.Run.
func (h *HTTPServer) StartReleaseFeedPoller(ctx context.Context) {
	if h.releaseFeed == nil {
		return
	}
	slog.Info("release feed: poller started",
		"repo", h.releaseFeed.Repo(),
		"interval", h.releaseFeed.Interval())
	go h.releaseFeed.Run(ctx)
}

// Run blocks until ctx is canceled or the listener errors.
func (h *HTTPServer) Run(ctx context.Context) error {
	// Start the webhook delivery worker so audit events fired during
	// the server's lifetime get drained from the in-memory queue.
	// Worker exits when ctx is canceled; the HTTP shutdown path
	// below also waits for ctx cancellation so they unwind together.
	h.StartPublisher(ctx)
	h.StartInviteReaper(ctx)
	h.StartAuditRetentionReaper(ctx)
	h.StartRelayHealthReaper(ctx)
	h.StartNAT64EgressHealthReaper(ctx)
	h.StartRevokedSessionsReaper(ctx)
	h.StartReleaseFeedPoller(ctx)
	h.StartRateLimitCleanup(ctx)
	errCh := make(chan error, 1)
	go func() {
		slog.Info("HTTP server listening", "addr", h.addr)
		err := h.srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

// handleSignOut clears the bamboo_session cookie. Idempotent; a request
// with no cookie still receives a 200 + Set-Cookie that invalidates any
// stale session in the browser. Redirects back to the referrer when
// the request originated from the Web UI; returns 200 plain text
// otherwise.
func (h *HTTPServer) handleSignOut(w http.ResponseWriter, r *http.Request) {
	// Audit + revoke only when a verifiable session cookie is
	// present. An unauthenticated sign-out (no cookie, or a forged
	// one) is a no-op for state but we don't want to log
	// "session.signout" for an actor we can't identify, and we
	// can't add to the revocation denylist without a jti.
	if cookie, cerr := r.Cookie(SessionCookieName); cerr == nil && cookie.Value != "" {
		if claims, verr := auth.VerifySessionToken(h.secret, cookie.Value); verr == nil {
			auditSessionSignout(r.Context(), h.audits, claims.TenantID, claims.UserID, requestIPString(r), r.UserAgent())
			// jti is empty for legacy tokens minted before slice 3a;
			// skip the insert there since there's nothing to key
			// off. The natural TTL still drops the token at exp.
			if h.revoked != nil && claims.JTI != uuid.Nil {
				if rerr := h.revoked.Insert(r.Context(), claims.JTI, claims.UserID, time.Unix(claims.ExpiresAt, 0)); rerr != nil {
					slog.Warn("revoked_sessions insert on sign-out", "err", rerr, "jti", claims.JTI)
				}
			}
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	if ref := r.Referer(); ref != "" {
		http.Redirect(w, r, ref, http.StatusSeeOther)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("signed out"))
}

// routeAuth dispatches `/auth/{provider}/{action}`.
//
// Supported routes:
//
//	/auth/{provider}/login?tenant={slug}    redirect to provider for consent
//	/auth/{provider}/callback?code=&state=  finish the flow, mint session JWT
func (h *HTTPServer) routeAuth(w http.ResponseWriter, r *http.Request) {
	// path layout: /auth/{provider}/{action}
	parts := splitPath(r.URL.Path)
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	providerName := parts[1]
	action := parts[2]
	provider, ok := h.providers[providerName]
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}

	switch action {
	case "login":
		h.handleLogin(w, r, provider)
	case "callback":
		h.handleCallback(w, r, provider)
	default:
		http.NotFound(w, r)
	}
}

func (h *HTTPServer) handleLogin(w http.ResponseWriter, r *http.Request, provider auth.OIDCProvider) {
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		tenant = "default"
	}

	// ?invite=<bki_token> kicks the login into invite-redeem mode.
	// When present, we validate the token's authenticity + freshness
	// up-front (better UX than letting Google complete its dance and
	// then failing in the callback), and we override the tenant slug
	// from the invitation row so a malicious tenant query-param can't
	// route the invite into the wrong tailnet.
	inviteToken := r.URL.Query().Get("invite")
	var inviteID string
	if inviteToken != "" {
		inv, slug, err := h.resolveInvite(r.Context(), inviteToken)
		if err != nil {
			http.Error(w, "invite: "+err.Error(), http.StatusBadRequest)
			return
		}
		inviteID = inv.ID.String()
		tenant = slug
	}

	// ?app_callback=bamboo://... routes the final session JWT back into
	// the native app instead of rendering the browser success page. We
	// only accept callback URIs whose scheme matches a fixed allow-list
	// (currently just the bamboo:// scheme registered by the macOS / iOS
	// app); any other scheme would let an attacker craft a login link
	// that ships a freshly minted JWT to their own deep link. The chosen
	// URI rides inside the signed state so it can't be swapped between
	// /login and /callback.
	appCallback, err := validateAppCallback(r.URL.Query().Get("app_callback"))
	if err != nil {
		http.Error(w, "app_callback: "+err.Error(), http.StatusBadRequest)
		return
	}

	// PKCE (audit M-6): mint a per-flow verifier, stash it in the signed
	// state, and send its S256 challenge on the authorize URL. The provider
	// then binds the code to this attempt, so an intercepted code is
	// useless without the verifier (which never leaves the signed state).
	verifier := auth.GeneratePKCEVerifier()
	state, err := auth.IssueOIDCState(h.secret, tenant, inviteID, appCallback, verifier, 10*time.Minute)
	if err != nil {
		http.Error(w, "issue state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	redirect := fmt.Sprintf("%s/auth/%s/callback", h.baseURL, provider.Name())
	http.Redirect(w, r, provider.AuthURL(state, redirect, verifier), http.StatusFound)
}

// validateAppCallback narrows the accepted app_callback URI to ones
// whose scheme matches a fixed allow-list. Empty input is allowed
// (browser flow). Returns the original URI on success — callers store
// it verbatim in the signed state.
//
// The current allow-list is just `bamboo://` (custom URL scheme
// registered by the macOS / iOS bamboo app). Adding new native
// clients means appending to this slice and updating their CFBundle-
// URLTypes so the scheme is reserved on the device.
func validateAppCallback(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	allowed := []string{"bamboo://"}
	for _, prefix := range allowed {
		if strings.HasPrefix(raw, prefix) {
			return raw, nil
		}
	}
	return "", fmt.Errorf("unsupported scheme")
}

func (h *HTTPServer) handleCallback(w http.ResponseWriter, r *http.Request, provider auth.OIDCProvider) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	claims, err := auth.VerifyOIDCState(h.secret, state)
	if err != nil {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	redirect := fmt.Sprintf("%s/auth/%s/callback", h.baseURL, provider.Name())
	identity, err := provider.Exchange(r.Context(), code, redirect, claims.Verifier)
	if err != nil {
		http.Error(w, "exchange: "+err.Error(), http.StatusBadGateway)
		return
	}

	tenant, err := h.tenants.GetOrCreate(r.Context(), claims.Tenant, "Default Tenant", repo.DefaultTenantCIDR)
	if err != nil {
		http.Error(w, "tenant: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Invite-redeem path: when state carries an invitation id, re-
	// validate the invitation (rows may have been revoked or expired
	// while the user was at Google) and ensure the OIDC identity's
	// email matches the invited address. Email-mismatch is hard-
	// rejected — letting any signed-in account redeem an invitation
	// would defeat the purpose of binding it to an email.
	//
	// isAdmin for the resulting user comes from the invitation when
	// we're creating the row for the first time. UpsertOIDC does not
	// touch is_admin on conflict, so an existing user's role is left
	// alone — an invitation cannot silently promote (or demote) an
	// already-onboarded user.
	isAdminFromInvite := false
	var invitationID *uuid.UUID
	if claims.Invite != "" {
		invID, perr := uuid.Parse(claims.Invite)
		if perr != nil {
			http.Error(w, "invalid invite id in state", http.StatusBadRequest)
			return
		}
		inv, gerr := h.invitations.GetByID(r.Context(), invID)
		if gerr != nil {
			http.Error(w, "invite: "+gerr.Error(), http.StatusBadRequest)
			return
		}
		if inv.RevokedAt != nil {
			http.Error(w, "invitation has been revoked", http.StatusForbidden)
			return
		}
		if inv.AcceptedAt != nil {
			http.Error(w, "invitation has already been accepted", http.StatusForbidden)
			return
		}
		if time.Now().After(inv.ExpiresAt) {
			http.Error(w, "invitation has expired", http.StatusForbidden)
			return
		}
		if !strings.EqualFold(strings.TrimSpace(identity.Email), strings.TrimSpace(inv.Email)) {
			http.Error(w, fmt.Sprintf("this invitation is for %s; sign in with that account", inv.Email), http.StatusForbidden)
			return
		}
		isAdminFromInvite = inv.IsAdmin
		invitationID = &inv.ID
	}

	user, err := h.users.UpsertOIDC(r.Context(), &repo.User{
		TenantID:     tenant.ID,
		Email:        identity.Email,
		DisplayName:  identity.DisplayName,
		OIDCProvider: identity.Provider,
		OIDCSubject:  identity.Subject,
		IsAdmin:      isAdminFromInvite,
	})
	if err != nil {
		http.Error(w, "upsert user: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Atomically mark the invitation accepted by this user. The
	// repo's WHERE guards against the (very unlikely) race where two
	// concurrent callbacks try to redeem the same invite — second
	// one gets ErrNotFound and we surface a clear error so the
	// loser-of-the-race understands.
	if invitationID != nil {
		if mErr := h.invitations.MarkAccepted(r.Context(), *invitationID, user.ID); mErr != nil {
			if errors.Is(mErr, repo.ErrNotFound) {
				http.Error(w, "invitation was just accepted by another sign-in", http.StatusConflict)
				return
			}
			http.Error(w, "mark invitation accepted: "+mErr.Error(), http.StatusInternalServerError)
			return
		}
		// Audit row records who redeemed which invitation. tenantID
		// pointer + resource id match the other admin actions in
		// audit_log so the activity feed can group them.
		auditInviteAccepted(r.Context(), h.audits, tenant.ID, user.ID, *invitationID, identity.Email)
	}

	token, err := auth.IssueSessionToken(h.secret, auth.SessionClaims{
		UserID:   user.ID,
		TenantID: tenant.ID,
		SV:       user.SessionVersion,
	}, h.ttl)
	if err != nil {
		http.Error(w, "issue token: "+err.Error(), http.StatusInternalServerError)
		return
	}
	auditSessionStart(r.Context(), h.audits, tenant.ID, user.ID, identity.Provider, requestIPString(r), r.UserAgent(), h.ttl)

	// Set the session cookie so the Web UI (or any same-origin browser
	// caller) sees the token without needing to copy/paste. The CLI
	// flow continues to read the token from the rendered page below.
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(h.ttl.Seconds()),
	})

	// Native-app flow: when state carries an app_callback URI (vetted
	// at /login time against the scheme allow-list), redirect the
	// browser session there with the freshly minted token instead of
	// rendering the HTML success page. ASWebAuthenticationSession on
	// the device intercepts the bamboo:// navigation before it leaves
	// the in-app browser; the URL — including the token — is delivered
	// to the calling app, which parses it and writes to the Keychain.
	// We re-validate the scheme on the callback path as defence in
	// depth (state was signed at /login, but a code change that ever
	// stored an attacker-supplied value should fail closed here too).
	if claims.AppCallback != "" {
		if _, verr := validateAppCallback(claims.AppCallback); verr != nil {
			http.Error(w, "app_callback in state: "+verr.Error(), http.StatusBadRequest)
			return
		}
		appURL, perr := url.Parse(claims.AppCallback)
		if perr != nil {
			http.Error(w, "app_callback parse: "+perr.Error(), http.StatusBadRequest)
			return
		}
		q := appURL.Query()
		q.Set("token", token)
		q.Set("tenant", tenant.Slug)
		q.Set("email", identity.Email)
		appURL.RawQuery = q.Encode()
		http.Redirect(w, r, appURL.String(), http.StatusFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<meta charset="utf-8">
<title>bamboo session issued</title>
<style>body{font-family:system-ui;max-width:40rem;margin:3rem auto;padding:1rem}code{background:#f4f4f6;padding:.5rem;display:block;word-break:break-all}</style>
<h1>Logged in as %s</h1>
<p>Tenant: <strong>%s</strong></p>
<p>A session cookie has been set; the Web UI will pick it up automatically. The bearer token is also shown below for CLI use:</p>
<code>%s</code>
`, htmlEscape(identity.Email), htmlEscape(tenant.Slug), htmlEscape(token))
}

// SessionCookieName is the cookie name set by the OIDC callback. The
// REST middleware reads it; the gRPC bearer-token path stays unchanged
// because gRPC clients pass the token via the Authorization metadata.
const SessionCookieName = "bamboo_session"

// splitPath splits a URL path into segments without leading or trailing
// empties. "/auth/google/login" → ["auth","google","login"].
func splitPath(p string) []string {
	var out []string
	current := ""
	for _, r := range p {
		if r == '/' {
			if current != "" {
				out = append(out, current)
				current = ""
			}
		} else {
			current += string(r)
		}
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}

// htmlEscape escapes a string for safe inclusion in HTML.
func htmlEscape(s string) string {
	r := []rune(s)
	out := make([]rune, 0, len(r))
	for _, c := range r {
		switch c {
		case '&':
			out = append(out, []rune("&amp;")...)
		case '<':
			out = append(out, []rune("&lt;")...)
		case '>':
			out = append(out, []rune("&gt;")...)
		case '"':
			out = append(out, []rune("&quot;")...)
		case '\'':
			out = append(out, []rune("&#39;")...)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// resolveInvite walks an invite token through the validation gauntlet
// used by both the pre-OIDC `?invite=` handler and the post-OIDC
// callback re-check: parse → lookup → not-revoked → not-accepted →
// not-expired → hash-matches → tenant-lookup. Returns the invitation
// row + its tenant slug on success. Any failure returns a short
// human-friendly error string suitable for surfacing in an HTML
// error page; the caller decides the HTTP status.
func (h *HTTPServer) resolveInvite(ctx context.Context, token string) (*repo.UserInvitation, string, error) {
	invID, err := auth.ParseInviteToken(token)
	if err != nil {
		return nil, "", errors.New("invite token is malformed")
	}
	inv, err := h.invitations.GetByID(ctx, invID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, "", errors.New("invitation not found")
		}
		return nil, "", err
	}
	if inv.RevokedAt != nil {
		return nil, "", errors.New("invitation has been revoked")
	}
	if inv.AcceptedAt != nil {
		return nil, "", errors.New("invitation has already been accepted")
	}
	if time.Now().After(inv.ExpiresAt) {
		return nil, "", errors.New("invitation has expired")
	}
	if err := auth.VerifyHash(token, inv.TokenHash); err != nil {
		return nil, "", errors.New("invite token is invalid")
	}
	tenant, err := h.tenants.GetByID(ctx, inv.TenantID)
	if err != nil {
		return nil, "", fmt.Errorf("resolve tenant for invite: %w", err)
	}
	return inv, tenant.Slug, nil
}

// requestIsHTTPS reports whether the original client request was HTTPS,
// used for the session cookie's Secure flag (audit H-2). Direct TLS
// (r.TLS) is the obvious case; behind a TLS-terminating reverse proxy
// (Caddy) the controller sees plain HTTP, so we also trust the proxy's
// X-Forwarded-Proto. Trusting XFF here can only make the cookie MORE
// restrictive (a spoofed value can't downgrade a legit user's cookie),
// so it's safe; without it, prod behind Caddy shipped session cookies
// without Secure.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// requestIPString returns the best-effort client IP for audit
// recording, formatted as a string suitable for the audit_log
// IPAddress column. Empty string means we couldn't extract one — the
// audit row is still written but with NULL ip_address.
func requestIPString(r *http.Request) string {
	addr := clientIPFromRequest(r.RemoteAddr, r.Header.Get("X-Forwarded-For"))
	if !addr.IsValid() {
		return ""
	}
	return addr.String()
}

// auditSessionStart writes the audit row for a fresh OIDC sign-in.
// session.start is a SOC 2 "successful authentication" event — it
// pairs with session.signout to bound the session window in the audit
// feed. The diff carries the OIDC provider + ttl so an operator
// looking at the row sees both *how* the session was opened and *how
// long* it is good for (matters when BAMBOO_SESSION_TTL_HOURS varies
// across deploys).
func auditSessionStart(ctx context.Context, audits *repo.AuditLogs, tenantID, userID uuid.UUID, provider, ip, userAgent string, ttl time.Duration) {
	if audits == nil {
		return
	}
	diff, _ := json.Marshal(map[string]any{
		"provider":  provider,
		"ttlHours":  int(ttl / time.Hour),
		"expiresAt": time.Now().Add(ttl).UTC().Format(time.RFC3339),
	})
	ev := &repo.AuditEvent{
		TenantID:     &tenantID,
		ActorType:    "user",
		ActorID:      &userID,
		Action:       "session.start",
		ResourceType: "user",
		ResourceID:   &userID,
		Diff:         diff,
	}
	if ip != "" {
		ev.IPAddress = &ip
	}
	if userAgent != "" {
		ev.UserAgent = &userAgent
	}
	if err := audits.Insert(ctx, ev); err != nil {
		slog.Warn("audit session.start", "err", err, "user_id", userID)
	}
}

// auditSessionSignout writes the audit row for an interactive sign-out.
// Only emitted when the request carried a verifiable session cookie
// (see handleSignOut); unauthenticated sign-out hits are not logged
// because we can't attribute them to a real actor.
func auditSessionSignout(ctx context.Context, audits *repo.AuditLogs, tenantID, userID uuid.UUID, ip, userAgent string) {
	if audits == nil {
		return
	}
	ev := &repo.AuditEvent{
		TenantID:     &tenantID,
		ActorType:    "user",
		ActorID:      &userID,
		Action:       "session.signout",
		ResourceType: "user",
		ResourceID:   &userID,
	}
	if ip != "" {
		ev.IPAddress = &ip
	}
	if userAgent != "" {
		ev.UserAgent = &userAgent
	}
	if err := audits.Insert(ctx, ev); err != nil {
		slog.Warn("audit session.signout", "err", err, "user_id", userID)
	}
}

// auditInviteAccepted writes the audit row for a successful
// invitation redemption. Mirrors the shape used for peer.register /
// preauthkey.* — actor = the user who accepted, action label scoped
// to the user.invite namespace so the activity feed can group it
// with other admin actions.
func auditInviteAccepted(ctx context.Context, audits *repo.AuditLogs, tenantID, userID, invitationID uuid.UUID, email string) {
	if audits == nil {
		return
	}
	diff, _ := json.Marshal(map[string]any{"email": email})
	err := audits.Insert(ctx, &repo.AuditEvent{
		TenantID:     &tenantID,
		ActorType:    "user",
		ActorID:      &userID,
		Action:       "user.invite.accept",
		ResourceType: "user_invitation",
		ResourceID:   &invitationID,
		Diff:         diff,
	})
	if err != nil {
		slog.Warn("audit user.invite.accept", "err", err, "invitation_id", invitationID)
	}
}
