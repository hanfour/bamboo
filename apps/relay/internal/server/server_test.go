// SPDX-License-Identifier: AGPL-3.0-or-later

package server_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hanfour/bamboo/apps/relay/internal/server"
)

// TestRelay_ForwardsBetweenTwoClients drives a real HTTP+WS server,
// connects two fake clients with distinct WG pubkeys, sends a PACKET
// from A to B, and asserts B's read loop receives it. This exercises
// the full session lifecycle end to end.
func TestRelay_ForwardsBetweenTwoClients(t *testing.T) {
	srv := server.New(nil)
	ts := httptest.NewServer(http.HandlerFunc(srv.HandleRelay))
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/relay"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Two clients with distinct pubkeys.
	var keyA, keyB [server.PubKeyLen]byte
	for i := range keyA {
		keyA[i] = byte(i)
		keyB[i] = byte(0xFF - i)
	}

	connA := dial(t, ctx, wsURL, keyA)
	defer connA.Close(websocket.StatusNormalClosure, "test done")
	connB := dial(t, ctx, wsURL, keyB)
	defer connB.Close(websocket.StatusNormalClosure, "test done")

	// A -> B: PACKET frame.
	body := []byte("hello-from-A")
	frame, err := server.Encode(server.EncodePacket(keyB, body))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := connA.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("A write: %v", err)
	}

	// B should receive a PACKET frame whose dst key is sender (A).
	_, raw, err := connB.Read(ctx)
	if err != nil {
		t.Fatalf("B read: %v", err)
	}
	got, err := server.Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Type != server.TypePacket {
		t.Fatalf("got type 0x%02x, want PACKET", got.Type)
	}
	pkt, err := server.ParsePacket(got.Payload)
	if err != nil {
		t.Fatalf("ParsePacket: %v", err)
	}
	if pkt.DstKey != keyA {
		t.Errorf("forwarded packet sender = %x; want A %x", pkt.DstKey, keyA)
	}
	if !bytes.Equal(pkt.Body, body) {
		t.Errorf("body = %q, want %q", pkt.Body, body)
	}
}

// TestRelay_PeerGoneWhenDestUnknown asserts a PACKET frame to an
// unconnected peer returns PEER_GONE to the sender.
func TestRelay_PeerGoneWhenDestUnknown(t *testing.T) {
	srv := server.New(nil)
	ts := httptest.NewServer(http.HandlerFunc(srv.HandleRelay))
	defer ts.Close()

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/relay"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var keyA, keyMissing [server.PubKeyLen]byte
	for i := range keyA {
		keyA[i] = 0xA0 | byte(i)
		keyMissing[i] = byte(i)
	}

	conn := dial(t, ctx, wsURL, keyA)
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	frame, _ := server.Encode(server.EncodePacket(keyMissing, []byte("ignored")))
	if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got, err := server.Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Type != server.TypePeerGone {
		t.Errorf("got type 0x%02x, want PEER_GONE", got.Type)
	}
}

// dial opens a websocket to the relay, sends CLIENT_HELLO, waits for
// SERVER_HELLO, and returns the live connection ready for app frames.
func dial(t *testing.T, ctx context.Context, url string, key [server.PubKeyLen]byte) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Client speaks first. CLIENT_HELLO carries our pubkey + auth.
	helloPayload := append([]byte(nil), key[:]...)
	helloPayload = append(helloPayload, []byte("dev-token")...)
	frame, _ := server.Encode(server.Frame{Type: server.TypeClientHello, Payload: helloPayload})
	if err := c.Write(ctx, websocket.MessageBinary, frame); err != nil {
		c.Close(websocket.StatusInternalError, "")
		t.Fatalf("write client_hello: %v", err)
	}
	// Wait for SERVER_HELLO — synchronous signal that the server has
	// us in its routing map.
	_, hello, err := c.Read(ctx)
	if err != nil {
		c.Close(websocket.StatusInternalError, "")
		t.Fatalf("read server_hello: %v", err)
	}
	got, err := server.Decode(hello)
	if err != nil || got.Type != server.TypeServerHello {
		c.Close(websocket.StatusInternalError, "")
		t.Fatalf("expected server_hello, got %v err=%v", got, err)
	}
	return c
}
