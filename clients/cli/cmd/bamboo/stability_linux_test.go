// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"

	"os"
	"sync"
	"testing"
	"time"

	peersync "github.com/hanfour/bamboo/clients/cli/internal/sync"
	"github.com/hanfour/bamboo/clients/core/device"
	"github.com/hanfour/bamboo/clients/core/wg"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

type stabilityHello struct {
	PublicKey string
	Endpoint  string
}

func stabilityRole(t *testing.T, role string) {
	t.Helper()
	if os.Getenv("BAMBOO_KERNEL_ROLE") != role {
		t.Skip("isolated Docker harness only")
	}
}

func stabilityDevice(t *testing.T) device.Device {
	t.Helper()
	d, err := device.New(device.Options{InterfaceName: "stability0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d
}

func stabilityKey(t *testing.T) wg.PrivateKey {
	t.Helper()
	k, err := wg.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func stabilityPeer(id, ip, key, endpoint string) *bamboov1.Peer {
	return &bamboov1.Peer{Id: id, Ip: ip, WireguardPublicKey: key, Endpoints: []string{endpoint}}
}

func stabilityEcho(t *testing.T, address string) {
	t.Helper()
	c, err := net.ListenPacket("udp4", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	go func() {
		b := make([]byte, 256)
		for {
			n, addr, err := c.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = c.WriteTo(b[:n], addr)
		}
	}()
}

func stabilityPing(address string) error {
	c, err := net.DialTimeout("udp4", address, time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		return err
	}
	payload := []byte("bamboo-kernel-stability")
	if _, err := c.Write(payload); err != nil {
		return err
	}
	b := make([]byte, 256)
	n, err := c.Read(b)
	if err != nil {
		return err
	}
	if !bytes.Equal(b[:n], payload) {
		return fmt.Errorf("mesh echo payload mismatch")
	}
	return nil
}

func TestKernelStabilityServer(t *testing.T) {
	stabilityRole(t, "server")
	d, priv := stabilityDevice(t), stabilityKey(t)
	mux := http.NewServeMux()
	var echoOnce sync.Once
	mux.HandleFunc("/public", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(stabilityHello{PublicKey: priv.PublicKey().Base64()})
	})
	mux.HandleFunc("/connect", func(w http.ResponseWriter, r *http.Request) {
		var hello stabilityHello
		if err := json.NewDecoder(r.Body).Decode(&hello); err != nil {
			http.Error(w, "invalid public handshake", 400)
			return
		}
		pub, err := wg.PublicKeyFromBase64(hello.PublicKey)
		if err != nil {
			http.Error(w, "invalid public key", 400)
			return
		}
		cfg := &wg.DeviceConfig{PrivateKey: priv, Address: netip.MustParsePrefix("100.90.0.2/32"), ListenPort: 51820,
			Peers: []wg.PeerConfig{{PublicKey: pub, Endpoint: hello.Endpoint, AllowedIPs: []netip.Prefix{netip.MustParsePrefix("100.90.0.1/32")}}}}
		if err := d.Apply(r.Context(), cfg); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		echoOnce.Do(func() { stabilityEcho(t, "100.90.0.2:9900") })
		if err := stabilityPing("100.90.0.1:9900"); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	t.Log("isolated kernel server ready")
	if err := server.ListenAndServe(); err != nil {
		t.Fatal(err)
	}
}

func stabilityHTTP(t *testing.T, path string, input, output any) {
	t.Helper()
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			t.Fatal(err)
		}
	}
	cli := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodPost, os.Getenv("BAMBOO_KERNEL_SERVER")+path, &body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("control %s: %s %s", path, resp.Status, b)
	}
	if output != nil {
		if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
}

type stabilityStream struct {
	ctx    context.Context
	events chan *bamboov1.WatchPeersEvent
}

func (s *stabilityStream) Recv() (*bamboov1.WatchPeersEvent, error) {
	select {
	case e := <-s.events:
		return e, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}
func (s *stabilityStream) WatchPeers(context.Context, *bamboov1.WatchPeersRequest) (peersync.WatchStream, error) {
	return s, nil
}

type stabilityObservation struct {
	stats []device.PeerStats
	err   error
}
type stabilityApplier struct {
	device.Device
	target  peersync.Applier
	applied chan stabilityObservation
}

func (a *stabilityApplier) Apply(ctx context.Context, cfg *wg.DeviceConfig) error {
	err := a.target.Apply(ctx, cfg)
	result := stabilityObservation{err: err}
	if err == nil {
		result.stats, result.err = a.Stats()
	}
	a.applied <- result
	return err
}

func TestKernelStabilityWatchPreservesRoaming(t *testing.T) {
	stabilityRole(t, "client")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var server stabilityHello
	stabilityHTTP(t, "/public", nil, &server)
	d, priv := stabilityDevice(t), stabilityKey(t)
	self := stabilityPeer("client", "100.90.0.1", priv.PublicKey().Base64(), "")
	remote := stabilityPeer("server", "100.90.0.2", server.PublicKey, "192.0.2.1:51820")
	cache := peersync.New(self, []*bamboov1.Peer{remote})
	cfg, err := cache.BuildDeviceConfig(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ListenPort = 51820
	owner := &deviceReapplier{dev: d}
	if err := owner.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	stabilityEcho(t, "100.90.0.1:9900")
	stabilityHTTP(t, "/connect", &stabilityHello{PublicKey: priv.PublicKey().Base64(), Endpoint: os.Getenv("BAMBOO_KERNEL_CLIENT_ENDPOINT")}, nil)
	if err := stabilityPing("100.90.0.2:9900"); err != nil {
		t.Fatalf("before update echo: %v", err)
	}
	before := stabilityStats(t, d)
	if before.Endpoint == remote.Endpoints[0] || before.LastHandshakeTime.IsZero() || before.RxBytes == 0 {
		t.Fatal("precondition: remote initiation did not establish a learned endpoint and handshake")
	}
	stream := &stabilityStream{ctx: ctx, events: make(chan *bamboov1.WatchPeersEvent, 1)}
	applier := &stabilityApplier{Device: d, target: owner, applied: make(chan stabilityObservation, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		peersync.RunWatchPeers(ctx, stream, applier, priv, cache, "client", nil, nil, nil)
	}()
	defer func() { cancel(); <-done }()
	stream.events <- &bamboov1.WatchPeersEvent{Event: &bamboov1.WatchPeersEvent_PeerUpdated{PeerUpdated: &bamboov1.PeerUpdated{Peer: stabilityPeer("server", "100.90.0.2", server.PublicKey, remote.Endpoints[0])}}}
	var observed stabilityObservation
	select {
	case observed = <-applier.applied:
	case <-ctx.Done():
		t.Fatal("watch update did not apply")
	}
	stabilityAssertPreserved(t, before, observed)
	if err := stabilityPing("100.90.0.2:9900"); err != nil {
		t.Errorf("after update mesh echo: %v", err)
	}
	stabilityBoundaries(t, owner, priv, cfg, server)
}

func stabilityAssertPreserved(t *testing.T, before device.PeerStats, observed stabilityObservation) {
	t.Helper()
	if observed.err != nil {
		t.Fatal(observed.err)
	}
	if len(observed.stats) != 1 {
		t.Fatalf("after watch update peer count = %d", len(observed.stats))
	}
	after := observed.stats[0]
	t.Logf("before endpoint=%s handshake=%s rx=%d tx=%d", before.Endpoint, before.LastHandshakeTime.Format(time.RFC3339Nano), before.RxBytes, before.TxBytes)
	t.Logf("after endpoint=%s handshake=%s rx=%d tx=%d", after.Endpoint, after.LastHandshakeTime.Format(time.RFC3339Nano), after.RxBytes, after.TxBytes)
	if after.Endpoint != before.Endpoint {
		t.Errorf("unchanged desired endpoint overwrote learned endpoint: got %s want %s", after.Endpoint, before.Endpoint)
	}
	if after.LastHandshakeTime.IsZero() || after.LastHandshakeTime.Before(before.LastHandshakeTime) {
		t.Errorf("unchanged peer handshake reset: got %s want %s", after.LastHandshakeTime, before.LastHandshakeTime)
	}
	if after.RxBytes < before.RxBytes || after.TxBytes < before.TxBytes {
		t.Error("unchanged peer counters reset")
	}
}

func stabilityStats(t *testing.T, d device.Device) device.PeerStats {
	t.Helper()
	stats, err := d.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected one peer, got %d", len(stats))
	}
	return stats[0]
}
