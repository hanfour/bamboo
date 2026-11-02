// SPDX-License-Identifier: AGPL-3.0-or-later

// Command bamboo-ingress serves public services
// (docs/design/2026-11-02-public-services-ingress.md §5): Caddy terminates
// TLS and forwards here; requests are routed by Host to a peer's mesh IP.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanfour/bamboo/apps/ingress/internal/metrics"
	"github.com/hanfour/bamboo/apps/ingress/internal/proxy"
	"github.com/hanfour/bamboo/apps/ingress/internal/routes"
	"github.com/hanfour/bamboo/apps/ingress/internal/telemetry"
)

// Version is set with -ldflags "-X main.Version=...".
var Version = "dev"

const (
	cleanupEvery    = time.Minute
	limiterIdle     = 5 * time.Minute
	shutdownTimeout = 10 * time.Second
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, cfg, log); err != nil {
		log.Error("bamboo-ingress stopped", "err", err)
		os.Exit(1)
	}
}

// run serves until ctx ends or a listener fails. Shutdown order: stop
// accepting and drain requests first, then flush telemetry, so records
// from draining requests are still delivered.
func run(ctx context.Context, cfg config, log *slog.Logger) error {
	if len(cfg.TrustedProxies) == 0 {
		log.Warn("BAMBOO_INGRESS_TRUSTED_PROXIES is unset: X-Forwarded-For is ignored and every client is its socket address")
	}
	m := metrics.New(Version)
	table := routes.NewTable(cfg.Suffix)
	poller := routes.NewPoller(routes.PollerOptions{ControllerURL: cfg.ControllerURL, TokenPath: cfg.TokenFile, Table: table, Log: log, OnResult: m.OnRefresh})
	sink := telemetry.NewSink(telemetry.SinkOptions{ControllerURL: cfg.ControllerURL, TokenPath: cfg.TokenFile, Log: log,
		OnDrop: func(n int) { m.TelemetryDropped.Add(float64(n)) }})
	handler := proxy.New(proxy.Options{Table: table, Limits: proxy.DefaultLimits(), Recorder: m.Recorder(sink), TrustedProxies: cfg.TrustedProxies})

	sinkCtx, stopSink := context.WithCancel(context.Background())
	sinkDone := make(chan struct{})
	go func() { sink.Run(sinkCtx); close(sinkDone) }()
	go poller.Run(ctx)
	go runEvery(ctx, cleanupEvery, func() { handler.CleanupLimiters(limiterIdle) })

	public := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10}
	admin := &http.Server{Addr: cfg.AdminListen, Handler: adminMux(m), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 2)
	go func() { errc <- serve(public) }()
	go func() { errc <- serve(admin) }()
	log.Info("bamboo-ingress listening", "addr", cfg.Listen, "admin", cfg.AdminListen, "suffix", cfg.Suffix, "version", Version)

	var err error
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	shutdown(log, public, admin)
	stopSink()
	<-sinkDone
	return err
}

func serve(s *http.Server) error {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// shutdown drains each server for up to shutdownTimeout, then closes it
// (long-lived streams would otherwise hold Shutdown open).
func shutdown(log *slog.Logger, servers ...*http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, s := range servers {
		if err := s.Shutdown(ctx); err != nil {
			log.Warn("forced close after drain timeout", "addr", s.Addr, "err", err)
			_ = s.Close()
		}
	}
}

func adminMux(m *metrics.Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": Version, "service": "bamboo-ingress"})
	})
	mux.Handle("/metrics", m.Handler())
	return mux
}

// runEvery calls fn every interval until ctx ends; the ticker is stopped
// on return.
func runEvery(ctx context.Context, every time.Duration, fn func()) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}
