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
