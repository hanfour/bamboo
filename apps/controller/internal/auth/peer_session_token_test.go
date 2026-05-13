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

func TestPeerSessionToken_RoundTrip(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	tenantID := uuid.New()
	peerID := uuid.New()

	tok, err := auth.IssuePeerSessionToken(secret, auth.PeerSessionClaims{
		TenantID:    tenantID,
		PeerID:      peerID,
		WGPublicKey: "abc123",
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	got, err := auth.VerifyPeerSessionToken(secret, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.TenantID != tenantID || got.PeerID != peerID || got.WGPublicKey != "abc123" {
		t.Errorf("claims mismatch: %+v", got)
	}
}

func TestPeerSessionToken_RejectsTamperedSignature(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	tok, _ := auth.IssuePeerSessionToken(secret, auth.PeerSessionClaims{
		TenantID: uuid.New(),
		PeerID:   uuid.New(),
	}, time.Hour)
	body, sig, _ := strings.Cut(tok, ".")
	flipped := body + "." + flipFirstByte(sig)
	if _, err := auth.VerifyPeerSessionToken(secret, flipped); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken, got %v", err)
	}
}

func TestPeerSessionToken_RejectsExpired(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	tok, _ := auth.IssuePeerSessionToken(secret, auth.PeerSessionClaims{
		TenantID:  uuid.New(),
		PeerID:    uuid.New(),
		IssuedAt:  time.Now().Add(-2 * time.Hour).Unix(),
		ExpiresAt: time.Now().Add(-1 * time.Hour).Unix(),
	}, time.Hour)
	if _, err := auth.VerifyPeerSessionToken(secret, tok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken for expired, got %v", err)
	}
}

func TestPeerSessionToken_RejectsWrongSecret(t *testing.T) {
	tok, _ := auth.IssuePeerSessionToken([]byte("a-secret-with-padding-aaaaaaaaaaa"), auth.PeerSessionClaims{
		TenantID: uuid.New(),
		PeerID:   uuid.New(),
	}, time.Hour)
	if _, err := auth.VerifyPeerSessionToken([]byte("a-different-secret-with-padding-x"), tok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken for wrong secret, got %v", err)
	}
}

func TestPeerSessionToken_DomainSeparation_RejectsSessionTokenAsPeerToken(t *testing.T) {
	// A user-session JWT signed with the same secret must NOT verify
	// as a peer session token. Domain separation in the HMAC input is
	// what makes this distinct — if the prefix were dropped, a leaked
	// user session JWT could be presented at peer-bound endpoints.
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	userTok, _ := auth.IssueSessionToken(secret, auth.SessionClaims{
		UserID:   uuid.New(),
		TenantID: uuid.New(),
	}, time.Hour)
	if _, err := auth.VerifyPeerSessionToken(secret, userTok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("expected user-session token to fail peer-session verify, got %v", err)
	}
}

func TestPeerSessionToken_RejectsZeroIDs(t *testing.T) {
	// Hand-crafted token whose claims JSON parses but carries the zero
	// uuid for tenant/peer. A future bug that issues such a token (e.g.
	// a nil-derefed handler) must not let it pass verification.
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	tok, _ := auth.IssuePeerSessionToken(secret, auth.PeerSessionClaims{
		TenantID: uuid.Nil,
		PeerID:   uuid.Nil,
	}, time.Hour)
	if _, err := auth.VerifyPeerSessionToken(secret, tok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken for zero ids, got %v", err)
	}
}
