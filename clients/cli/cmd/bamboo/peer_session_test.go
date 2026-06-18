// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	clientsync "github.com/hanfour/bamboo/clients/cli/internal/sync"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/metadata"
)

// TestRestRegister_ParsesNAT64Fields is the regression for the hardware-E2E
// bug: the REST register response carries the tenant's NAT64 config + the
// egress activation signal, but restRegisterResponse didn't decode them, so
// they never reached the bamboov1.RegisterResponse that reconcileEgress reads
// — a CLI egress always saw active=false and never stood up Tayga.
func TestRestRegister_ParsesNAT64Fields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/peers/register" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"self": {"id":"p1","tenantId":"t1","hostname":"egress","ip":"100.64.0.2","wireguardPublicKey":"k"},
			"peers": [],
			"policyRevision": 0,
			"dns64Enabled": true,
			"nat64Prefix": "64:ff9b::/96",
			"nat64EgressActive": true
		}`))
	}))
	defer srv.Close()
	t.Setenv("BAMBOO_CONTROLLER_HTTP_URL", srv.URL)

	resp, _, _, err := restRegister(context.Background(),
		"egress", "k", "linux", "vtest", "bka_test", "default",
		nil, nil, false, true)
	if err != nil {
		t.Fatalf("restRegister: %v", err)
	}
	if !resp.GetDns64Enabled() {
		t.Errorf("Dns64Enabled = false, want true")
	}
	if got := resp.GetNat64Prefix(); got != "64:ff9b::/96" {
		t.Errorf("Nat64Prefix = %q, want 64:ff9b::/96", got)
	}
	if !resp.GetNat64EgressActive() {
		t.Errorf("Nat64EgressActive = false, want true (egress would never build Tayga)")
	}
}

// fakeCoord captures the ctx passed by callers so the test can assert
// on the gRPC outgoing metadata the authedAdapter injected. Only
// WatchPeers lives on the gRPC adapter now — heartbeat moved to REST
// for the §4 P2 bandwidth metering side-channel.
type fakeCoord struct {
	lastCtx context.Context
}

func (f *fakeCoord) WatchPeers(ctx context.Context, _ *bamboov1.WatchPeersRequest) (clientsync.WatchStream, error) {
	f.lastCtx = ctx
	return nil, nil
}

func TestAuthedAdapter_InjectsBearerWhenTokenSet(t *testing.T) {
	fake := &fakeCoord{}
	sess := &peerSession{}
	sess.set("tok-abc", time.Now().Add(time.Hour))
	a := newAuthedAdapter(fake, sess)

	if _, err := a.WatchPeers(context.Background(), &bamboov1.WatchPeersRequest{}); err != nil {
		t.Fatalf("watch: %v", err)
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
	if _, err := a.WatchPeers(context.Background(), &bamboov1.WatchPeersRequest{}); err != nil {
		t.Fatalf("watch: %v", err)
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
