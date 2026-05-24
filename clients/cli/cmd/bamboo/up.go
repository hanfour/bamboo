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
	"net"
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
	// Validate --advertise-routes before any network work — a typo
	// in a CIDR should fail with a clear local error, not after the
	// controller round trip rejects it.
	if err := validateAdvertisedRoutes(flagAdvertiseRoutes); err != nil {
		return err
	}

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

	// Decide the WireGuard listen port BEFORE STUN discovery so the
	// STUN binding socket and the eventual WG socket share the same
	// 5-tuple. NATs allocate external mappings keyed on the internal
	// (src_ip, src_port); using two different internal source ports
	// for STUN and WG produces an advertised endpoint that other
	// peers can't actually dial. See Finding #4 in
	// docs/development/project-understanding-2026-05-13.md.
	//
	// pickFreeUDPPort briefly binds + closes a UDP socket to learn a
	// free ephemeral port. There is a small race window between the
	// bind/close pair and WireGuard taking the same port, but in
	// practice nothing else opens UDP on this host between those
	// microseconds, and WG's Apply will return a clear error if it
	// loses the race rather than silently mis-binding.
	wgPort := flagWGListenPort
	if wgPort == 0 {
		wgPort, err = pickFreeUDPPort()
		if err != nil {
			return fmt.Errorf("pick wg listen port: %w", err)
		}
	}
	slog.Info("wireguard listen port chosen", "port", wgPort)

	// session holds the controller-issued peer-bound bearer for the
	// CLI process lifetime. Populated on every Register; consumed by
	// the relay-token mint and the authedAdapter that wraps heartbeat
	// / watch outgoing metadata.
	session := &peerSession{}
	resp, err := registerWithController(ctx, priv, session, wgPort)
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
	// Override the kernel-default listen port with the one we picked
	// pre-Register; this is what the STUN-discovered endpoint already
	// names externally, so the NAT mapping will line up.
	cfg.ListenPort = wgPort

	// Optional: open a relay-server session as a *fallback* path.
	// Triggered by BAMBOO_RELAY_URL env var. The relay is opened
	// proactively so per-peer proxy ports exist; the monitor below
	// swaps individual peers from direct to relay when their direct
	// endpoint stops handshaking. Peers with no direct endpoint at
	// all start on relay immediately.
	relayClient, relayProxies, err := maybeOpenRelay(cmd.Context(), resp, priv, session)
	if err != nil {
		slog.Warn("relay setup failed; continuing without relay", "err", err)
	}
	if relayClient != nil {
		defer func() { _ = relayClient.Close() }()
		for i := range cfg.Peers {
			if cfg.Peers[i].Endpoint == "" {
				if r, ok := relayProxies[cfg.Peers[i].PublicKey.Base64()]; ok {
					cfg.Peers[i].Endpoint = r
				}
			}
		}
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
	cache.SetPolicyRevision(resp.GetPolicyRevision())
	// Wrap the gRPC adapter so the WatchPeers call context carries
	// the current peer-session bearer as outgoing metadata. The
	// controller's interceptor whitelist ignores it today; the
	// prod-mode gate follow-up flips that. Heartbeat moved to REST
	// (see restHeartbeater) so the §4 P2 bandwidth-sample side-
	// channel fires — gRPC's HeartbeatRequest doesn't model the
	// byte counters today.
	adapter := newAuthedAdapter(clientsync.AdaptClient(cli.Coordinator), session)
	heartbeater := newRESTHeartbeater(session, flagTenant)

	refresh := func(refreshCtx context.Context) (*bamboov1.RegisterResponse, error) {
		return registerWithController(refreshCtx, priv, session, wgPort)
	}
	discover := func() []string { return discoverEndpoints(wgPort) }
	// Bytes reporter aggregates per-peer wg counters into one cumulative
	// pair the controller writes as a connection_events bandwidth_sample
	// row. Failures (stats unavailable, device not yet brought up) yield
	// (0,0) which the controller treats as "no data" and skips the write.
	reportBytes := func() (uint64, uint64) {
		stats, err := dev.Stats()
		if err != nil {
			slog.Debug("wg stats read for bandwidth report failed", "err", err)
			return 0, 0
		}
		return device.SumBytes(stats)
	}

	daemonCtx, daemonCancel := context.WithCancel(cmd.Context())
	defer daemonCancel()
	go clientsync.RunHeartbeat(daemonCtx, heartbeater, resp.GetSelf().GetId(), discover, reportBytes)
	go clientsync.RunWatchPeers(daemonCtx, adapter, dev, priv, cache, resp.GetSelf().GetId(), refresh)
	if relayClient != nil {
		reapply := &deviceReapplier{dev: dev, base: cfg}
		go clientsync.RunRelayFallback(daemonCtx, dev, reapply, relayProxies, time.Now())
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	slog.Info("tearing down tunnel")
	return nil
}

// registerWithController posts a Register call over REST carrying
// either the pre-auth-key credential (if --auth-key is set) or the
// dev tenant-slug fallback. STUN discovery binds the same UDP port
// WireGuard will listen on so the advertised endpoint is one other
// peers can actually dial through this host's NAT.
//
// We use REST instead of the gRPC Register stub so the response
// carries the peer-session bearer token. Heartbeat / WatchPeers
// remain on gRPC (those don't need the proto regen this token
// addition would require).
//
// On success the session pointer is updated with the new bearer.
// When the controller returns no token (older deployment without
// PR A), session.Token() stays whatever it was, and the slug-only
// fallback path keeps working for relay-token mint.
func registerWithController(ctx context.Context, priv wg.PrivateKey, session *peerSession, wgPort uint16) (*bamboov1.RegisterResponse, error) {
	resp, token, exp, err := restRegister(
		ctx,
		flagHostname,
		priv.PublicKey().Base64(),
		runtime.GOOS,
		Version,
		flagAuthKey,
		flagTenant,
		discoverEndpoints(wgPort),
		flagAdvertiseRoutes,
		flagAdvertiseExitNode,
	)
	if err != nil {
		return nil, err
	}
	if token != "" {
		session.set(token, exp)
		slog.Info("peer session token issued", "expires_at", exp.Format(time.RFC3339))
	}
	return resp, nil
}

// discoverEndpoints returns the STUN-observed public host:port for
// this peer, or nil if discovery fails. We deliberately swallow
// errors here: failure to discover an endpoint should not block
// tunnel bring-up. Callers that want stricter behavior can look at
// the log.
//
// The bind uses 0.0.0.0:<wgPort> so the STUN socket and the
// eventual WireGuard socket share the same internal source port.
// Most NAT types map an external port keyed on the internal source
// port — discovering with a different port would yield an endpoint
// other peers can't actually dial. See Finding #4.
func discoverEndpoints(wgPort uint16) []string {
	local := fmt.Sprintf("0.0.0.0:%d", wgPort)
	addr, err := stun.DiscoverFrom(local, stun.DefaultServer, 2*time.Second)
	if err != nil {
		slog.Warn("stun discovery failed; tunnel will rely on peer-initiated handshake",
			"server", stun.DefaultServer, "wg_port", wgPort, "err", err)
		return nil
	}
	slog.Info("stun discovered endpoint", "endpoint", addr.String(), "wg_port", wgPort)
	return []string{addr.String()}
}

// pickFreeUDPPort opens an ephemeral UDP socket, reads back the port
// the OS assigned, then closes it. The returned port is what the
// caller will hand to STUN (via DiscoverFrom) and to WireGuard (via
// DeviceConfig.ListenPort). The brief close/reopen window is a
// race; in practice nothing else is racing for UDP ports on a host
// running this CLI and WG will fail loudly if it does lose the
// race (rather than silently picking a different port).
func pickFreeUDPPort() (uint16, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close() }()
	port := conn.LocalAddr().(*net.UDPAddr).Port
	if port <= 0 || port > 0xFFFF {
		return 0, fmt.Errorf("invalid picked port %d", port)
	}
	return uint16(port), nil
}

// maybeOpenRelay opens a relay client when BAMBOO_RELAY_URL is set
// and pre-registers every peer so per-peer proxy ports exist. It
// does NOT rewrite peer endpoints — the caller decides whether to
// use direct or relay endpoints initially. The fallback monitor
// (RunRelayFallback) is what swaps individual peers from direct to
// relay on handshake failure.
//
// Returns (nil, nil, nil) when BAMBOO_RELAY_URL is unset.
func maybeOpenRelay(ctx context.Context, resp *bamboov1.RegisterResponse, priv wg.PrivateKey, session *peerSession) (*relay.Client, clientsync.PeerRelayMap, error) {
	relayURL := os.Getenv("BAMBOO_RELAY_URL")
	if relayURL == "" {
		return nil, nil, nil
	}

	token, err := mintRelayToken(ctx, resp.GetSelf().GetId(), priv.PublicKey().Base64(), session)
	if err != nil {
		return nil, nil, fmt.Errorf("mint relay token: %w", err)
	}
	selfKey, err := decodePubKey(priv.PublicKey().Base64())
	if err != nil {
		return nil, nil, err
	}

	c, err := relay.Dial(ctx, relayURL, selfKey, token, "127.0.0.1:51820")
	if err != nil {
		return nil, nil, fmt.Errorf("relay dial: %w", err)
	}

	proxies := make(clientsync.PeerRelayMap, len(resp.GetPeers()))
	for _, p := range resp.GetPeers() {
		peerKey, err := decodePubKey(p.GetWireguardPublicKey())
		if err != nil {
			return nil, nil, fmt.Errorf("peer %s pubkey: %w", p.GetId(), err)
		}
		proxyAddr, err := c.AddPeer(peerKey)
		if err != nil {
			return nil, nil, fmt.Errorf("relay add peer %s: %w", p.GetId(), err)
		}
		proxies[p.GetWireguardPublicKey()] = proxyAddr
	}
	slog.Info("relay opened", "url", relayURL, "peers", len(proxies))
	return c, proxies, nil
}

// deviceReapplier wraps the bamboo CLI's device + base config so the
// generic relay-fallback monitor can re-apply with endpoint overrides.
type deviceReapplier struct {
	dev  device.Device
	base *wg.DeviceConfig
}

func (r *deviceReapplier) Reapply(ctx context.Context, overrides map[string]string) error {
	out := clientsync.ApplyEndpointOverrides(r.base, overrides)
	return r.dev.Apply(ctx, out)
}

// mintRelayToken calls the controller's POST /api/v1/relay-token
// endpoint and returns the JWT. Uses the controller HTTP base URL
// from BAMBOO_CONTROLLER_HTTP_URL (default localhost:8081 in dev).
//
// Credential precedence:
//
//  1. Authorization: Bearer <peer-session-token> — the controller-
//     issued peer-bound bearer captured at Register time. This is
//     the path the prod-mode gate follow-up will require.
//  2. X-Tenant-Slug header — legacy dev fallback. Kept so this
//     command still works against an older controller that does not
//     yet issue peer session tokens.
//
// Both headers may be sent simultaneously; the controller resolves
// tenant from the bearer when valid and falls through to the slug
// otherwise.
func mintRelayToken(ctx context.Context, peerID, wgPubKey string, session *peerSession) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"peerId":             peerID,
		"wireguardPublicKey": wgPubKey,
	})
	u, err := url.JoinPath(controllerHTTPBase(), "/api/v1/relay-token")
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := session.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("X-Tenant-Slug", flagTenant)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
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
