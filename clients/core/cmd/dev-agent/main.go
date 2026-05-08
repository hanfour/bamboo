// SPDX-License-Identifier: AGPL-3.0-or-later

// Command dev-agent is a tiny development binary used to validate the
// gRPC contract between client and controller. It is not the production
// agent; that lives in clients/cli (and later in the platform-specific
// directories).
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/hanfour/bamboo/clients/core/internal/client"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/metadata"
)

func main() {
	addr := flag.String("addr", "localhost:8080", "controller gRPC address")
	hostname := flag.String("hostname", defaultHostname(), "peer hostname to register")
	tenantSlug := flag.String("tenant", "default", "tenant slug (sent as x-tenant-slug metadata)")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cli, err := client.Dial(ctx, *addr)
	if err != nil {
		fail("dial: %v", err)
	}
	defer func() { _ = cli.Close() }()
	slog.Info("connected to controller", "addr", *addr)

	pubKey := randomPubKey()

	ctx = metadata.AppendToOutgoingContext(ctx, "x-tenant-slug", *tenantSlug)

	resp, err := cli.Coordinator.Register(ctx, &bamboov1.RegisterRequest{
		Hostname:           *hostname,
		WireguardPublicKey: pubKey,
		Os:                 runtime.GOOS,
		ClientVersion:      "dev",
	})
	if err != nil {
		fail("register: %v", err)
	}

	self := resp.GetSelf()
	fmt.Printf("registered: peer_id=%s ip=%s tenant=%s peers_in_set=%d\n",
		self.GetId(), self.GetIp(), self.GetTenantId(), len(resp.GetPeers()))
	for _, p := range resp.GetPeers() {
		fmt.Printf("  peer: %s @ %s (%s)\n", p.GetHostname(), p.GetIp(), p.GetId())
	}
}

func randomPubKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		fail("random: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func defaultHostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "dev-agent"
	}
	return h
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
