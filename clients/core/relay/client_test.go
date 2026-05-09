// SPDX-License-Identifier: Apache-2.0

package relay_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanfour/bamboo/apps/relay/server"
	"github.com/hanfour/bamboo/clients/core/relay"
)

// TestClient_RoundTripsThroughRelay drives a real in-process relay
// (the same server type the production binary uses) plus two relay
// Clients. Sends a payload from peerA, asserts peerB's per-peer
// socket delivers it to a mock WG-listening UDP socket on the
// loopback.
func TestClient_RoundTripsThroughRelay(t *testing.T) {
	relaySrv := server.New(server.Options{AllowNoAuth: true})
	ts := httptest.NewServer(http.HandlerFunc(relaySrv.HandleRelay))
	defer ts.Close()
	relayURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/relay"

	var keyA, keyB [relay.PubKeyLen]byte
	for i := range keyA {
		keyA[i] = byte(0xA0 ^ i)
		keyB[i] = byte(0xB0 ^ i)
	}

	// Mock WireGuard listener — the per-peer socket on B will
	// deliver inbound packets here once it receives them from the
	// relay.
	wgRecv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("wg listen: %v", err)
	}
	defer wgRecv.Close()
	wgAddr := wgRecv.LocalAddr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientA, err := relay.Dial(ctx, relayURL, keyA, "dev-token", wgAddr)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	defer clientA.Close()

	clientB, err := relay.Dial(ctx, relayURL, keyB, "dev-token", wgAddr)
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	defer clientB.Close()

	// On B, register A as a known peer; B's per-A socket is what
	// will write inbound packets to wgRecv.
	if _, err := clientB.AddPeer(keyA); err != nil {
		t.Fatalf("B.AddPeer: %v", err)
	}

	// On A, register B; the returned address is what A's "WG"
	// would set as B's Endpoint.
	bProxyAddr, err := clientA.AddPeer(keyB)
	if err != nil {
		t.Fatalf("A.AddPeer: %v", err)
	}

	// Send a synthetic outbound packet from "A's WG" to A's per-B
	// proxy socket. This should land at wgRecv via the relay.
	body := []byte("hello-via-relay")
	src, err := net.DialUDP("udp", nil, mustResolve(t, bProxyAddr))
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer src.Close()
	if _, err := src.Write(body); err != nil {
		t.Fatalf("write to proxy: %v", err)
	}

	_ = wgRecv.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1024)
	n, _, err := wgRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("wg recv: %v", err)
	}
	if string(buf[:n]) != string(body) {
		t.Errorf("got %q, want %q", buf[:n], body)
	}
}

func mustResolve(t *testing.T, addr string) *net.UDPAddr {
	t.Helper()
	r, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatalf("resolve %q: %v", addr, err)
	}
	return r
}
