// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanfour/bamboo/apps/controller/internal/clickhouse"
	"github.com/hanfour/bamboo/apps/controller/internal/config"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/server"
	"github.com/hanfour/bamboo/apps/controller/internal/wgsync"
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
	serveCmd.Flags().StringVarP(&serveConfigPath, "config", "c", "", "path to YAML config file (optional; defaults to env-var-only when empty)")
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

	// ClickHouse is best-effort. Connect failures log and proceed in
	// degraded mode (telemetry writes drop with a single warning).
	var ch *clickhouse.Conn
	if cfg.ClickHouse.URL != "" {
		var chErr error
		ch, chErr = clickhouse.Open(ctx, cfg.ClickHouse.URL)
		if chErr != nil {
			slog.Warn("clickhouse open failed; running in degraded mode", "err", chErr)
			ch = nil
		} else {
			defer func() { _ = ch.Close() }()
			slog.Info("clickhouse connected")
		}
	}

	srv, err := server.New(cfg, pool, ch)
	if err != nil {
		return err
	}

	// Spawn the wg-state reporter when configured. On a single-VPS
	// deploy infra/full/bootstrap.sh installs a host systemd timer
	// that writes `wg show $IFACE dump` to StatePath; the reporter
	// reconciles each peer's status into the DB so the UI's online
	// / last-seen columns reflect actual cryptokey-routing state
	// rather than a once-set 'online' that never decays. Empty
	// StatePath disables the reporter for dev deploys.
	if cfg.WGSync.StatePath != "" {
		interval := parseDurationWithWarn("BAMBOO_WG_SYNC_INTERVAL", cfg.WGSync.Interval)
		win := parseDurationWithWarn("BAMBOO_WG_ONLINE_WINDOW", cfg.WGSync.OnlineWindow)
		reporter := wgsync.New(wgsync.Config{
			Peers:        repo.NewPeers(pool),
			StatePath:    cfg.WGSync.StatePath,
			Interval:     interval,
			OnlineWindow: win,
		})
		go func() {
			if err := reporter.Run(ctx); err != nil {
				slog.Warn("wgsync reporter exited", "err", err)
			}
		}()
	}

	slog.Info("controller starting",
		"version", Version,
		"grpc_addr", cfg.Server.GRPCAddr,
		"http_addr", cfg.Server.HTTPAddr,
	)
	return srv.Run(ctx)
}

// parseDurationWithWarn returns the parsed duration, or zero (so the
// downstream caller falls back to its own default) when raw is empty
// or unparseable. Logs a warn for unparseable input so operators
// don't get a silent fall-through.
func parseDurationWithWarn(envName, raw string) time.Duration {
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("wgsync: ignoring unparseable duration; using default",
			"env", envName, "value", raw, "err", err)
		return 0
	}
	return d
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
