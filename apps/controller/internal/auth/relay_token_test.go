// SPDX-License-Identifier: AGPL-3.0-or-later

package auth_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
)

func TestRelayToken_RoundTrip(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	tenantID := uuid.New()
	peerID := uuid.New()

	tok, err := auth.IssueRelayToken(secret, auth.RelayClaims{
		TenantID:    tenantID,
		PeerID:      peerID,
		WGPublicKey: "abc123",
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	got, err := auth.VerifyRelayToken(secret, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.TenantID != tenantID || got.PeerID != peerID || got.WGPublicKey != "abc123" {
		t.Errorf("claims mismatch: %+v", got)
	}
}

func TestRelayToken_RejectsTamperedSignature(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	tok, _ := auth.IssueRelayToken(secret, auth.RelayClaims{
		TenantID: uuid.New(),
		PeerID:   uuid.New(),
	}, time.Hour)
	// Flip first char of signature.
	body, sig, _ := strings.Cut(tok, ".")
	flipped := body + "." + flipFirstByte(sig)
	if _, err := auth.VerifyRelayToken(secret, flipped); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken, got %v", err)
	}
}

func TestRelayToken_RejectsExpired(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	tok, _ := auth.IssueRelayToken(secret, auth.RelayClaims{
		TenantID:  uuid.New(),
		PeerID:    uuid.New(),
		IssuedAt:  time.Now().Add(-2 * time.Hour).Unix(),
		ExpiresAt: time.Now().Add(-1 * time.Hour).Unix(),
	}, time.Hour)
	if _, err := auth.VerifyRelayToken(secret, tok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken for expired, got %v", err)
	}
}

func TestRelayToken_RejectsWrongSecret(t *testing.T) {
	tok, _ := auth.IssueRelayToken([]byte("a-secret-with-padding-aaaaaaaaaaa"), auth.RelayClaims{
		TenantID: uuid.New(),
		PeerID:   uuid.New(),
	}, time.Hour)
	if _, err := auth.VerifyRelayToken([]byte("a-different-secret-with-padding-x"), tok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken for wrong secret, got %v", err)
	}
}

// TestRelayToken_NotVerifiableAsSession is the H-1 regression: a relay
// token must NOT pass VerifySessionToken even when both token types are
// signed with the SAME secret. Before the relay-token HMAC domain was
// added, the two constructions were byte-identical, so a relay token
// (obtainable by any peer via /api/v1/relay-token) verified as a
// session JWT and — decoded as SessionClaims with a nil sub — slipped
// past resolveBearerToken to bypass the device-approval gate. The
// domain prefix makes the signatures disjoint; secret separation (C-1)
// is defense in depth on top of this, not the primary guard.
func TestRelayToken_NotVerifiableAsSession(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	tok, err := auth.IssueRelayToken(secret, auth.RelayClaims{
		TenantID: uuid.New(),
		PeerID:   uuid.New(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue relay token: %v", err)
	}
	if _, err := auth.VerifySessionToken(secret, tok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("relay token verified as a SESSION token (token confusion / H-1); want ErrInvalidToken, got %v", err)
	}
}

// TestSessionToken_NotVerifiableAsRelay is the reverse direction: a
// user-session JWT must NOT pass VerifyRelayToken under a shared secret.
func TestSessionToken_NotVerifiableAsRelay(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	tok, err := auth.IssueSessionToken(secret, auth.SessionClaims{
		UserID:   uuid.New(),
		TenantID: uuid.New(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue session token: %v", err)
	}
	if _, err := auth.VerifyRelayToken(secret, tok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("session token verified as a RELAY token; want ErrInvalidToken, got %v", err)
	}
}

func flipFirstByte(s string) string {
	if len(s) == 0 {
		return s
	}
	if s[0] == 'A' {
		return "B" + s[1:]
	}
	return "A" + s[1:]
}

// --- Ed25519 relay token (C-1 root fix) ---------------------------------

func TestRelayTokenEd25519_RoundTrip(t *testing.T) {
	seedB64, pubB64, err := auth.GenerateRelayKeypair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	priv, err := auth.ParseRelaySigningKey(seedB64)
	if err != nil {
		t.Fatalf("parse signing key: %v", err)
	}
	pub, err := auth.ParseRelayPublicKey(pubB64)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}

	tenantID, peerID := uuid.New(), uuid.New()
	tok, err := auth.IssueRelayTokenEd25519(priv, auth.RelayClaims{
		TenantID: tenantID, PeerID: peerID, WGPublicKey: "wg-1",
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	got, err := auth.VerifyRelayTokenEd25519(pub, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.TenantID != tenantID || got.PeerID != peerID || got.WGPublicKey != "wg-1" {
		t.Errorf("claims mismatch: %+v", got)
	}
}

// TestRelayTokenEd25519_RejectsWrongPublicKey: a token signed by one key
// must not verify under another's public half.
func TestRelayTokenEd25519_RejectsWrongPublicKey(t *testing.T) {
	seed1, _, _ := auth.GenerateRelayKeypair()
	_, pub2B64, _ := auth.GenerateRelayKeypair()
	priv1, _ := auth.ParseRelaySigningKey(seed1)
	pub2, _ := auth.ParseRelayPublicKey(pub2B64)

	tok, _ := auth.IssueRelayTokenEd25519(priv1, auth.RelayClaims{
		TenantID: uuid.New(), PeerID: uuid.New(),
	}, time.Hour)
	if _, err := auth.VerifyRelayTokenEd25519(pub2, tok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("token verified under the wrong public key; want ErrInvalidToken, got %v", err)
	}
}

// TestRelayTokenEd25519_RejectsExpired guards TTL enforcement.
func TestRelayTokenEd25519_RejectsExpired(t *testing.T) {
	seed, pubB64, _ := auth.GenerateRelayKeypair()
	priv, _ := auth.ParseRelaySigningKey(seed)
	pub, _ := auth.ParseRelayPublicKey(pubB64)
	tok, _ := auth.IssueRelayTokenEd25519(priv, auth.RelayClaims{
		TenantID: uuid.New(), PeerID: uuid.New(),
		IssuedAt:  time.Now().Add(-2 * time.Hour).Unix(),
		ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	}, time.Hour)
	if _, err := auth.VerifyRelayTokenEd25519(pub, tok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expired token accepted; want ErrInvalidToken, got %v", err)
	}
}

// TestRelayToken_CrossSchemeRejection: an HMAC token must not verify with
// the Ed25519 path, and an Ed25519 token must not verify with the HMAC
// path. The two schemes are config-selected and must never cross.
func TestRelayToken_CrossSchemeRejection(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	seed, pubB64, _ := auth.GenerateRelayKeypair()
	priv, _ := auth.ParseRelaySigningKey(seed)
	pub, _ := auth.ParseRelayPublicKey(pubB64)
	claims := auth.RelayClaims{TenantID: uuid.New(), PeerID: uuid.New()}

	hmacTok, _ := auth.IssueRelayToken(secret, claims, time.Hour)
	ed25519Tok, _ := auth.IssueRelayTokenEd25519(priv, claims, time.Hour)

	if _, err := auth.VerifyRelayTokenEd25519(pub, hmacTok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("HMAC token verified as Ed25519; want ErrInvalidToken, got %v", err)
	}
	if _, err := auth.VerifyRelayToken(secret, ed25519Tok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("Ed25519 token verified as HMAC; want ErrInvalidToken, got %v", err)
	}
}

func TestParseRelayKeys_RejectMalformed(t *testing.T) {
	if _, err := auth.ParseRelaySigningKey("not-base64!!!"); err == nil {
		t.Error("ParseRelaySigningKey accepted non-base64")
	}
	if _, err := auth.ParseRelaySigningKey("aGVsbG8="); err == nil {
		t.Error("ParseRelaySigningKey accepted a wrong-length seed")
	}
	if _, err := auth.ParseRelayPublicKey("aGVsbG8="); err == nil {
		t.Error("ParseRelayPublicKey accepted a wrong-length key")
	}
}
