// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server wires the controller's gRPC and HTTP listeners and
// handles graceful shutdown.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/clickhouse"
	"github.com/hanfour/bamboo/apps/controller/internal/config"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/events"
	"github.com/hanfour/bamboo/apps/controller/internal/handlers"
	"github.com/hanfour/bamboo/apps/controller/internal/mail"
	"github.com/hanfour/bamboo/apps/controller/internal/releasefeed"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// Server holds the gRPC + HTTP servers and dependencies.
type Server struct {
	cfg  *config.Config
	pool *db.Pool
	grpc *grpc.Server
	http *HTTPServer
}

// New constructs a Server with all gRPC services registered.
// It does not start any listeners; call Run.
//
// ch may be nil; when nil, telemetry writes silently drop after a single
// warning (degraded mode). See clickhouse.Open.
func New(cfg *config.Config, pool *db.Pool, ch *clickhouse.Conn) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	if pool == nil {
		return nil, fmt.Errorf("nil pool")
	}

	providers := buildOIDCProviders(cfg)
	ttl := resolveSessionTTL(cfg.Auth.SessionTTL, 24*time.Hour)
	secret := []byte(cfg.Auth.SessionSecret)

	revoked := repo.NewRevokedSessions(pool)
	users := repo.NewUsers(pool)
	grpcSrv, authHandler, coordHandler := buildGRPCWithAuth(pool, ch, cfg.Auth.RequireAuth, secret, revoked, users)
	authHandler.SetOIDCConfig(cfg.Auth.OIDC.BaseURL, secret, ttl)

	// Construct the release-feed poller iff the config enables it.
	// New returns a non-nil *Feed; the disabled case passes nil to
	// NewHTTPServer so handler code stays branchless (Feed.Latest is
	// nil-safe and returns ("", false)).
	var feed *releasefeed.Feed
	if cfg.ReleaseFeed.IsEnabled() {
		feed = releasefeed.New(cfg.ReleaseFeed.Repo, cfg.ReleaseFeed.Interval)
	}

	httpSrv := NewHTTPServer(cfg.Server.HTTPAddr, pool, providers, ch, secret, cfg.Auth.OIDC.BaseURL, ttl, coordHandler, feed)
	httpSrv.SetRequireAuth(cfg.Auth.RequireAuth)
	// Relay tokens are signed with a dedicated key when BAMBOO_RELAY_SECRET
	// is set, else the session secret (audit C-1 — see ResolvedRelaySecret).
	httpSrv.SetRelaySecret([]byte(cfg.Auth.ResolvedRelaySecret()))
	// Ed25519 relay-token signing (audit C-1 root fix) when a signing key
	// is configured; otherwise the HMAC path above stands. Fail fast on a
	// malformed key rather than silently falling back to HMAC.
	if cfg.Auth.RelaySigningKey != "" {
		signKey, err := auth.ParseRelaySigningKey(cfg.Auth.RelaySigningKey)
		if err != nil {
			return nil, fmt.Errorf("parse BAMBOO_RELAY_SIGNING_KEY: %w", err)
		}
		httpSrv.SetRelaySigningKey(signKey)
	}
	// Per-IP brute-force limiting on login / register / relay-token
	// (audit H-4). On by default; set BAMBOO_RATE_LIMIT_DISABLED=true to
	// opt out (e.g. load testing).
	httpSrv.SetRateLimiting(os.Getenv("BAMBOO_RATE_LIMIT_DISABLED") != "true")
	coordHandler.SetRequireAuth(cfg.Auth.RequireAuth)
	// Wire SMTP. New() returns a no-op sender when SMTP is unconfigured;
	// the public base URL for invite links falls back to the OIDC base
	// when the SMTP-specific one isn't set (single-domain deploys).
	httpSrv.SetMailer(mail.New(cfg.SMTP), cfg.SMTP.PublicBaseURL)

	return &Server{
		cfg:  cfg,
		pool: pool,
		grpc: grpcSrv,
		http: httpSrv,
	}, nil
}

// buildGRPCWithAuth is the production wiring used by Server.New. It
// returns the underlying AuthHandler (so the caller can apply OIDC
// configuration) and the CoordinatorHandler (so the HTTP REST bridge
// can delegate peer register / heartbeat / watch to the same code path
// as gRPC). Tests use BuildGRPCServer instead, which takes the simpler
// path.
func buildGRPCWithAuth(pool *db.Pool, ch *clickhouse.Conn, requireAuth bool, sessionSec []byte, revoked *repo.RevokedSessions, users *repo.Users) (*grpc.Server, *handlers.AuthHandler, *handlers.CoordinatorHandler) {
	bus := events.NewBus()
	s := grpc.NewServer(
		grpc.UnaryInterceptor(requireAuthUnaryInterceptor(requireAuth, sessionSec, revoked, users)),
		grpc.StreamInterceptor(requireAuthStreamInterceptor(requireAuth, sessionSec, revoked, users)),
	)

	authHandler := handlers.NewAuthHandler(pool)
	coordHandler := handlers.NewCoordinatorHandler(pool, authHandler, bus)
	bamboov1.RegisterAuthServiceServer(s, authHandler)
	bamboov1.RegisterCoordinatorServiceServer(s, coordHandler)
	bamboov1.RegisterPolicyServiceServer(s, handlers.NewPolicyHandler(pool, ch, bus, authHandler))
	bamboov1.RegisterTelemetryServiceServer(s, handlers.NewTelemetryHandler(pool, ch, authHandler))
	// gRPC reflection exposes the full service schema — handy for grpcurl
	// in dev, needless attack-surface in prod (audit L-1). Off in prod
	// mode (require_auth) unless explicitly re-enabled.
	if !requireAuth || os.Getenv("BAMBOO_GRPC_REFLECTION") == "true" {
		reflection.Register(s)
	}

	return s, authHandler, coordHandler
}

// BuildGRPCServer constructs and returns a fully wired *grpc.Server with
// every bamboo service registered against pool. Reflection is enabled.
// Used by tests; they typically pass ch=nil for degraded telemetry.
func BuildGRPCServer(pool *db.Pool) *grpc.Server {
	bus := events.NewBus()
	s := grpc.NewServer()

	authHandler := handlers.NewAuthHandler(pool)
	bamboov1.RegisterAuthServiceServer(s, authHandler)
	bamboov1.RegisterCoordinatorServiceServer(s, handlers.NewCoordinatorHandler(pool, authHandler, bus))
	bamboov1.RegisterPolicyServiceServer(s, handlers.NewPolicyHandler(pool, nil, bus, authHandler))
	bamboov1.RegisterTelemetryServiceServer(s, handlers.NewTelemetryHandler(pool, nil, authHandler))

	reflection.Register(s)
	return s
}

// buildOIDCProviders returns the configured OIDC providers; entries with
// empty client_id are skipped so an unconfigured provider never appears.
func buildOIDCProviders(cfg *config.Config) map[string]auth.OIDCProvider {
	out := make(map[string]auth.OIDCProvider)
	if id := cfg.Auth.OIDC.Google.ClientID; id != "" {
		out["google"] = auth.NewGoogleProvider(id, cfg.Auth.OIDC.Google.ClientSecret)
	}
	if id := cfg.Auth.OIDC.GitHub.ClientID; id != "" {
		out["github"] = auth.NewGitHubProvider(id, cfg.Auth.OIDC.GitHub.ClientSecret)
	}
	return out
}

func parseTTL(raw string, fallback time.Duration) time.Duration {
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return d
}

// resolveSessionTTL picks the session JWT lifetime in this precedence:
// BAMBOO_SESSION_TTL_HOURS env (operator override) → cfg.Auth.SessionTTL
// (YAML) → fallback. Env wins because rotating session lifetimes during
// an incident shouldn't require a config-file deploy — SOC 2 control
// language often demands "ability to shorten session validity" as an
// operator runbook step.
//
// The env value is parsed as a positive integer number of hours; a
// non-positive value or unparseable string falls through to the config
// (warned in logs so the operator notices the typo).
func resolveSessionTTL(cfgRaw string, fallback time.Duration) time.Duration {
	if raw := os.Getenv("BAMBOO_SESSION_TTL_HOURS"); raw != "" {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			slog.Warn("session ttl: BAMBOO_SESSION_TTL_HOURS unparseable; falling back to config",
				"value", raw, "err", err)
		} else if n <= 0 {
			slog.Warn("session ttl: BAMBOO_SESSION_TTL_HOURS must be positive; falling back to config",
				"value", raw)
		} else {
			return time.Duration(n) * time.Hour
		}
	}
	return parseTTL(cfgRaw, fallback)
}

// Run blocks until ctx is canceled or either listener errors. On shutdown
// signal it performs a graceful gRPC stop and HTTP shutdown.
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.cfg.Server.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Server.GRPCAddr, err)
	}

	grpcErr := make(chan error, 1)
	httpErr := make(chan error, 1)

	go func() {
		slog.Info("gRPC server listening", "addr", s.cfg.Server.GRPCAddr)
		grpcErr <- s.grpc.Serve(listener)
	}()
	go func() {
		httpErr <- s.http.Run(ctx)
	}()

	select {
	case <-ctx.Done():
		slog.Info("controller shutting down", "reason", ctx.Err())
		s.grpc.GracefulStop()
		<-httpErr
		return nil
	case err := <-grpcErr:
		return fmt.Errorf("gRPC serve: %w", err)
	case err := <-httpErr:
		s.grpc.GracefulStop()
		return fmt.Errorf("HTTP serve: %w", err)
	}
}
