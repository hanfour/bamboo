// SPDX-License-Identifier: AGPL-3.0-or-later

// Command relay is the bamboo DERP-style relay server.
//
// Skeleton implementation — see ADR 0013. v0 omits JWT auth (any
// CLIENT_HELLO is accepted, single tenant) so the focus stays on the
// frame protocol + routing. JWT auth + tenant isolation land in the
// next PR alongside the client-side relay integration.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanfour/bamboo/apps/relay/internal/server"
)

func main() {
	addr := flag.String("addr", ":8443", "TLS listen address")
	certFile := flag.String("cert", "", "TLS certificate file (use empty for unencrypted dev mode on -addr)")
	keyFile := flag.String("key", "", "TLS private key file")
	devPlaintext := flag.Bool("dev-plaintext", false, "run on plaintext HTTP (dev only; never in production)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	srv := server.New(logger)
	mux := http.NewServeMux()
	mux.HandleFunc("/relay", srv.HandleRelay)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if !*devPlaintext {
		httpSrv.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		<-ctx.Done()
		shutdownCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	slog.Info("relay listening", "addr", *addr, "tls", !*devPlaintext)

	var err error
	if *devPlaintext {
		err = httpSrv.ListenAndServe()
	} else {
		if *certFile == "" || *keyFile == "" {
			slog.Error("cert and key are required without --dev-plaintext")
			os.Exit(2)
		}
		err = httpSrv.ListenAndServeTLS(*certFile, *keyFile)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("relay listen failed", "err", err)
		os.Exit(1)
	}
}
