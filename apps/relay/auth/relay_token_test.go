// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// mintRelayToken reconstructs a controller-issued relay token so the
// vendored verify path can be exercised without importing
// apps/controller/internal/auth (a different Go module). It MUST mirror
// IssueRelayToken there: HMAC over relayTokenDomain || body. When
// withDomain is false it emits the pre-fix (domain-less) format — used
// to assert that shape is now rejected.
func mintRelayToken(t *testing.T, secret []byte, claims RelayClaims, withDomain bool) string {
	t.Helper()
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, secret)
	if withDomain {
		mac.Write([]byte(relayTokenDomain))
	}
	mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + sig
}

func TestVerifyRelayToken_AcceptsDomainedToken(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	claims := RelayClaims{
		TenantID:    "tenant-1",
		PeerID:      "peer-1",
		WGPublicKey: "wg-abc",
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	}
	tok := mintRelayToken(t, secret, claims, true)
	got, err := VerifyRelayToken(secret, tok)
	if err != nil {
		t.Fatalf("verify domained token: %v", err)
	}
	if got.TenantID != "tenant-1" || got.PeerID != "peer-1" || got.WGPublicKey != "wg-abc" {
		t.Errorf("claims mismatch: %+v", got)
	}
}

// TestVerifyRelayToken_RejectsDomainlessToken is the relay-side H-1
// regression: a token signed WITHOUT the domain (the pre-fix format, or
// a session/peer token forged with the same shared secret) must be
// rejected. Guards against the vendored verify path silently dropping
// the domain and re-opening the token-confusion hole.
func TestVerifyRelayToken_RejectsDomainlessToken(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	claims := RelayClaims{
		TenantID:  "tenant-1",
		PeerID:    "peer-1",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	tok := mintRelayToken(t, secret, claims, false) // no domain prefix
	if _, err := VerifyRelayToken(secret, tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("domain-less token accepted; want ErrInvalidToken, got %v", err)
	}
}

// TestRelayTokenDomain_Pinned is a cross-module tripwire: this constant
// MUST equal relayTokenDomain in
// apps/controller/internal/auth/relay_token.go. Change one, change both
// — otherwise controller-issued tokens silently stop verifying here.
func TestRelayTokenDomain_Pinned(t *testing.T) {
	const want = "bamboo.relay-token.v1"
	if relayTokenDomain != want {
		t.Fatalf("relayTokenDomain = %q, want %q — keep it byte-identical to the controller's constant", relayTokenDomain, want)
	}
}

func TestVerifyRelayToken_RejectsExpired(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	claims := RelayClaims{
		TenantID:  "tenant-1",
		PeerID:    "peer-1",
		ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	}
	tok := mintRelayToken(t, secret, claims, true)
	if _, err := VerifyRelayToken(secret, tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token accepted; want ErrInvalidToken, got %v", err)
	}
}

func TestVerifyRelayToken_RejectsWrongSecret(t *testing.T) {
	claims := RelayClaims{
		TenantID:  "tenant-1",
		PeerID:    "peer-1",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	tok := mintRelayToken(t, []byte("secret-a-padding-aaaaaaaaaaaaaaaaaa"), claims, true)
	if _, err := VerifyRelayToken([]byte("secret-b-padding-bbbbbbbbbbbbbbbbbb"), tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong-secret token accepted; want ErrInvalidToken, got %v", err)
	}
}

// --- Ed25519 relay token (C-1 root fix) ---------------------------------

// mintEd25519RelayToken reconstructs a controller-issued Ed25519 relay
// token so the vendored verify can be exercised without importing the
// controller module. MUST mirror IssueRelayTokenEd25519 there: Ed25519
// over relayTokenDomain || encoded, sig base64url-encoded.
func mintEd25519RelayToken(t *testing.T, priv ed25519.PrivateKey, claims RelayClaims) string {
	t.Helper()
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	sig := ed25519.Sign(priv, []byte(relayTokenDomain+encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestVerifyRelayTokenEd25519_AcceptsControllerFormat(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	claims := RelayClaims{
		TenantID: "tenant-1", PeerID: "peer-1", WGPublicKey: "wg-abc",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	tok := mintEd25519RelayToken(t, priv, claims)
	got, err := VerifyRelayTokenEd25519(pub, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.TenantID != "tenant-1" || got.PeerID != "peer-1" || got.WGPublicKey != "wg-abc" {
		t.Errorf("claims mismatch: %+v", got)
	}
}

// TestVerifyRelayTokenEd25519_RejectsHMACToken is the cross-scheme guard
// on the relay side: an HMAC token must not slip through the Ed25519 path.
func TestVerifyRelayTokenEd25519_RejectsHMACToken(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	claims := RelayClaims{TenantID: "t", PeerID: "p", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	hmacTok := mintRelayToken(t, []byte("test-secret-with-at-least-32-bytes-padding"), claims, true)
	if _, err := VerifyRelayTokenEd25519(pub, hmacTok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("HMAC token accepted by Ed25519 verify; want ErrInvalidToken, got %v", err)
	}
}

func TestVerifyRelayTokenEd25519_RejectsWrongPublicKey(t *testing.T) {
	_, priv1, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	claims := RelayClaims{TenantID: "t", PeerID: "p", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	tok := mintEd25519RelayToken(t, priv1, claims)
	if _, err := VerifyRelayTokenEd25519(pub2, tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong-key token accepted; want ErrInvalidToken, got %v", err)
	}
}

func TestParseRelayPublicKey_RoundTripAndReject(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	b64 := base64.StdEncoding.EncodeToString(pub)
	got, err := ParseRelayPublicKey(b64)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.Equal(pub) {
		t.Error("round-tripped public key differs")
	}
	if _, err := ParseRelayPublicKey("not-base64!!!"); err == nil {
		t.Error("accepted non-base64")
	}
	if _, err := ParseRelayPublicKey("aGVsbG8="); err == nil {
		t.Error("accepted wrong-length key")
	}
}
