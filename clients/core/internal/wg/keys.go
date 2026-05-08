// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wg owns WireGuard-facing primitives: Curve25519 key material and
// the configuration we would push into a kernel or userspace device.
//
// Bringing up an actual WireGuard interface requires root or
// CAP_NET_ADMIN. That step lives in a follow-up; the code here is the
// prerequisite that an unprivileged process can compute and validate.
package wg

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// Key sizes in bytes.
const (
	KeySize = 32 // Curve25519 secret/public/preshared key length
)

// PrivateKey is a Curve25519 secret. The zero value is invalid.
type PrivateKey [KeySize]byte

// PublicKey is a Curve25519 public key.
type PublicKey [KeySize]byte

// ErrInvalidKey wraps any decoding or validation failure on key material.
var ErrInvalidKey = errors.New("invalid wireguard key")

// GeneratePrivateKey returns a new random Curve25519 private key with the
// canonical clamping applied per RFC 7748 §5.
func GeneratePrivateKey() (PrivateKey, error) {
	var k PrivateKey
	if _, err := rand.Read(k[:]); err != nil {
		return PrivateKey{}, fmt.Errorf("read random: %w", err)
	}
	clamp(k[:])
	return k, nil
}

// PublicKey returns the Curve25519 public key derived from p.
func (p PrivateKey) PublicKey() PublicKey {
	var pub PublicKey
	curve25519.ScalarBaseMult((*[KeySize]byte)(&pub), (*[KeySize]byte)(&p))
	return pub
}

// Base64 returns the standard-base64 encoded form of p (no padding stripped).
// This matches `wg`/`wireguard-tools` behaviour: 32 bytes → 44 chars.
func (p PrivateKey) Base64() string { return base64.StdEncoding.EncodeToString(p[:]) }

// Base64 returns the standard-base64 encoded form of pub.
func (pub PublicKey) Base64() string { return base64.StdEncoding.EncodeToString(pub[:]) }

// PrivateKeyFromBase64 decodes a key string produced by `wg genkey` or
// PrivateKey.Base64.
func PrivateKeyFromBase64(s string) (PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != KeySize {
		return PrivateKey{}, ErrInvalidKey
	}
	var k PrivateKey
	copy(k[:], b)
	return k, nil
}

// PublicKeyFromBase64 decodes a public key string. Used when receiving
// peer keys from the controller.
func PublicKeyFromBase64(s string) (PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != KeySize {
		return PublicKey{}, ErrInvalidKey
	}
	var k PublicKey
	copy(k[:], b)
	return k, nil
}

// clamp applies the Curve25519 mask required for valid private keys.
func clamp(b []byte) {
	b[0] &= 248
	b[31] &= 127
	b[31] |= 64
}
