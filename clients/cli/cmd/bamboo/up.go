// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/hanfour/bamboo/clients/cli/internal/state"
	clientsync "github.com/hanfour/bamboo/clients/cli/internal/sync"
	"github.com/hanfour/bamboo/clients/core/client"
	"github.com/hanfour/bamboo/clients/core/device"
	"github.com/hanfour/bamboo/clients/core/stun"
	"github.com/hanfour/bamboo/clients/core/wg"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/metadata"
)

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Register with the controller and bring up the WireGuard tunnel",
	Long: `Register with the controller, generate (or reuse) a stable Curve25519
key pair, build the WireGuard configuration, and bring up the local
interface. The command runs in the foreground until interrupted with
Ctrl-C, at which point the interface is removed.`,
	RunE: runUp,
}

func runUp(cmd *cobra.Command, _ []string) error {
	priv, err := state.LoadOrCreatePrivateKey()
	if err != nil {
		return fmt.Errorf("load private key: %w", err)
	}
	slog.Info("identity loaded", "pubkey", priv.PublicKey().Base64())

	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()

	cli, err := client.Dial(ctx, flagAddr)
	if err != nil {
		return fmt.Errorf("dial controller: %w", err)
	}
	defer func() { _ = cli.Close() }()

	resp, err := registerWithController(ctx, cli, priv)
	if err != nil {
		return err
	}
	slog.Info("registered",
		"peer_id", resp.GetSelf().GetId(),
		"ip", resp.GetSelf().GetIp(),
		"peers_in_set", len(resp.GetPeers()),
	)

	cfg, err := wg.BuildDeviceConfig(priv, resp)
	if err != nil {
		return fmt.Errorf("build wireguard config: %w", err)
	}

	dev, err := device.New(device.Options{InterfaceName: flagIface})
	if err != nil {
		if errors.Is(err, device.ErrUnsupported) {
			return fmt.Errorf("bamboo up requires Linux with root; on macOS / iOS use the app under clients/apple/")
		}
		return fmt.Errorf("device: %w", err)
	}
	defer func() {
		if err := dev.Close(); err != nil {
			slog.Warn("device close", "err", err)
		}
	}()

	if err := dev.Apply(cmd.Context(), cfg); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	slog.Info("tunnel up", "iface", flagIface, "address", cfg.Address.String())

	// Daemon mode: keep the cache in sync with the controller while the
	// process runs. WatchPeers reconnects with backoff if the stream
	// breaks; Heartbeat keeps the controller's "online" status fresh
	// and is the canonical liveness signal for our own peer row.
	cache := clientsync.New(resp.GetSelf(), resp.GetPeers())
	adapter := clientsync.AdaptClient(cli.Coordinator)

	daemonCtx, daemonCancel := context.WithCancel(cmd.Context())
	defer daemonCancel()
	go clientsync.RunHeartbeat(daemonCtx, adapter, resp.GetSelf().GetId(), discoverEndpoints)
	go clientsync.RunWatchPeers(daemonCtx, adapter, dev, priv, cache, resp.GetSelf().GetId())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	slog.Info("tearing down tunnel")
	return nil
}

// registerWithController posts a Register call carrying either the
// pre-auth-key credential (if --auth-key is set) or the dev tenant-slug
// metadata fallback. STUN-discovered endpoints are included so other
// peers in the tenant can dial this peer directly.
func registerWithController(ctx context.Context, cli *client.Client, priv wg.PrivateKey) (*bamboov1.RegisterResponse, error) {
	req := &bamboov1.RegisterRequest{
		Hostname:           flagHostname,
		WireguardPublicKey: priv.PublicKey().Base64(),
		Os:                 runtime.GOOS,
		ClientVersion:      Version,
		Endpoints:          discoverEndpoints(),
	}
	if flagAuthKey != "" {
		req.Credential = &bamboov1.RegisterRequest_PreAuthKeySecret{
			PreAuthKeySecret: flagAuthKey,
		}
	} else {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-tenant-slug", flagTenant)
	}
	return cli.Coordinator.Register(ctx, req)
}

// discoverEndpoints returns the STUN-observed public host:port for
// this peer, or nil if discovery fails. We deliberately swallow errors
// here: failure to discover an endpoint should not block tunnel
// bring-up. Callers that want stricter behavior can look at the log.
func discoverEndpoints() []string {
	addr, err := stun.Discover(stun.DefaultServer, 2*time.Second)
	if err != nil {
		slog.Warn("stun discovery failed; tunnel will rely on peer-initiated handshake",
			"server", stun.DefaultServer, "err", err)
		return nil
	}
	slog.Info("stun discovered endpoint", "endpoint", addr.String())
	return []string{addr.String()}
}
