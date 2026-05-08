// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server wires the controller's gRPC and HTTP listeners and
// handles graceful shutdown.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/hanfour/bamboo/apps/controller/internal/config"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/handlers"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// Server holds the gRPC server and dependencies.
type Server struct {
	cfg  *config.Config
	pool *db.Pool
	grpc *grpc.Server
}

// New constructs a Server with all gRPC services registered.
// It does not start any listeners; call Run.
func New(cfg *config.Config, pool *db.Pool) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	if pool == nil {
		return nil, fmt.Errorf("nil pool")
	}

	grpcServer := grpc.NewServer()
	registerHandlers(grpcServer, pool)

	// gRPC reflection lets tools like grpcurl introspect the server.
	// Useful in dev; consider gating on an env flag for production.
	reflection.Register(grpcServer)

	return &Server{
		cfg:  cfg,
		pool: pool,
		grpc: grpcServer,
	}, nil
}

// registerHandlers wires every gRPC service. New services should be added here.
func registerHandlers(s *grpc.Server, pool *db.Pool) {
	bamboov1.RegisterAuthServiceServer(s, handlers.NewAuthHandler())
	bamboov1.RegisterCoordinatorServiceServer(s, handlers.NewCoordinatorHandler(pool))
	bamboov1.RegisterPolicyServiceServer(s, handlers.NewPolicyHandler())
	bamboov1.RegisterTelemetryServiceServer(s, handlers.NewTelemetryHandler())
}

// Run blocks until ctx is canceled or the listener errors. On shutdown
// signal it performs a graceful gRPC stop.
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.cfg.Server.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Server.GRPCAddr, err)
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("gRPC server listening", "addr", s.cfg.Server.GRPCAddr)
		errCh <- s.grpc.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		slog.Info("controller shutting down", "reason", ctx.Err())
		s.grpc.GracefulStop()
		return nil
	case err := <-errCh:
		return fmt.Errorf("gRPC serve: %w", err)
	}
}
