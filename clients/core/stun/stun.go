// SPDX-License-Identifier: Apache-2.0

// Package stun is a small client for RFC 5389 STUN binding requests.
//
// It implements only the slice of the protocol bamboo needs: send a
// binding request, parse XOR-MAPPED-ADDRESS from the response, return
// the discovered host:port. No long-poll keepalives, no message
// integrity, no DNS-failover. The intent is endpoint discovery for
// peer-to-peer WireGuard handshakes; that does not require the full
// RFC.
package stun

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// DefaultServer is Google's public STUN server. It is reachable from
// most networks, but operators are expected to override via
// configuration in production deployments.
const DefaultServer = "stun.l.google.com:19302"

// magicCookie is the fixed value in STUN message headers (RFC 5389 §6).
const magicCookie uint32 = 0x2112A442

// bindingRequest is the message type for a Binding Request (§7.1).
const bindingRequest uint16 = 0x0001

// xorMappedAddress is the attribute type for XOR-MAPPED-ADDRESS (§15.2).
const xorMappedAddress uint16 = 0x0020

// mappedAddress is the legacy MAPPED-ADDRESS attribute (RFC 3489
// §11.2.1). Some servers still emit it; we accept it as a fallback.
const mappedAddress uint16 = 0x0001

// Discover sends a binding request to server (host:port) over UDP and
// returns the externally-visible host:port the bound socket appears at.
// localAddr can be empty to let the OS pick the source.
//
// timeout governs both the dial and the read. A reasonable default is
// 2 * time.Second.
func Discover(server string, timeout time.Duration) (netip.AddrPort, error) {
	return DiscoverFrom("", server, timeout)
}

// DiscoverFrom is Discover with an explicit local UDP bind address —
// callers that want the discovered endpoint to match the *WireGuard*
// listening port should bind first to that port and pass it here.
func DiscoverFrom(localUDP, server string, timeout time.Duration) (netip.AddrPort, error) {
	var laddr *net.UDPAddr
	if localUDP != "" {
		var err error
		laddr, err = net.ResolveUDPAddr("udp", localUDP)
		if err != nil {
			return netip.AddrPort{}, fmt.Errorf("resolve local %q: %w", localUDP, err)
		}
	}
	raddr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("resolve server %q: %w", server, err)
	}
	conn, err := net.DialUDP("udp", laddr, raddr)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("dial %s: %w", server, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(timeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return netip.AddrPort{}, err
	}

	req, txID, err := buildBindingRequest()
	if err != nil {
		return netip.AddrPort{}, err
	}
	if _, err := conn.Write(req); err != nil {
		return netip.AddrPort{}, fmt.Errorf("write: %w", err)
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("read: %w", err)
	}
	return parseResponse(buf[:n], txID)
}

// buildBindingRequest constructs a 20-byte STUN header for a
// Binding Request with no attributes. Returns the bytes plus the
// 12-byte transaction id so the caller can verify the response.
func buildBindingRequest() ([]byte, [12]byte, error) {
	var txID [12]byte
	if _, err := rand.Read(txID[:]); err != nil {
		return nil, txID, err
	}
	hdr := make([]byte, 20)
	binary.BigEndian.PutUint16(hdr[0:2], bindingRequest)
	binary.BigEndian.PutUint16(hdr[2:4], 0) // length: no attributes
	binary.BigEndian.PutUint32(hdr[4:8], magicCookie)
	copy(hdr[8:20], txID[:])
	return hdr, txID, nil
}

func parseResponse(buf []byte, expectedTxID [12]byte) (netip.AddrPort, error) {
	if len(buf) < 20 {
		return netip.AddrPort{}, errors.New("response too short")
	}
	msgType := binary.BigEndian.Uint16(buf[0:2])
	if msgType&0x0110 != 0x0100 {
		return netip.AddrPort{}, fmt.Errorf("not a success response: type=0x%04x", msgType)
	}
	if cookie := binary.BigEndian.Uint32(buf[4:8]); cookie != magicCookie {
		return netip.AddrPort{}, fmt.Errorf("bad magic cookie 0x%08x", cookie)
	}
	if [12]byte(buf[8:20]) != expectedTxID {
		return netip.AddrPort{}, errors.New("transaction id mismatch")
	}
	length := int(binary.BigEndian.Uint16(buf[2:4]))
	if 20+length > len(buf) {
		return netip.AddrPort{}, errors.New("declared length exceeds response")
	}

	attrs := buf[20 : 20+length]
	for len(attrs) >= 4 {
		atype := binary.BigEndian.Uint16(attrs[0:2])
		alen := int(binary.BigEndian.Uint16(attrs[2:4]))
		if 4+alen > len(attrs) {
			return netip.AddrPort{}, errors.New("truncated attribute")
		}
		body := attrs[4 : 4+alen]
		switch atype {
		case xorMappedAddress:
			return parseXORMappedAddress(body, expectedTxID)
		case mappedAddress:
			return parseMappedAddress(body)
		}
		// 4-byte alignment padding between TLVs.
		step := 4 + alen
		if pad := step % 4; pad != 0 {
			step += 4 - pad
		}
		if step > len(attrs) {
			break
		}
		attrs = attrs[step:]
	}
	return netip.AddrPort{}, errors.New("no MAPPED-ADDRESS in response")
}

func parseXORMappedAddress(body []byte, txID [12]byte) (netip.AddrPort, error) {
	if len(body) < 8 {
		return netip.AddrPort{}, errors.New("XOR-MAPPED-ADDRESS too short")
	}
	family := body[1]
	port := binary.BigEndian.Uint16(body[2:4]) ^ uint16(magicCookie>>16)
	switch family {
	case 0x01: // IPv4
		var addr [4]byte
		var cookie [4]byte
		binary.BigEndian.PutUint32(cookie[:], magicCookie)
		for i := 0; i < 4; i++ {
			addr[i] = body[4+i] ^ cookie[i]
		}
		return netip.AddrPortFrom(netip.AddrFrom4(addr), port), nil
	case 0x02: // IPv6
		if len(body) < 20 {
			return netip.AddrPort{}, errors.New("IPv6 XOR-MAPPED-ADDRESS too short")
		}
		var key [16]byte
		binary.BigEndian.PutUint32(key[0:4], magicCookie)
		copy(key[4:], txID[:])
		var addr [16]byte
		for i := 0; i < 16; i++ {
			addr[i] = body[4+i] ^ key[i]
		}
		return netip.AddrPortFrom(netip.AddrFrom16(addr), port), nil
	default:
		return netip.AddrPort{}, fmt.Errorf("unknown family 0x%02x", family)
	}
}

func parseMappedAddress(body []byte) (netip.AddrPort, error) {
	if len(body) < 8 {
		return netip.AddrPort{}, errors.New("MAPPED-ADDRESS too short")
	}
	family := body[1]
	port := binary.BigEndian.Uint16(body[2:4])
	switch family {
	case 0x01:
		var addr [4]byte
		copy(addr[:], body[4:8])
		return netip.AddrPortFrom(netip.AddrFrom4(addr), port), nil
	case 0x02:
		if len(body) < 20 {
			return netip.AddrPort{}, errors.New("IPv6 MAPPED-ADDRESS too short")
		}
		var addr [16]byte
		copy(addr[:], body[4:20])
		return netip.AddrPortFrom(netip.AddrFrom16(addr), port), nil
	default:
		return netip.AddrPort{}, fmt.Errorf("unknown family 0x%02x", family)
	}
}
