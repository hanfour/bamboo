// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
)

// TestTenantFromBearer locks the security primitive shared by the Policy
// and Telemetry handlers: a peer- or user-session token yields its own
// tenant, and anything that doesn't verify yields ok=false (so callers
// fall back to the dev-only slug path rather than trusting a bad token).
func TestTenantFromBearer(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	tenant := uuid.New()

	userTok, err := auth.IssueSessionToken(secret,
		auth.SessionClaims{UserID: uuid.New(), TenantID: tenant}, time.Hour)
	if err != nil {
		t.Fatalf("issue user token: %v", err)
	}
	if got, ok := tenantFromBearer(secret, userTok); !ok || got != tenant {
		t.Errorf("user-session token: got (%s, %v), want (%s, true)", got, ok, tenant)
	}

	peerTok, err := auth.IssuePeerSessionToken(secret,
		auth.PeerSessionClaims{TenantID: tenant, PeerID: uuid.New()}, time.Hour)
	if err != nil {
		t.Fatalf("issue peer token: %v", err)
	}
	if got, ok := tenantFromBearer(secret, peerTok); !ok || got != tenant {
		t.Errorf("peer-session token: got (%s, %v), want (%s, true)", got, ok, tenant)
	}

	if _, ok := tenantFromBearer(secret, "not-a-token"); ok {
		t.Errorf("garbage token: ok=true, want false")
	}
	if _, ok := tenantFromBearer([]byte("another-secret-at-least-32-bytes-xx"), userTok); ok {
		t.Errorf("wrong-secret token: ok=true, want false")
	}
}
