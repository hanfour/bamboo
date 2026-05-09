// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Frame types per ADR 0013.
const (
	TypeServerHello byte = 0x01
	TypeClientHello byte = 0x02
	TypePacket      byte = 0x03
	TypePeerGone    byte = 0x04
	TypeKeepalive   byte = 0x05
)

// PubKeyLen is the size of a Curve25519 public key in bytes.
const PubKeyLen = 32

// MaxFrameSize bounds a single frame. Generous for WG packets (max
// MTU ~1500 + 32-byte dst key + 5-byte header), defensive against
// malicious or misbehaving clients.
const MaxFrameSize = 64 * 1024

// Frame is the decoded form of one wire frame.
//
//	+--------+-----+----------+
//	|  len   | typ | payload  |
//	| 4 BE   | 1B  | variable |
//	+--------+-----+----------+
type Frame struct {
	Type    byte
	Payload []byte
}

// Encode writes a frame's wire form. The 4-byte length prefix counts
// the entire frame including itself.
func Encode(f Frame) ([]byte, error) {
	total := 4 + 1 + len(f.Payload)
	if total > MaxFrameSize {
		return nil, fmt.Errorf("frame too large: %d > %d", total, MaxFrameSize)
	}
	buf := make([]byte, total)
	binary.BigEndian.PutUint32(buf[0:4], uint32(total))
	buf[4] = f.Type
	copy(buf[5:], f.Payload)
	return buf, nil
}

// Decode parses a complete frame from buf. Returns an error if buf is
// truncated, too large, or has an inconsistent length prefix.
func Decode(buf []byte) (Frame, error) {
	if len(buf) < 5 {
		return Frame{}, errors.New("frame too short")
	}
	declared := binary.BigEndian.Uint32(buf[0:4])
	if int(declared) != len(buf) {
		return Frame{}, fmt.Errorf("declared length %d does not match buf %d", declared, len(buf))
	}
	if declared > MaxFrameSize {
		return Frame{}, fmt.Errorf("frame too large: %d", declared)
	}
	return Frame{Type: buf[4], Payload: buf[5:]}, nil
}

// PacketFrame is the decoded form of a TypePacket payload:
//
//	[32-byte dst pubkey] [WG packet bytes]
type PacketFrame struct {
	DstKey [PubKeyLen]byte
	Body   []byte
}

// ParsePacket decodes a TypePacket payload into its (DstKey, Body)
// pair. Returns an error when the payload is shorter than 32 bytes
// (no destination key).
func ParsePacket(payload []byte) (PacketFrame, error) {
	if len(payload) < PubKeyLen {
		return PacketFrame{}, errors.New("packet payload missing destination key")
	}
	var p PacketFrame
	copy(p.DstKey[:], payload[:PubKeyLen])
	p.Body = payload[PubKeyLen:]
	return p, nil
}

// EncodePacket builds a TypePacket Frame whose payload is the
// destination key prefix followed by the body bytes.
func EncodePacket(dst [PubKeyLen]byte, body []byte) Frame {
	payload := make([]byte, PubKeyLen+len(body))
	copy(payload[:PubKeyLen], dst[:])
	copy(payload[PubKeyLen:], body)
	return Frame{Type: TypePacket, Payload: payload}
}

// ClientHello is the decoded form of a CLIENT_HELLO payload. Layout:
//
//	[32-byte src pubkey] [auth token bytes]
//
// The auth token is verified by Server.verifyAuth as a JWT signed
// with the controller's shared secret.
type ClientHello struct {
	SrcKey    [PubKeyLen]byte
	AuthToken string
}

// ParseClientHello extracts the source pubkey + auth token from a
// CLIENT_HELLO payload. Returns an error when the payload is shorter
// than 32 bytes.
func ParseClientHello(payload []byte) (ClientHello, error) {
	if len(payload) < PubKeyLen {
		return ClientHello{}, errors.New("client_hello missing source key")
	}
	var ch ClientHello
	copy(ch.SrcKey[:], payload[:PubKeyLen])
	ch.AuthToken = string(payload[PubKeyLen:])
	return ch, nil
}
