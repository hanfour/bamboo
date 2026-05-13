// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"
	"time"

	clientsync "github.com/hanfour/bamboo/clients/cli/internal/sync"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/metadata"
)

// fakeCoord captures the ctx passed by callers so the test can assert
// on the gRPC outgoing metadata the authedAdapter injected.
type fakeCoord struct {
	lastCtx context.Context
}

func (f *fakeCoord) WatchPeers(ctx context.Context, _ *bamboov1.WatchPeersRequest) (clientsync.WatchStream, error) {
	f.lastCtx = ctx
	return nil, nil
}

func (f *fakeCoord) Heartbeat(ctx context.Context, _ *bamboov1.HeartbeatRequest) (*bamboov1.HeartbeatResponse, error) {
	f.lastCtx = ctx
	return &bamboov1.HeartbeatResponse{}, nil
}

func TestAuthedAdapter_InjectsBearerWhenTokenSet(t *testing.T) {
	fake := &fakeCoord{}
	sess := &peerSession{}
	sess.set("tok-abc", time.Now().Add(time.Hour))
	a := newAuthedAdapter(fake, sess)

	if _, err := a.Heartbeat(context.Background(), &bamboov1.HeartbeatRequest{}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	md, ok := metadata.FromOutgoingContext(fake.lastCtx)
	if !ok {
		t.Fatalf("expected outgoing metadata to be present")
	}
	got := md.Get("authorization")
	if len(got) != 1 || got[0] != "Bearer tok-abc" {
		t.Errorf("authorization header = %v, want [Bearer tok-abc]", got)
	}
}

func TestAuthedAdapter_NoBearerWhenSessionEmpty(t *testing.T) {
	// An older controller (or a Register call that failed to mint)
	// leaves the session empty. The wrapper must not inject an empty
	// "Bearer " header, which would look like a credentialed call to
	// the server's interceptor once the gate lands.
	fake := &fakeCoord{}
	sess := &peerSession{}
	a := newAuthedAdapter(fake, sess)
	if _, err := a.Heartbeat(context.Background(), &bamboov1.HeartbeatRequest{}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if md, ok := metadata.FromOutgoingContext(fake.lastCtx); ok {
		if v := md.Get("authorization"); len(v) > 0 {
			t.Errorf("expected no authorization metadata, got %v", v)
		}
	}
}

func TestPeerSession_SetGetRace(t *testing.T) {
	// Smoke test that concurrent set/Token calls don't deadlock or
	// race. The refresh loop in RunWatchPeers writes from a daemon
	// goroutine while the heartbeat loop reads on its own goroutine.
	sess := &peerSession{}
	done := make(chan struct{}, 2)
	go func() {
		for i := 0; i < 200; i++ {
			sess.set("token", time.Now().Add(time.Hour))
		}
		done <- struct{}{}
	}()
	go func() {
		for i := 0; i < 200; i++ {
			_ = sess.Token()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}
