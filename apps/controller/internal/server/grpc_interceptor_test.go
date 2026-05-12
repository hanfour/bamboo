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
	icpt := requireAuthUnaryInterceptor(false, nil)
	info := &grpc.UnaryServerInfo{FullMethod: "/bamboo.v1.PolicyService/PutPolicy"}
	resp, err := icpt(context.Background(), nil, info, okHandler)
	if err != nil || resp != "ok" {
		t.Errorf("disabled mode must pass through any method: resp=%v err=%v", resp, err)
	}
}

func TestRequireAuthUnaryInterceptor_WhitelistAllowsWithoutToken(t *testing.T) {
	icpt := requireAuthUnaryInterceptor(true, []byte(testSecret))
	for _, fullMethod := range []string{
		"/bamboo.v1.CoordinatorService/Register",
		"/bamboo.v1.CoordinatorService/Heartbeat",
		"/bamboo.v1.CoordinatorService/WatchPeers",
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

func TestRequireAuthUnaryInterceptor_RejectsUnauthenticated(t *testing.T) {
	icpt := requireAuthUnaryInterceptor(true, []byte(testSecret))
	info := &grpc.UnaryServerInfo{FullMethod: "/bamboo.v1.PolicyService/PutPolicy"}
	_, err := icpt(context.Background(), nil, info, okHandler)
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("missing token should yield Unauthenticated, got code=%v err=%v", status.Code(err), err)
	}
}

func TestRequireAuthUnaryInterceptor_RejectsInvalidBearer(t *testing.T) {
	icpt := requireAuthUnaryInterceptor(true, []byte(testSecret))
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

	icpt := requireAuthUnaryInterceptor(true, secret)
	md := metadata.Pairs("authorization", "Bearer "+tok)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	info := &grpc.UnaryServerInfo{FullMethod: "/bamboo.v1.PolicyService/PutPolicy"}
	resp, err := icpt(ctx, nil, info, okHandler)
	if err != nil || resp != "ok" {
		t.Errorf("valid bearer should pass: resp=%v err=%v", resp, err)
	}
}
