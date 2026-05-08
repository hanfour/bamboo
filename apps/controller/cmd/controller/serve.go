// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hanfour/bamboo/apps/controller/internal/config"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/server"
	"github.com/spf13/cobra"
)

var (
	serveConfigPath string
	serveLogJSON    bool
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the control plane server",
	RunE:  runServe,
}

func init() {
	serveCmd.Flags().StringVarP(&serveConfigPath, "config", "c", "config/example.yaml", "path to YAML config file")
	serveCmd.Flags().BoolVar(&serveLogJSON, "log-json", false, "emit JSON-formatted logs (default: text)")
}

func runServe(_ *cobra.Command, _ []string) error {
	configureLogger(serveLogJSON)

	cfg, err := config.Load(serveConfigPath)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := db.Open(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer pool.Close()
	slog.Info("postgres connected")

	srv, err := server.New(cfg, pool)
	if err != nil {
		return err
	}

	slog.Info("controller starting",
		"version", Version,
		"grpc_addr", cfg.Server.GRPCAddr,
		"http_addr", cfg.Server.HTTPAddr,
	)
	return srv.Run(ctx)
}

func configureLogger(jsonOutput bool) {
	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if jsonOutput {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}
