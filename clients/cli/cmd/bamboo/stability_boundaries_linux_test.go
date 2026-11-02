// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/hanfour/bamboo/clients/core/wg"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"golang.zx2c4.com/wireguard/wgctrl"
)

func stabilityBoundaries(t *testing.T, owner *deviceReapplier, priv wg.PrivateKey, original *wg.DeviceConfig, server stabilityHello) {
	t.Helper()
	ctx := context.Background()
	cfg := cloneDesired(original)
	u, err := url.Parse(os.Getenv("BAMBOO_KERNEL_SERVER"))
	if err != nil {
		t.Fatal(err)
	}
	reachable := net.JoinHostPort(u.Hostname(), "51820")
	cfg.Peers[0].Endpoint = reachable
	cfg.Peers[0].PersistentKeepalive = 25 * time.Second
	if err := owner.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	stabilityAssertKernel(t, reachable, 25*time.Second)
	cfg.Peers[0].PersistentKeepalive = 0
	if err := owner.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	stabilityAssertKernel(t, reachable, 0)
	cfg.Peers[0].AllowedIPs = nil
	if err := owner.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := stabilityPing("100.90.0.2:9900"); err == nil {
		t.Fatal("withdrawn AllowedIPs still carries packets")
	}
	cfg.Peers[0].AllowedIPs = append([]netip.Prefix(nil), original.Peers[0].AllowedIPs...)
	if err := owner.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := stabilityPing("100.90.0.2:9900"); err != nil {
		t.Fatal(err)
	}
	relay := stabilityRelayBoundaries(t, owner, cfg, server, reachable)
	stabilityAuthorityBoundaries(t, owner, cfg, priv, server, reachable, relay)
	stabilityKeyBoundaries(t, owner, cfg, original, reachable)
}

func stabilityRelayBoundaries(t *testing.T, owner *deviceReapplier, cfg *wg.DeviceConfig, server stabilityHello, reachable string) string {
	t.Helper()
	ctx := context.Background()
	relay := stabilityProxy(t, reachable)
	if err := owner.Reapply(ctx, map[string]string{server.PublicKey: relay}); err != nil {
		t.Fatal(err)
	}
	stabilityAssertKernel(t, relay, 0)
	before := stabilityStats(t, owner.dev)
	if err := owner.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	after := stabilityStats(t, owner.dev)
	if after.Endpoint != relay || after.LastHandshakeTime.Before(before.LastHandshakeTime) || after.RxBytes < before.RxBytes {
		t.Fatal("unchanged relay rebuilt peer")
	}
	if err := stabilityPing("100.90.0.2:9900"); err != nil {
		t.Fatalf("explicit relay echo: %v", err)
	}
	old := owner.bound()
	newRelay := stabilityProxy(t, reachable)
	if err := owner.replaceRelay(ctx, map[string]string{server.PublicKey: newRelay}); err != nil {
		t.Fatal(err)
	}
	if err := old.Reapply(ctx, map[string]string{server.PublicKey: relay}); err != nil {
		t.Fatal(err)
	}
	stabilityAssertKernel(t, newRelay, 0)
	if err := stabilityPing("100.90.0.2:9900"); err != nil {
		t.Fatalf("new generation relay echo: %v", err)
	}
	t.Log("kernel stale generation preserved new proxy endpoint and echo")
	return relay
}

func stabilityAuthorityBoundaries(t *testing.T, owner *deviceReapplier, cfg *wg.DeviceConfig, priv wg.PrivateKey, server stabilityHello, reachable, relay string) {
	t.Helper()
	ctx := context.Background()
	stabilityAssertRemoved(t, owner, &wg.DeviceConfig{PrivateKey: priv, Address: cfg.Address}, server.PublicKey, relay)
	denied, err := wg.BuildDeviceConfig(priv, &bamboov1.RegisterResponse{
		Self:  stabilityPeer("client", "100.90.0.1", priv.PublicKey().Base64(), ""),
		Peers: []*bamboov1.Peer{stabilityPeer("server", "100.90.0.2", server.PublicKey, reachable)}, PolicyRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(denied.Peers) != 0 {
		t.Fatal("enforcing ACL did not exclude denied peer")
	}
	if err := owner.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	stabilityAssertRemoved(t, owner, denied, server.PublicKey, relay)
}

func stabilityKeyBoundaries(t *testing.T, owner *deviceReapplier, cfg, original *wg.DeviceConfig, reachable string) {
	t.Helper()
	ctx := context.Background()
	replacement := stabilityKey(t)
	pub := replacement.PublicKey()
	cfg.Peers[0].PublicKey = pub
	if err := owner.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	stats := stabilityStats(t, owner.dev)
	if stats.PublicKey != pub.Base64() || stats.Endpoint != reachable {
		t.Fatal("replacement key not seeded")
	}
	restored := original.Peers[0]
	restored.Endpoint = reachable
	cfg.Peers = append(cfg.Peers, restored)
	cfg.Peers[0].AllowedIPs = nil
	stabilityTrace(t, "before append reachable server peer")
	if err := owner.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	stabilityTrace(t, "after append reachable server peer")
	rows, err := owner.dev.Stats()
	if err != nil || len(rows) != 2 {
		t.Fatalf("new peer membership: %v %v", rows, err)
	}
	cfg.Peers = []wg.PeerConfig{restored}
	cfg.Peers[0].Endpoint = reachable
	stabilityTrace(t, "before remove replacement peer")
	if err := owner.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	stabilityTrace(t, "after remove replacement peer")
	if err := stabilityPing("100.90.0.2:9900"); err != nil {
		t.Fatalf("restored key echo: %v", err)
	}
	t.Log("kernel boundaries passed: endpoint change, keepalive zero, AllowedIPs withdrawal, relay echo, removal/ACL plus relay, new/replacement key membership; reachable restore echo")
}

func stabilityAssertRemoved(t *testing.T, owner *deviceReapplier, cfg *wg.DeviceConfig, key, relay string) {
	t.Helper()
	if err := owner.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := owner.Reapply(context.Background(), map[string]string{key: relay}); err != nil {
		t.Fatal(err)
	}
	stats, err := owner.dev.Stats()
	if err != nil || len(stats) != 0 {
		t.Fatalf("removed/ACL peer resurrected: %v %v", stats, err)
	}
	if err := stabilityPing("100.90.0.2:9900"); err == nil {
		t.Fatal("removed/ACL peer still passes packets")
	}
}

func stabilityAssertKernel(t *testing.T, endpoint string, keepalive time.Duration) {
	t.Helper()
	c, err := wgctrl.New()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	d, err := c.Device("stability0")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Peers) != 1 || d.Peers[0].Endpoint.String() != endpoint || d.Peers[0].PersistentKeepaliveInterval != keepalive {
		t.Fatalf("kernel endpoint/keepalive mismatch: %v", d.Peers)
	}
}

func stabilityProxy(t *testing.T, endpoint string) string {
	t.Helper()
	dst, err := net.ResolveUDPAddr("udp4", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	local, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := net.DialUDP("udp4", nil, dst)
	if err != nil {
		local.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { local.Close(); remote.Close() })
	clients := make(chan *net.UDPAddr, 1)
	go stabilityProxySend(local, remote, clients)
	go func() {
		var client *net.UDPAddr
		b := make([]byte, 65535)
		for {
			n, err := remote.Read(b)
			if err != nil {
				return
			}
			if client == nil {
				client = <-clients
			}
			if _, err = local.WriteToUDP(b[:n], client); err != nil {
				return
			}
		}
	}()
	return local.LocalAddr().String()
}

func stabilityProxySend(local, remote *net.UDPConn, clients chan<- *net.UDPAddr) {
	b := make([]byte, 65535)
	for {
		n, addr, err := local.ReadFromUDP(b)
		if err != nil {
			return
		}
		select {
		case clients <- addr:
		default:
		}
		if _, err = remote.Write(b[:n]); err != nil {
			return
		}
	}
}

func stabilityTrace(t *testing.T, label string) {
	t.Helper()
	c, err := wgctrl.New()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	d, err := c.Device("stability0")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s timestamp=%s", label, time.Now().UTC().Format(time.RFC3339Nano))
	for _, p := range d.Peers {
		t.Logf("peer=%s endpoint=%v handshake=%s rx=%d tx=%d keepalive=%s allowed=%v", p.PublicKey.String(), p.Endpoint, p.LastHandshakeTime.UTC().Format(time.RFC3339Nano), p.ReceiveBytes, p.TransmitBytes, p.PersistentKeepaliveInterval, p.AllowedIPs)
	}
}
