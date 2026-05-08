// SPDX-License-Identifier: AGPL-3.0-or-later

package wg_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/hanfour/bamboo/clients/core/internal/wg"
)

func TestGeneratePrivateKey_ProducesNonZero(t *testing.T) {
	k, err := wg.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	var zero wg.PrivateKey
	if k == zero {
		t.Error("private key is all zeros")
	}
	// Clamping invariants (RFC 7748).
	if k[0]&7 != 0 {
		t.Errorf("low bits not cleared: %08b", k[0])
	}
	if k[31]&0x80 != 0 {
		t.Errorf("high bit not cleared: %08b", k[31])
	}
	if k[31]&0x40 == 0 {
		t.Errorf("bit 6 of last byte not set: %08b", k[31])
	}
}

func TestPrivateKey_PublicKey_Stable(t *testing.T) {
	priv, err := wg.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	a := priv.PublicKey()
	b := priv.PublicKey()
	if a != b {
		t.Error("PublicKey is non-deterministic for the same private key")
	}
	var zero wg.PublicKey
	if a == zero {
		t.Error("public key is all zeros")
	}
}

func TestKey_Base64RoundTrip(t *testing.T) {
	priv, err := wg.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	pub := priv.PublicKey()

	pStr := priv.Base64()
	if len(pStr) != 44 {
		t.Errorf("private base64 length = %d, want 44", len(pStr))
	}
	pBack, err := wg.PrivateKeyFromBase64(pStr)
	if err != nil {
		t.Fatalf("PrivateKeyFromBase64: %v", err)
	}
	if pBack != priv {
		t.Error("private key did not round-trip")
	}

	pubStr := pub.Base64()
	pubBack, err := wg.PublicKeyFromBase64(pubStr)
	if err != nil {
		t.Fatalf("PublicKeyFromBase64: %v", err)
	}
	if pubBack != pub {
		t.Error("public key did not round-trip")
	}
}

func TestKeyFromBase64_RejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"not base64 at all!!",
		"AA==", // valid base64 but too short
		strings.Repeat("A", 45),
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := wg.PrivateKeyFromBase64(c); !errors.Is(err, wg.ErrInvalidKey) {
				t.Errorf("PrivateKeyFromBase64(%q) err = %v, want ErrInvalidKey", c, err)
			}
			if _, err := wg.PublicKeyFromBase64(c); !errors.Is(err, wg.ErrInvalidKey) {
				t.Errorf("PublicKeyFromBase64(%q) err = %v, want ErrInvalidKey", c, err)
			}
		})
	}
}
