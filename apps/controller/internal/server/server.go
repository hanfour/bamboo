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
	"google.golang.org/grpc"
)

// Server holds the gRPC server and dependencies.
type Server struct {
	cfg  *config.Config
	grpc *grpc.Server
}

// New constructs a Server. It does not start any listeners; call Run.
func New(cfg *config.Config) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	return &Server{
		cfg:  cfg,
		grpc: grpc.NewServer(),
	}, nil
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
