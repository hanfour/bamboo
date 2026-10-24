// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
)

func TestRenewPeerSession_InsideWindow(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	h := &HTTPServer{secret: secret}
	now := time.Now().UTC().Truncate(time.Second)
	tenantID := uuid.New()
	peerID := uuid.New()
	claims := &auth.PeerSessionClaims{
		TenantID:    tenantID,
		PeerID:      peerID,
		WGPublicKey: "abc123",
		ExpiresAt:   now.Add(time.Hour).Unix(),
	}
	tok, exp, ok := h.renewPeerSession(claims, now)
	if !ok || tok == "" {
		t.Fatalf("renew inside window: ok=%v tok empty=%v", ok, tok == "")
	}
	if !exp.Equal(now.Add(peerSessionTokenTTL)) {
		t.Errorf("exp = %s, want %s", exp, now.Add(peerSessionTokenTTL))
	}
	got, err := auth.VerifyPeerSessionToken(secret, tok)
	if err != nil {
		t.Fatalf("verify renewed token: %v", err)
	}
	if got.TenantID != tenantID || got.PeerID != peerID || got.WGPublicKey != "abc123" {
		t.Errorf("renewed claims = %+v", got)
	}
	if got.ExpiresAt != exp.Unix() {
		t.Errorf("claim exp = %d, want %d", got.ExpiresAt, exp.Unix())
	}
}

func TestRenewPeerSession_NotYetDue(t *testing.T) {
	h := &HTTPServer{secret: []byte("test-secret-with-at-least-32-bytes-padding")}
	now := time.Now().UTC().Truncate(time.Second)
	claims := &auth.PeerSessionClaims{
		TenantID:  uuid.New(),
		PeerID:    uuid.New(),
		ExpiresAt: now.Add(12 * time.Hour).Unix(),
	}
	if _, _, ok := h.renewPeerSession(claims, now); ok {
		t.Fatal("renewed a token that still has 12h left")
	}
}

func TestRenewPeerSession_SkipsWithoutMaterial(t *testing.T) {
	now := time.Now()
	claims := &auth.PeerSessionClaims{
		TenantID:  uuid.New(),
		PeerID:    uuid.New(),
		ExpiresAt: now.Add(time.Hour).Unix(),
	}
	h := &HTTPServer{secret: []byte("test-secret-with-at-least-32-bytes-padding")}
	if _, _, ok := h.renewPeerSession(nil, now); ok {
		t.Fatal("nil claims renewed")
	}
	if _, _, ok := (&HTTPServer{}).renewPeerSession(claims, now); ok {
		t.Fatal("empty secret renewed")
	}
	claims.ExpiresAt = 0
	if _, _, ok := h.renewPeerSession(claims, now); ok {
		t.Fatal("zero expiry renewed")
	}
}
