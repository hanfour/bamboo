// SPDX-License-Identifier: AGPL-3.0-or-later

// Command relay is the bamboo DERP-style relay server.
//
// See ADR 0013 for the protocol. JWT-authenticated production mode is
// the default; --dev-no-auth + --dev-plaintext are escape hatches for
// local development.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanfour/bamboo/apps/relay/server"
)

// Version is set at build time via -ldflags "-X main.Version=...".
// It is reported by /version so deployment tooling can verify which
// build is running on a given relay.
var Version = "dev"

func main() {
	addr := flag.String("addr", ":8443", "TLS listen address")
	certFile := flag.String("cert", "", "TLS certificate file (use empty for unencrypted dev mode on -addr)")
	keyFile := flag.String("key", "", "TLS private key file")
	devPlaintext := flag.Bool("dev-plaintext", false, "run on plaintext HTTP (dev only; never in production)")
	devNoAuth := flag.Bool("dev-no-auth", false, "accept any CLIENT_HELLO without verifying the JWT (dev only)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	secret := []byte(os.Getenv("BAMBOO_RELAY_SHARED_SECRET"))
	if !*devNoAuth && len(secret) == 0 {
		slog.Error("BAMBOO_RELAY_SHARED_SECRET is required (or pass --dev-no-auth)")
		os.Exit(2)
	}

	srv := server.New(server.Options{
		Log:          logger,
		SharedSecret: secret,
		AllowNoAuth:  *devNoAuth,
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/relay", srv.HandleRelay)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"version": Version,
			"service": "bamboo-relay",
		})
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
