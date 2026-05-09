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

func flipFirstByte(s string) string {
	if len(s) == 0 {
		return s
	}
	if s[0] == 'A' {
		return "B" + s[1:]
	}
	return "A" + s[1:]
}
