// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/hanfour/bamboo/clients/cli/internal/state"
	clientsync "github.com/hanfour/bamboo/clients/cli/internal/sync"
	"github.com/hanfour/bamboo/clients/core/client"
	"github.com/hanfour/bamboo/clients/core/device"
	"github.com/hanfour/bamboo/clients/core/relay"
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

	// Optional: route every peer through a relay server. Triggered
	// by BAMBOO_RELAY_URL env var. Useful for testing on networks
	// where direct STUN-discovered endpoints don't work (symmetric
	// NAT, restrictive egress firewalls).
	relayClient, err := maybeStartRelay(cmd.Context(), resp, priv, cfg)
	if err != nil {
		slog.Warn("relay setup failed; continuing without relay", "err", err)
	}
	if relayClient != nil {
		defer relayClient.Close()
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

// maybeStartRelay opens a relay client when BAMBOO_RELAY_URL is set,
// rewrites every peer's Endpoint in cfg to point at a localhost
// proxy port, and returns the client (caller closes on exit). When
// the env var is empty this returns (nil, nil) — the regular STUN
// path remains in effect.
func maybeStartRelay(ctx context.Context, resp *bamboov1.RegisterResponse, priv wg.PrivateKey, cfg *wg.DeviceConfig) (*relay.Client, error) {
	relayURL := os.Getenv("BAMBOO_RELAY_URL")
	if relayURL == "" {
		return nil, nil
	}

	// Mint a relay token from the controller.
	token, err := mintRelayToken(ctx, resp.GetSelf().GetId(), priv.PublicKey().Base64())
	if err != nil {
		return nil, fmt.Errorf("mint relay token: %w", err)
	}

	selfKey, err := decodePubKey(priv.PublicKey().Base64())
	if err != nil {
		return nil, err
	}

	// WG listens on flagListenPort (or kernel-picked port if zero).
	// Phase 2: assume default 51820 if cfg.ListenPort is zero. The
	// netlink device.go path uses 51820 as default too.
	wgPort := int(cfg.ListenPort)
	if wgPort == 0 {
		wgPort = 51820
	}

	c, err := relay.Dial(ctx, relayURL, selfKey, token, fmt.Sprintf("127.0.0.1:%d", wgPort))
	if err != nil {
		return nil, fmt.Errorf("relay dial: %w", err)
	}

	// Rewrite each peer's Endpoint to point at a localhost proxy
	// socket on the relay client. WG will send packets there; the
	// relay client wraps and forwards via WSS.
	for i := range cfg.Peers {
		peerKey, err := decodePubKey(cfg.Peers[i].PublicKey.Base64())
		if err != nil {
			return nil, fmt.Errorf("peer %d pubkey: %w", i, err)
		}
		proxyAddr, err := c.AddPeer(peerKey)
		if err != nil {
			return nil, fmt.Errorf("relay add peer: %w", err)
		}
		cfg.Peers[i].Endpoint = proxyAddr
	}
	slog.Info("relay enabled", "url", relayURL, "peers", len(cfg.Peers))
	return c, nil
}

// mintRelayToken calls the controller's POST /api/v1/relay-token
// endpoint and returns the JWT. Uses the controller HTTP base URL
// from BAMBOO_CONTROLLER_HTTP_URL (default localhost:8081 in dev).
func mintRelayToken(ctx context.Context, peerID, wgPubKey string) (string, error) {
	base := os.Getenv("BAMBOO_CONTROLLER_HTTP_URL")
	if base == "" {
		base = "http://localhost:8081"
	}
	body, _ := json.Marshal(map[string]string{
		"peerId":             peerID,
		"wireguardPublicKey": wgPubKey,
	})
	u, err := url.JoinPath(base, "/api/v1/relay-token")
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Slug", flagTenant)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("relay-token http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Token, nil
}

func decodePubKey(b64 string) ([relay.PubKeyLen]byte, error) {
	var out [relay.PubKeyLen]byte
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return out, err
	}
	if len(raw) != relay.PubKeyLen {
		return out, fmt.Errorf("pubkey wrong size: %d", len(raw))
	}
	copy(out[:], raw)
	return out, nil
}
