// SPDX-License-Identifier: AGPL-3.0-or-later

// Command dev-agent is a tiny development binary used to validate the
// gRPC contract between client and controller. It is not the production
// agent; that lives in clients/cli (and later in the platform-specific
// directories).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/hanfour/bamboo/clients/core/internal/client"
	"github.com/hanfour/bamboo/clients/core/internal/device"
	"github.com/hanfour/bamboo/clients/core/internal/wg"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/metadata"
)

func main() {
	addr := flag.String("addr", "localhost:8080", "controller gRPC address")
	hostname := flag.String("hostname", defaultHostname(), "peer hostname to register")
	tenantSlug := flag.String("tenant", "default", "tenant slug (sent as x-tenant-slug metadata; ignored if --auth-key is set)")
	authKey := flag.String("auth-key", "", "pre-auth key secret (bka_..._...); when set, --tenant is ignored")
	showConfig := flag.Bool("show-config", false, "print the wg-quick config that would be applied and exit")
	bringUp := flag.Bool("up", false, "bring up a real WireGuard interface after register (Linux only; root required)")
	iface := flag.String("iface", "bamboo0", "interface name to use with --up")
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

	priv, err := wg.GeneratePrivateKey()
	if err != nil {
		fail("generate key: %v", err)
	}

	req := &bamboov1.RegisterRequest{
		Hostname:           *hostname,
		WireguardPublicKey: priv.PublicKey().Base64(),
		Os:                 runtime.GOOS,
		ClientVersion:      "dev",
	}

	if *authKey != "" {
		req.Credential = &bamboov1.RegisterRequest_PreAuthKeySecret{
			PreAuthKeySecret: *authKey,
		}
		slog.Info("authenticating with pre-auth key")
	} else {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-tenant-slug", *tenantSlug)
		slog.Info("authenticating via tenant-slug fallback", "tenant", *tenantSlug)
	}

	resp, err := cli.Coordinator.Register(ctx, req)
	if err != nil {
		fail("register: %v", err)
	}

	self := resp.GetSelf()
	fmt.Printf("registered: peer_id=%s ip=%s tenant=%s peers_in_set=%d\n",
		self.GetId(), self.GetIp(), self.GetTenantId(), len(resp.GetPeers()))
	for _, p := range resp.GetPeers() {
		fmt.Printf("  peer: %s @ %s (%s)\n", p.GetHostname(), p.GetIp(), p.GetId())
	}

	cfg, err := wg.BuildDeviceConfig(priv, resp)
	if err != nil {
		fail("build wireguard config: %v", err)
	}

	if *showConfig {
		fmt.Println("\n# --- wg-quick config (would be applied as root) ---")
		fmt.Println(cfg.WGQuick())
	}

	if *bringUp {
		runTunnel(*iface, cfg)
	}
}

// runTunnel applies cfg to a real WireGuard interface and blocks until
// SIGINT/SIGTERM, at which point it tears down. Linux + root only; on
// other platforms it surfaces ErrUnsupported and exits.
func runTunnel(iface string, cfg *wg.DeviceConfig) {
	dev, err := device.New(device.Options{InterfaceName: iface})
	if err != nil {
		if errors.Is(err, device.ErrUnsupported) {
			fail("--up requires Linux; use the platform app on macOS / Windows")
		}
		fail("device: %v", err)
	}
	defer func() {
		if err := dev.Close(); err != nil {
			slog.Warn("device close", "err", err)
		}
	}()

	if err := dev.Apply(context.Background(), cfg); err != nil {
		fail("apply: %v", err)
	}
	slog.Info("tunnel up", "iface", iface, "address", cfg.Address.String())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	slog.Info("tearing down tunnel")
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
