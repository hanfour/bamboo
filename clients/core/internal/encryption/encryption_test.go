// SPDX-License-Identifier: AGPL-3.0-or-later
//
// origin: netbirdio/netbird@7da94a4956af76f7187733aa488e9d20a0f62202
// upstream path: encryption/encryption_test.go
//
// Local changes vs upstream:
//   - rewritten from Ginkgo + Gomega to stdlib testing (one less heavy dep)
//   - uses google.golang.org/protobuf/types/known/wrapperspb.StringValue
//     as the test proto so we do not import a NetBird-specific testproto

package encryption_test

import (
	"bytes"
	"testing"

	"github.com/hanfour/bamboo/clients/core/internal/encryption"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func mustGenerateKey(t *testing.T) wgtypes.Key {
	t.Helper()
	k, err := wgtypes.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return k
}

func TestEncryptDecrypt_PlainBytes(t *testing.T) {
	enc := mustGenerateKey(t)
	dec := mustGenerateKey(t)

	msg := []byte("hello, mesh")
	sealed, err := encryption.Encrypt(msg, dec.PublicKey(), enc)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(sealed, msg) {
		t.Error("plaintext appears verbatim in ciphertext")
	}

	opened, err := encryption.Decrypt(sealed, enc.PublicKey(), dec)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(opened, msg) {
		t.Errorf("opened = %q, want %q", opened, msg)
	}
}

func TestDecrypt_ShortPayload(t *testing.T) {
	enc := mustGenerateKey(t)
	dec := mustGenerateKey(t)
	if _, err := encryption.Decrypt([]byte("nope"), enc.PublicKey(), dec); err == nil {
		t.Error("expected error on short payload")
	}
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	enc := mustGenerateKey(t)
	dec := mustGenerateKey(t)

	sealed, err := encryption.Encrypt([]byte("hello"), dec.PublicKey(), enc)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := encryption.Decrypt(sealed, enc.PublicKey(), dec); err == nil {
		t.Error("expected error on tampered ciphertext")
	}
}

func TestEncryptMessage_RoundTripProto(t *testing.T) {
	enc := mustGenerateKey(t)
	dec := mustGenerateKey(t)

	in := &wrapperspb.StringValue{Value: "hello, proto"}
	sealed, err := encryption.EncryptMessage(dec.PublicKey(), enc, in)
	if err != nil {
		t.Fatalf("EncryptMessage: %v", err)
	}

	out := &wrapperspb.StringValue{}
	if err := encryption.DecryptMessage(enc.PublicKey(), dec, sealed, out); err != nil {
		t.Fatalf("DecryptMessage: %v", err)
	}
	if out.GetValue() != in.GetValue() {
		t.Errorf("round-trip body = %q, want %q", out.GetValue(), in.GetValue())
	}
}
