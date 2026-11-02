// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	peersync "github.com/hanfour/bamboo/clients/cli/internal/sync"
	"github.com/hanfour/bamboo/clients/core/device"
	"github.com/hanfour/bamboo/clients/core/wg"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// restartHandshakeBudget bounds how long the restarted host may take
// to reach the peer: CandidateProbeTimeout on the dead LAN candidate,
// one RotationInterval tick, one WireGuard retransmit (5s + jitter).
const restartHandshakeBudget = 30 * time.Second

// restartQuiet lets WireGuard's passive keepalive (10s) acknowledge the
// last echo before the restart. Otherwise the Mac holds unanswered data
// and its own 15s new-handshake timer would re-initiate, which is the
// help this test rules out.
const restartQuiet = 12 * time.Second

// TestKernelRestartMac plays the NAT'd Mac: it trusts the VPS key but
// has no endpoint for it and no keepalive, so it can never initiate.
// Any handshake must come from the VPS reaching one of its candidates.
func TestKernelRestartMac(t *testing.T) {
	stabilityRole(t, "server")
	d, priv := stabilityDevice(t), stabilityKey(t)
	var echoOnce sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/public", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(stabilityHello{PublicKey: priv.PublicKey().Base64()})
	})
	mux.HandleFunc("/trust", func(w http.ResponseWriter, r *http.Request) {
		var hello stabilityHello
		if err := json.NewDecoder(r.Body).Decode(&hello); err != nil {
			http.Error(w, "invalid trust request", 400)
			return
		}
		pub, err := wg.PublicKeyFromBase64(hello.PublicKey)
		if err != nil {
			http.Error(w, "invalid public key", 400)
			return
		}
		cfg := &wg.DeviceConfig{PrivateKey: priv, Address: netip.MustParsePrefix("100.90.0.1/32"), ListenPort: 51820,
			Peers: []wg.PeerConfig{{PublicKey: pub, AllowedIPs: []netip.Prefix{netip.MustParsePrefix("100.90.0.2/32")}}}}
		if err := d.Apply(r.Context(), cfg); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		echoOnce.Do(func() { stabilityEcho(t, "100.90.0.1:9900") })
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	t.Log("isolated kernel restart peer ready")
	if err := server.ListenAndServe(); err != nil {
		t.Fatal(err)
	}
}

// TestKernelRestartVPS plays the public host whose bamboo restarts.
// The Mac advertises [off-subnet private, dead same-subnet, reachable].
// Each bring-up must skip the first, time out on the second and reach
// the third, with no help from the Mac.
func TestKernelRestartVPS(t *testing.T) {
	stabilityRole(t, "client")
	var mac stabilityHello
	stabilityHTTP(t, "/public", nil, &mac)
	priv := stabilityKey(t)
	stabilityHTTP(t, "/trust", &stabilityHello{PublicKey: priv.PublicKey().Base64()}, nil)
	cands := restartCandidates(t)
	for _, phase := range []string{"first start", "after restart"} {
		elapsed := restartBringUp(t, priv, mac.PublicKey, cands)
		t.Logf("%s: handshake via %s after %s", phase, cands[2], elapsed.Round(100*time.Millisecond))
		if elapsed < peersync.CandidateProbeTimeout {
			t.Fatalf("%s: handshake before rotation; the Mac initiated", phase)
		}
	}
}

func restartCandidates(t *testing.T) []string {
	t.Helper()
	u, err := url.Parse(os.Getenv("BAMBOO_KERNEL_SERVER"))
	if err != nil {
		t.Fatal(err)
	}
	macIP := netip.MustParseAddr(u.Hostname())
	octets := macIP.As4()
	octets[3] = 250
	dead := netip.AddrFrom4(octets)
	return []string{
		"10.255.255.1:51820",
		net.JoinHostPort(dead.String(), "51820"),
		net.JoinHostPort(macIP.String(), "51820"),
	}
}

func restartBringUp(t *testing.T, priv wg.PrivateKey, macKey string, cands []string) time.Duration {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := device.New(device.Options{InterfaceName: "stability0"})
	if err != nil {
		t.Fatal(err)
	}
	rotating := make(chan struct{})
	defer func() {
		cancel()
		<-rotating
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	}()
	self := stabilityPeer("vps", "100.90.0.2", priv.PublicKey().Base64(), "")
	macPeer := &bamboov1.Peer{Id: "mac", Ip: "100.90.0.1", WireguardPublicKey: macKey, Endpoints: cands}
	cfg, err := peersync.New(self, []*bamboov1.Peer{macPeer}).BuildDeviceConfig(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ListenPort = 51820
	owner := &deviceReapplier{dev: d, rotator: peersync.NewCandidateRotator(wg.InterfacePrefixes, time.Now)}
	start := time.Now()
	if err := owner.Apply(ctx, cfg); err != nil {
		close(rotating)
		t.Fatal(err)
	}
	go func() {
		defer close(rotating)
		peersync.RunEndpointRotation(ctx, owner.rotate)
	}()
	if first := stabilityStats(t, d).Endpoint; first != cands[1] {
		t.Fatalf("first endpoint %s, want same-subnet %s (off-subnet private skipped)", first, cands[1])
	}
	elapsed := restartAwaitHandshake(t, d, start)
	if got := stabilityStats(t, d).Endpoint; got != cands[2] {
		t.Fatalf("handshake endpoint %s, want %s", got, cands[2])
	}
	if err := stabilityPing("100.90.0.1:9900"); err != nil {
		t.Fatalf("mesh echo after handshake: %v", err)
	}
	time.Sleep(restartQuiet)
	return elapsed
}

func restartAwaitHandshake(t *testing.T, d device.Device, start time.Time) time.Duration {
	t.Helper()
	for time.Since(start) < restartHandshakeBudget {
		if s := stabilityStats(t, d); !s.LastHandshakeTime.IsZero() {
			return time.Since(start)
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("no handshake within %s; peer=%+v", restartHandshakeBudget, stabilityStats(t, d))
	return 0
}
