// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server wires the controller's gRPC and HTTP listeners and
// handles graceful shutdown.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/clickhouse"
	"github.com/hanfour/bamboo/apps/controller/internal/config"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/events"
	"github.com/hanfour/bamboo/apps/controller/internal/handlers"
	"github.com/hanfour/bamboo/apps/controller/internal/mail"
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
	ttl := parseTTL(cfg.Auth.SessionTTL, 24*time.Hour)
	secret := []byte(cfg.Auth.SessionSecret)

	grpcSrv, authHandler, coordHandler := buildGRPCWithAuth(pool, ch, cfg.Auth.RequireAuth, secret)
	authHandler.SetOIDCConfig(cfg.Auth.OIDC.BaseURL, secret, ttl)

	httpSrv := NewHTTPServer(cfg.Server.HTTPAddr, pool, providers, ch, secret, cfg.Auth.OIDC.BaseURL, ttl, coordHandler)
	httpSrv.SetRequireAuth(cfg.Auth.RequireAuth)
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
func buildGRPCWithAuth(pool *db.Pool, ch *clickhouse.Conn, requireAuth bool, sessionSec []byte) (*grpc.Server, *handlers.AuthHandler, *handlers.CoordinatorHandler) {
	bus := events.NewBus()
	s := grpc.NewServer(
		grpc.UnaryInterceptor(requireAuthUnaryInterceptor(requireAuth, sessionSec)),
		grpc.StreamInterceptor(requireAuthStreamInterceptor(requireAuth, sessionSec)),
	)

	authHandler := handlers.NewAuthHandler(pool)
	coordHandler := handlers.NewCoordinatorHandler(pool, authHandler, bus)
	bamboov1.RegisterAuthServiceServer(s, authHandler)
	bamboov1.RegisterCoordinatorServiceServer(s, coordHandler)
	bamboov1.RegisterPolicyServiceServer(s, handlers.NewPolicyHandler(pool, ch, bus, authHandler))
	bamboov1.RegisterTelemetryServiceServer(s, handlers.NewTelemetryHandler(pool, ch))
	reflection.Register(s)

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
	bamboov1.RegisterTelemetryServiceServer(s, handlers.NewTelemetryHandler(pool, nil))

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
