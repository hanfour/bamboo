// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/operate"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestServeOperateRequiresBoundPeerSession pins who may stand in for a
// device on the desk stream. Admins send files and terminal keystrokes
// down this stream and trust what comes back, so only the device's own
// peer-session token may attach. A user-session JWT from the same
// tenant, a token bound to another peer, or no bearer at all must not
// replace the real device's link.
func TestServeOperateRequiresBoundPeerSession(t *testing.T) {
	secret := []byte("test-secret-with-at-least-32-bytes-padding")
	tenant := uuid.New()
	peerA, peerB := uuid.New(), uuid.New()

	peerTokA, err := auth.IssuePeerSessionToken(secret, auth.PeerSessionClaims{TenantID: tenant, PeerID: peerA}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	peerTokB, err := auth.IssuePeerSessionToken(secret, auth.PeerSessionClaims{TenantID: tenant, PeerID: peerB}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	userTok, err := auth.IssueSessionToken(secret, auth.SessionClaims{UserID: uuid.New(), TenantID: tenant}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		ctx  context.Context
		want codes.Code
	}{
		{"no bearer", context.Background(), codes.Unauthenticated},
		{"user session in same tenant", incomingBearer(userTok), codes.PermissionDenied},
		{"peer session bound to another peer", incomingBearer(peerTokB), codes.PermissionDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &CoordinatorHandler{auth: &AuthHandler{sessionSec: secret}, ops: operate.NewHub()}
			ctx, cancel := context.WithCancel(tc.ctx)
			defer cancel()
			stream := newFakeOperate(ctx)
			stream.in <- operateHello(peerA.String())
			done := make(chan error, 1)
			go func() { done <- h.serveOperate(stream) }()
			select {
			case err := <-done:
				if status.Code(err) != tc.want {
					t.Fatalf("code=%v err=%v, want %v", status.Code(err), err, tc.want)
				}
			case <-time.After(time.Second):
				_, online := h.ops.ExecEnabled(peerA.String())
				t.Fatalf("stream was accepted (attached as the device: %v), want %v", online, tc.want)
			}
		})
	}

	t.Run("own peer session attaches", func(t *testing.T) {
		h := &CoordinatorHandler{auth: &AuthHandler{sessionSec: secret}, ops: operate.NewHub()}
		ctx, cancel := context.WithCancel(incomingBearer(peerTokA))
		defer cancel()
		stream := newFakeOperate(ctx)
		done := make(chan error, 1)
		go func() { done <- h.serveOperate(stream) }()
		stream.in <- operateHello(peerA.String())
		deadline := time.Now().Add(time.Second)
		for {
			if _, online := h.ops.ExecEnabled(peerA.String()); online {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("bound device did not attach")
			}
			time.Sleep(time.Millisecond)
		}
		close(stream.in)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func operateHello(peerID string) *bamboov1.DeviceOperateFrame {
	return &bamboov1.DeviceOperateFrame{Frame: &bamboov1.DeviceOperateFrame_Hello{Hello: &bamboov1.DeviceOperateHello{PeerId: peerID}}}
}
