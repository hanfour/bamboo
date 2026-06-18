// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hanfour/bamboo/clients/core/wg"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// encodeRelayFrame mirrors apps/relay/server.Encode: [4-byte BE total len
// (incl. itself)][1-byte type][payload]. Used by the fake relay below.
func encodeRelayFrame(typ byte, payload []byte) []byte {
	total := 4 + 1 + len(payload)
	buf := make([]byte, total)
	binary.BigEndian.PutUint32(buf[0:4], uint32(total))
	buf[4] = typ
	copy(buf[5:], payload)
	return buf
}

// TestOpenRelayAtURL_DeliversToActualWGPort is the regression for the
// hardware-E2E bug: openRelayAtURL hardcoded the relay client's WG-delivery
// address to "127.0.0.1:51820" instead of THIS peer's actual wg listen port,
// so a CLI peer only ever received relay-forwarded traffic when its (usually
// random) wg port happened to be 51820. The fix threads wgPort through; this
// test stands up a fake relay that forwards a PACKET to the just-opened client
// and asserts the body lands on a mock WG socket bound to the wgPort we passed
// (an ephemeral port that is essentially never 51820).
func TestOpenRelayAtURL_DeliversToActualWGPort(t *testing.T) {
	// Mock WireGuard socket on an ephemeral port — this is the wgPort the CLI
	// would pass; the relay client must deliver inbound bodies here.
	wgRecv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("wg listen: %v", err)
	}
	defer wgRecv.Close()
	wgPort := uint16(wgRecv.LocalAddr().(*net.UDPAddr).Port)

	// The remote peer whose packets the fake relay will forward to us. Its
	// pubkey is both what openRelayAtURL AddPeer-registers (so readLoop has a
	// per-peer socket for it) and the PACKET source key the fake relay sends.
	peerPriv, err := wg.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("gen peer key: %v", err)
	}
	peerPub := peerPriv.PublicKey()
	body := []byte("relayed-wg-bytes")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/relay-token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"dev-token"}`))
			return
		}
		// /relay — the WS endpoint (normalizeRelayURL appends /relay).
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		if _, _, err := conn.Read(ctx); err != nil { // CLIENT_HELLO
			return
		}
		if err := conn.Write(ctx, websocket.MessageBinary, encodeRelayFrame(0x01, []byte{0x00})); err != nil { // SERVER_HELLO
			return
		}
		// Forward a PACKET "from" peerPub repeatedly until the WS closes, so
		// the client's AddPeer (done inside openRelayAtURL) is in place before
		// one lands.
		pkt := encodeRelayFrame(0x03, append(append([]byte{}, peerPub[:]...), body...))
		for {
			if err := conn.Write(ctx, websocket.MessageBinary, pkt); err != nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}))
	defer srv.Close()
	t.Setenv("BAMBOO_CONTROLLER_HTTP_URL", srv.URL)
	relayURL := "ws" + srv.URL[len("http"):] // http(s)://host → ws(s)://host

	priv, err := wg.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("gen self key: %v", err)
	}
	peers := []*bamboov1.Peer{{
		Id:                 "peer-b",
		WireguardPublicKey: peerPub.Base64(),
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, _, err := openRelayAtURL(ctx, relayURL, "self-id", peers, priv, &peerSession{}, wgPort)
	if err != nil {
		t.Fatalf("openRelayAtURL: %v", err)
	}
	defer c.Close()

	_ = wgRecv.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1024)
	n, _, err := wgRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("mock WG never received the relayed packet (wgPort threading broken?): %v", err)
	}
	if string(buf[:n]) != string(body) {
		t.Errorf("mock WG got %q, want %q", buf[:n], body)
	}
}
