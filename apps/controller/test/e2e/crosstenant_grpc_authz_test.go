// SPDX-License-Identifier: AGPL-3.0-or-later

// gRPC counterpart of crosstenant_authz_test.go. The gRPC
// Heartbeat / WatchPeers handlers gate on enforcePeerBinding, which
// binds a PEER-session token to its peer_id but skips USER-session
// JWTs entirely — so a tenant-A user JWT can drive heartbeat / watch
// on a tenant-B peer (same cross-tenant IDOR as the REST side).
// These tests encode the desired contract (deny cross-tenant) and so
// FAIL until enforcePeerBinding also binds the user-session case to
// the caller's tenant. gRPC is not proxied in prod, so this is
// defense-in-depth rather than a live remote breach.
package e2e

import (
	"context"
	"testing"
	"time"

	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// bearerCtx attaches a session JWT as gRPC authorization metadata.
func bearerCtx(ctx context.Context, jwt string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+jwt)
}

// TestCrossTenant_GRPCHeartbeatRejectsForeignPeer: a tenant-A user JWT
// must not drive Heartbeat on a tenant-B peer. Currently succeeds
// (code=OK) because enforcePeerBinding waves user-session tokens
// through → the assertion below fails until it's fixed.
func TestCrossTenant_GRPCHeartbeatRejectsForeignPeer(t *testing.T) {
	f := startFixture(t)

	victimPeerID, _ := registerVictimPeerInSeparateTenant(t, f, []string{"203.0.113.10:51820"})
	f.enableRequireAuth()
	attackerJWT, _ := f.mintJWTWithUser(t, false)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := f.coord.Heartbeat(bearerCtx(ctx, attackerJWT), &bamboov1.HeartbeatRequest{
		PeerId:    victimPeerID,
		Endpoints: []string{"198.51.100.66:51820"},
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("CROSS-TENANT gRPC WRITE: tenant-A user JWT drove Heartbeat on "+
			"tenant-B peer %s; want codes.NotFound, got code=%s err=%v",
			victimPeerID, status.Code(err), err)
	}
}

// TestCrossTenant_GRPCWatchPeersRejectsForeignPeer: same, for the
// server-streaming WatchPeers. The terminal status surfaces on the
// first Recv. Currently the stream opens (and Recv blocks to the
// deadline) instead of returning NotFound.
func TestCrossTenant_GRPCWatchPeersRejectsForeignPeer(t *testing.T) {
	f := startFixture(t)

	victimPeerID, _ := registerVictimPeerInSeparateTenant(t, f, nil)
	f.enableRequireAuth()
	attackerJWT, _ := f.mintJWTWithUser(t, false)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	stream, err := f.coord.WatchPeers(bearerCtx(ctx, attackerJWT), &bamboov1.WatchPeersRequest{
		PeerId: victimPeerID,
	})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.NotFound {
		t.Errorf("CROSS-TENANT gRPC READ: tenant-A user JWT opened WatchPeers for "+
			"tenant-B peer %s; want codes.NotFound, got code=%s err=%v",
			victimPeerID, status.Code(err), err)
	}
}
