// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestEnforcePeerBinding is the audit M-2 regression: a peer-session bearer
// must only act on the peer it's bound to. Without the binding, a valid
// token for peer A could Heartbeat / WatchPeers as peer B (endpoint
// poisoning, netmap snooping).
func TestEnforcePeerBinding(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	h := &CoordinatorHandler{auth: &AuthHandler{sessionSec: secret}}

	peerA, peerB := uuid.New(), uuid.New()
	tenant := uuid.New()

	peerTok, err := auth.IssuePeerSessionToken(secret, auth.PeerSessionClaims{
		TenantID: tenant, PeerID: peerA,
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue peer-session token: %v", err)
	}
	ctxA := incomingBearer(peerTok)

	// Bound peer → allowed.
	if err := h.enforcePeerBinding(ctxA, peerA.String()); err != nil {
		t.Errorf("token for peerA acting on peerA should pass, got %v", err)
	}
	// Different peer → PermissionDenied (the whole point).
	if err := h.enforcePeerBinding(ctxA, peerB.String()); status.Code(err) != codes.PermissionDenied {
		t.Errorf("token for peerA acting on peerB should be PermissionDenied, got code=%v (%v)",
			status.Code(err), err)
	}

	// No bearer → skip (dev / require_auth off paths keep working).
	if err := h.enforcePeerBinding(context.Background(), peerB.String()); err != nil {
		t.Errorf("no bearer should skip binding, got %v", err)
	}

	// A user-session bearer (admin) is now tenant-bound too, but that
	// check needs a wired peers repo; this no-DB handler has none, so it
	// skips (the h.peers==nil guard). The enforced cross-tenant denial —
	// a tenant-A user JWT acting on a tenant-B peer → NotFound — is
	// covered by e2e TestCrossTenant_GRPC* with a real peers repo.
	userTok, err := auth.IssueSessionToken(secret, auth.SessionClaims{
		UserID: uuid.New(), TenantID: tenant,
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue session token: %v", err)
	}
	if err := h.enforcePeerBinding(incomingBearer(userTok), peerB.String()); err != nil {
		t.Errorf("user-session bearer with no peers repo should skip, got %v", err)
	}

	// Not configured (no session secret) → skip.
	h2 := &CoordinatorHandler{auth: &AuthHandler{}}
	if err := h2.enforcePeerBinding(ctxA, peerB.String()); err != nil {
		t.Errorf("unconfigured handler should skip binding, got %v", err)
	}
}

func incomingBearer(token string) context.Context {
	md := metadata.Pairs("authorization", "Bearer "+token)
	return metadata.NewIncomingContext(context.Background(), md)
}
