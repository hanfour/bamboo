// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const testSecret = "test-secret-with-at-least-32-bytes-padding"

func okHandler(_ context.Context, _ any) (any, error) { return "ok", nil }

func TestRequireAuthUnaryInterceptor_PassesThroughWhenDisabled(t *testing.T) {
	icpt := requireAuthUnaryInterceptor(false, nil, nil)
	info := &grpc.UnaryServerInfo{FullMethod: "/bamboo.v1.PolicyService/PutPolicy"}
	resp, err := icpt(context.Background(), nil, info, okHandler)
	if err != nil || resp != "ok" {
		t.Errorf("disabled mode must pass through any method: resp=%v err=%v", resp, err)
	}
}

func TestRequireAuthUnaryInterceptor_WhitelistAllowsWithoutToken(t *testing.T) {
	icpt := requireAuthUnaryInterceptor(true, []byte(testSecret), nil)
	// Register stays whitelisted because its credential lives on the
	// payload (pre_auth_key_secret / bearer_token); the
	// CoordinatorHandler enforces it. Auth bootstrap methods are
	// whitelisted too — that's how a peer mints its first session.
	for _, fullMethod := range []string{
		"/bamboo.v1.CoordinatorService/Register",
		"/bamboo.v1.AuthService/StartOIDCFlow",
		"/bamboo.v1.AuthService/RedeemPreAuthKey",
	} {
		info := &grpc.UnaryServerInfo{FullMethod: fullMethod}
		resp, err := icpt(context.Background(), nil, info, okHandler)
		if err != nil || resp != "ok" {
			t.Errorf("%s should bypass auth: resp=%v err=%v", fullMethod, resp, err)
		}
	}
}

// Heartbeat and WatchPeers used to be whitelisted with no credential
// check at all. The doc's Finding #1 closes that hole — they now go
// through the bearer gate like any other authenticated method.
func TestRequireAuthUnaryInterceptor_HeartbeatRejectsUnauthenticated(t *testing.T) {
	icpt := requireAuthUnaryInterceptor(true, []byte(testSecret), nil)
	for _, fullMethod := range []string{
		"/bamboo.v1.CoordinatorService/Heartbeat",
		"/bamboo.v1.CoordinatorService/WatchPeers",
	} {
		info := &grpc.UnaryServerInfo{FullMethod: fullMethod}
		_, err := icpt(context.Background(), nil, info, okHandler)
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("%s without bearer should reject; got code=%v err=%v", fullMethod, status.Code(err), err)
		}
	}
}

// A peer-session bearer (issued at Register time) is the canonical
// credential for Heartbeat / WatchPeers in prod mode. verifyBearer
// accepts both peer-session and user-session tokens; this test
// pins the peer-session path so a future refactor doesn't silently
// regress the CLI's authedAdapter contract.
func TestRequireAuthUnaryInterceptor_AcceptsPeerSessionBearer(t *testing.T) {
	secret := []byte(testSecret)
	tok, err := auth.IssuePeerSessionToken(secret, auth.PeerSessionClaims{
		TenantID:    uuid.New(),
		PeerID:      uuid.New(),
		WGPublicKey: "abc",
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssuePeerSessionToken: %v", err)
	}
	icpt := requireAuthUnaryInterceptor(true, secret, nil)
	md := metadata.Pairs("authorization", "Bearer "+tok)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	info := &grpc.UnaryServerInfo{FullMethod: "/bamboo.v1.CoordinatorService/Heartbeat"}
	resp, err := icpt(ctx, nil, info, okHandler)
	if err != nil || resp != "ok" {
		t.Errorf("peer-session bearer should pass: resp=%v err=%v", resp, err)
	}
}

func TestRequireAuthUnaryInterceptor_RejectsUnauthenticated(t *testing.T) {
	icpt := requireAuthUnaryInterceptor(true, []byte(testSecret), nil)
	info := &grpc.UnaryServerInfo{FullMethod: "/bamboo.v1.PolicyService/PutPolicy"}
	_, err := icpt(context.Background(), nil, info, okHandler)
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("missing token should yield Unauthenticated, got code=%v err=%v", status.Code(err), err)
	}
}

func TestRequireAuthUnaryInterceptor_RejectsInvalidBearer(t *testing.T) {
	icpt := requireAuthUnaryInterceptor(true, []byte(testSecret), nil)
	md := metadata.Pairs("authorization", "Bearer "+"not-a-real-jwt")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	info := &grpc.UnaryServerInfo{FullMethod: "/bamboo.v1.PolicyService/PutPolicy"}
	_, err := icpt(ctx, nil, info, okHandler)
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("invalid token should yield Unauthenticated, got code=%v err=%v", status.Code(err), err)
	}
}

func TestRequireAuthUnaryInterceptor_AcceptsValidBearer(t *testing.T) {
	secret := []byte(testSecret)
	tok, err := auth.IssueSessionToken(secret, auth.SessionClaims{
		UserID:   uuid.New(),
		TenantID: uuid.New(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueSessionToken: %v", err)
	}

	icpt := requireAuthUnaryInterceptor(true, secret, nil)
	md := metadata.Pairs("authorization", "Bearer "+tok)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	info := &grpc.UnaryServerInfo{FullMethod: "/bamboo.v1.PolicyService/PutPolicy"}
	resp, err := icpt(ctx, nil, info, okHandler)
	if err != nil || resp != "ok" {
		t.Errorf("valid bearer should pass: resp=%v err=%v", resp, err)
	}
}
