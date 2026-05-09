// SPDX-License-Identifier: AGPL-3.0-or-later

package server_test

import (
	"bytes"
	"testing"

	"github.com/hanfour/bamboo/apps/relay/server"
)

func TestFrame_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		typ  byte
		body []byte
	}{
		{"hello", server.TypeServerHello, []byte{0x01}},
		{"keepalive", server.TypeKeepalive, nil},
		{"packet32", server.TypePacket, bytes.Repeat([]byte{0xAB}, 32)},
		{"packet1500", server.TypePacket, bytes.Repeat([]byte{0xCD}, 1500)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wire, err := server.Encode(server.Frame{Type: c.typ, Payload: c.body})
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := server.Decode(wire)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Type != c.typ {
				t.Errorf("Type=%x, want %x", got.Type, c.typ)
			}
			if !bytes.Equal(got.Payload, c.body) {
				t.Errorf("Payload mismatch: got %x want %x", got.Payload, c.body)
			}
		})
	}
}

func TestFrame_RejectsTooLarge(t *testing.T) {
	huge := bytes.Repeat([]byte{0xFF}, server.MaxFrameSize+1)
	if _, err := server.Encode(server.Frame{Type: server.TypePacket, Payload: huge}); err == nil {
		t.Fatal("expected error for oversized frame")
	}
}

func TestParsePacket_RoundTrip(t *testing.T) {
	var key [server.PubKeyLen]byte
	for i := range key {
		key[i] = byte(i)
	}
	body := []byte("hello")
	wireFrame := server.EncodePacket(key, body)

	pkt, err := server.ParsePacket(wireFrame.Payload)
	if err != nil {
		t.Fatalf("ParsePacket: %v", err)
	}
	if pkt.DstKey != key {
		t.Errorf("DstKey mismatch: got %x want %x", pkt.DstKey, key)
	}
	if !bytes.Equal(pkt.Body, body) {
		t.Errorf("Body mismatch: got %q want %q", pkt.Body, body)
	}
}

func TestParseClientHello(t *testing.T) {
	var key [server.PubKeyLen]byte
	for i := range key {
		key[i] = byte(0x80 ^ i)
	}
	payload := append([]byte(nil), key[:]...)
	payload = append(payload, []byte("token-data")...)

	ch, err := server.ParseClientHello(payload)
	if err != nil {
		t.Fatalf("ParseClientHello: %v", err)
	}
	if ch.SrcKey != key {
		t.Errorf("SrcKey mismatch")
	}
	if ch.AuthToken != "token-data" {
		t.Errorf("AuthToken=%q, want token-data", ch.AuthToken)
	}
}

func TestParseClientHello_Truncated(t *testing.T) {
	if _, err := server.ParseClientHello([]byte{0x01, 0x02}); err == nil {
		t.Fatal("expected error on truncated payload")
	}
}
