// SPDX-License-Identifier: Apache-2.0

package stun

import (
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"
)

// TestDiscover_LocalServer brings up a tiny in-process STUN responder
// that emits a XOR-MAPPED-ADDRESS pointing at a fixed IPv4 address +
// the source port of the binding request, and verifies Discover()
// parses it correctly.
func TestDiscover_LocalServer(t *testing.T) {
	server, _ := newFakeServer(t, netip.MustParseAddr("203.0.113.42"))

	got, err := Discover(server, 2*time.Second)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got.Addr().String() != "203.0.113.42" {
		t.Errorf("addr = %s, want 203.0.113.42", got.Addr())
	}
	if got.Port() == 0 {
		t.Errorf("port = 0, want non-zero source port")
	}
}

// TestDiscover_BadCookieRejected verifies Discover refuses a response
// whose magic cookie does not match.
func TestDiscover_BadCookieRejected(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	go func() {
		buf := make([]byte, 1024)
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		// Echo the txid back but corrupt the magic cookie.
		if n < 20 {
			return
		}
		response := make([]byte, 20)
		binary.BigEndian.PutUint16(response[0:2], 0x0101)
		binary.BigEndian.PutUint32(response[4:8], 0xDEADBEEF) // wrong cookie
		copy(response[8:20], buf[8:20])
		_, _ = conn.WriteToUDP(response, raddr)
	}()

	_, err = Discover(conn.LocalAddr().String(), 2*time.Second)
	if err == nil {
		t.Fatal("expected error from bad magic cookie")
	}
}

// newFakeServer launches an in-process UDP listener that responds to
// any STUN binding request with a XOR-MAPPED-ADDRESS pointing at the
// given IPv4 address and the request's source port. Returns the
// host:port to pass to Discover.
func newFakeServer(t *testing.T, claimed netip.Addr) (string, *net.UDPConn) {
	t.Helper()
	if !claimed.Is4() {
		t.Fatalf("fake server only supports IPv4 in this test")
	}

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	go func() {
		buf := make([]byte, 1024)
		for {
			n, raddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 20 {
				continue
			}
			txid := make([]byte, 12)
			copy(txid, buf[8:20])

			// Build XOR-MAPPED-ADDRESS attribute.
			attr := make([]byte, 12)
			binary.BigEndian.PutUint16(attr[0:2], xorMappedAddress)
			binary.BigEndian.PutUint16(attr[2:4], 8) // attribute length
			attr[4] = 0
			attr[5] = 0x01 // family IPv4
			binary.BigEndian.PutUint16(attr[6:8], uint16(raddr.Port)^uint16(magicCookie>>16))
			ip4 := claimed.As4()
			var cookie [4]byte
			binary.BigEndian.PutUint32(cookie[:], magicCookie)
			for i := 0; i < 4; i++ {
				attr[8+i] = ip4[i] ^ cookie[i]
			}

			// Build response header (success type = 0x0101).
			resp := make([]byte, 20+len(attr))
			binary.BigEndian.PutUint16(resp[0:2], 0x0101)
			binary.BigEndian.PutUint16(resp[2:4], uint16(len(attr)))
			binary.BigEndian.PutUint32(resp[4:8], magicCookie)
			copy(resp[8:20], txid)
			copy(resp[20:], attr)

			_, _ = conn.WriteToUDP(resp, raddr)
		}
	}()
	return conn.LocalAddr().String(), conn
}
