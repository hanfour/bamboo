// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRegister_FirstPeerGetsBaseIP(t *testing.T) {
	f := startFixture(t)

	ctx := f.outgoingCtx(context.Background())
	resp, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Hostname:           "alpha",
		WireguardPublicKey: randomPubKey(t),
		Os:                 "linux",
		ClientVersion:      "test",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if resp.GetSelf().GetIp() != "100.64.0.1" {
		t.Errorf("self.ip = %q, want 100.64.0.1", resp.GetSelf().GetIp())
	}
	if got := len(resp.GetPeers()); got != 0 {
		t.Errorf("first peer should see 0 others, got %d", got)
	}
}

func TestRegister_SecondPeerSeesFirst(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())

	first, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Hostname: "alpha", WireguardPublicKey: randomPubKey(t), Os: "linux",
	})
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}

	second, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Hostname: "beta", WireguardPublicKey: randomPubKey(t), Os: "linux",
	})
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}

	if second.GetSelf().GetIp() != "100.64.0.2" {
		t.Errorf("second peer ip = %q, want 100.64.0.2", second.GetSelf().GetIp())
	}
	if got := len(second.GetPeers()); got != 1 {
		t.Fatalf("second peer should see 1 other, got %d", got)
	}
	if second.GetPeers()[0].GetId() != first.GetSelf().GetId() {
		t.Errorf("peer set first id = %s, want %s",
			second.GetPeers()[0].GetId(), first.GetSelf().GetId())
	}
}

func TestRegister_IdempotentOnSameKey(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())
	pubKey := randomPubKey(t)

	first, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Hostname: "alpha", WireguardPublicKey: pubKey,
	})
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	second, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Hostname: "alpha", WireguardPublicKey: pubKey,
	})
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}
	if first.GetSelf().GetId() != second.GetSelf().GetId() {
		t.Errorf("ids differ: %s vs %s", first.GetSelf().GetId(), second.GetSelf().GetId())
	}
	if first.GetSelf().GetIp() != second.GetSelf().GetIp() {
		t.Errorf("ips differ: %s vs %s", first.GetSelf().GetIp(), second.GetSelf().GetIp())
	}
}

func TestRegister_RejectsMissingFields(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())

	cases := []struct {
		name string
		req  *bamboov1.RegisterRequest
	}{
		{"missing pubkey", &bamboov1.RegisterRequest{Hostname: "x"}},
		{"missing hostname", &bamboov1.RegisterRequest{WireguardPublicKey: randomPubKey(t)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.coord.Register(ctx, tc.req)
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("err code = %v, want InvalidArgument", status.Code(err))
			}
		})
	}
}

func TestHeartbeat_UpdatesLastSeen(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())

	resp, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Hostname: "alpha", WireguardPublicKey: randomPubKey(t),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := f.coord.Heartbeat(ctx, &bamboov1.HeartbeatRequest{
		PeerId: resp.GetSelf().GetId(),
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	var lastSeen *time.Time
	row := f.pool.QueryRow(context.Background(),
		`SELECT last_seen_at FROM peers WHERE id = $1`, resp.GetSelf().GetId())
	if err := row.Scan(&lastSeen); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if lastSeen == nil {
		t.Fatal("last_seen_at not set after heartbeat")
	}
	if delta := time.Since(*lastSeen); delta > 5*time.Second || delta < 0 {
		t.Errorf("last_seen_at delta = %v, want < 5s", delta)
	}
}

func TestHeartbeat_NotFound(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())

	_, err := f.coord.Heartbeat(ctx, &bamboov1.HeartbeatRequest{
		PeerId: "00000000-0000-0000-0000-000000000000",
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("err code = %v, want NotFound", status.Code(err))
	}
}

func TestWatchPeers_ReceivesPeerAdded(t *testing.T) {
	f := startFixture(t)

	// Watcher: peer 1 registers and opens a stream.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mctx := f.outgoingCtx(ctx)

	first, err := f.coord.Register(mctx, &bamboov1.RegisterRequest{
		Hostname: "alpha", WireguardPublicKey: randomPubKey(t),
	})
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}

	stream, err := f.coord.WatchPeers(mctx, &bamboov1.WatchPeersRequest{
		PeerId: first.GetSelf().GetId(),
	})
	if err != nil {
		t.Fatalf("WatchPeers: %v", err)
	}

	// Give the subscribe goroutine a moment to register before we publish.
	time.Sleep(100 * time.Millisecond)

	// Trigger: peer 2 registers, which should publish a PeerAdded event.
	second, err := f.coord.Register(mctx, &bamboov1.RegisterRequest{
		Hostname: "beta", WireguardPublicKey: randomPubKey(t),
	})
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}

	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("stream.Recv: %v", err)
	}
	added := event.GetPeerAdded()
	if added == nil {
		t.Fatalf("expected PeerAdded event, got %T", event.GetEvent())
	}
	if added.GetPeer().GetId() != second.GetSelf().GetId() {
		t.Errorf("event peer id = %s, want %s",
			added.GetPeer().GetId(), second.GetSelf().GetId())
	}
}

func TestWatchPeers_RejectsMissingPeerID(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())

	stream, err := f.coord.WatchPeers(ctx, &bamboov1.WatchPeersRequest{})
	if err != nil {
		// Some gRPC servers report this on Recv; check both paths.
		if status.Code(err) == codes.InvalidArgument {
			return
		}
		t.Fatalf("WatchPeers: %v", err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("err code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestWatchPeers_ContextCancelEndsStream(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())

	resp, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Hostname: "alpha", WireguardPublicKey: randomPubKey(t),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	streamCtx, streamCancel := context.WithCancel(ctx)
	stream, err := f.coord.WatchPeers(streamCtx, &bamboov1.WatchPeersRequest{
		PeerId: resp.GetSelf().GetId(),
	})
	if err != nil {
		t.Fatalf("WatchPeers: %v", err)
	}

	// Cancel after a brief pause.
	go func() {
		time.Sleep(100 * time.Millisecond)
		streamCancel()
	}()

	_, err = stream.Recv()
	if err == nil {
		t.Error("expected stream to end with error after cancel")
	}
	// Cancel manifests as either io.EOF (if server handled it via ctx.Done)
	// or context.Canceled / Unavailable on the client side.
	if !errors.Is(err, io.EOF) &&
		status.Code(err) != codes.Canceled &&
		status.Code(err) != codes.Unavailable {
		t.Errorf("unexpected stream end error: %v (code %v)", err, status.Code(err))
	}
}

func TestRegister_AllowedIpsReflectsACL(t *testing.T) {
	f := startFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mctx := f.outgoingCtx(ctx)

	// Two peers: A claims tag "dev", B claims tag "db".
	devPubKey := randomPubKey(t)
	dbPubKey := randomPubKey(t)

	devResp, err := f.coord.Register(mctx, &bamboov1.RegisterRequest{
		Hostname: "dev-host", WireguardPublicKey: devPubKey,
	})
	if err != nil {
		t.Fatalf("Register dev: %v", err)
	}
	dbResp, err := f.coord.Register(mctx, &bamboov1.RegisterRequest{
		Hostname: "db-host", WireguardPublicKey: dbPubKey,
	})
	if err != nil {
		t.Fatalf("Register db: %v", err)
	}

	devID, err := uuid.Parse(devResp.GetSelf().GetId())
	if err != nil {
		t.Fatalf("parse dev id: %v", err)
	}
	dbID, err := uuid.Parse(dbResp.GetSelf().GetId())
	if err != nil {
		t.Fatalf("parse db id: %v", err)
	}

	peers := repo.NewPeers(f.pool)
	if _, err := peers.SetTags(ctx, devID, []string{"dev"}); err != nil {
		t.Fatalf("SetTags dev: %v", err)
	}
	if _, err := peers.SetTags(ctx, dbID, []string{"db"}); err != nil {
		t.Fatalf("SetTags db: %v", err)
	}

	// Author a policy: dev may reach db; the implicit default-deny
	// covers everything else, including db → dev.
	putResp, err := f.policy.PutPolicy(mctx, &bamboov1.PutPolicyRequest{
		HclSource: `rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:*"]
}`,
	})
	if err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	wantRev := putResp.GetPolicy().GetRevision()
	if wantRev <= 0 {
		t.Fatalf("revision = %d, want > 0", wantRev)
	}

	// Re-register dev (idempotent on pubkey) — response should now
	// carry policy-driven AllowedIps for the db peer.
	devRefreshed, err := f.coord.Register(mctx, &bamboov1.RegisterRequest{
		Hostname: "dev-host", WireguardPublicKey: devPubKey,
	})
	if err != nil {
		t.Fatalf("dev re-Register: %v", err)
	}
	if got := devRefreshed.GetPolicyRevision(); got != wantRev {
		t.Errorf("dev sees PolicyRevision = %d, want %d", got, wantRev)
	}
	var dbViewFromDev *bamboov1.Peer
	for _, p := range devRefreshed.GetPeers() {
		if p.GetId() == dbResp.GetSelf().GetId() {
			dbViewFromDev = p
			break
		}
	}
	if dbViewFromDev == nil {
		t.Fatalf("dev's peer list missing db peer")
	}
	if len(dbViewFromDev.GetAllowedIps()) == 0 {
		t.Errorf("dev → db should be allowed: AllowedIps = %v, want non-empty", dbViewFromDev.GetAllowedIps())
	}
	wantCIDR := dbResp.GetSelf().GetIp() + "/32"
	if len(dbViewFromDev.GetAllowedIps()) > 0 && dbViewFromDev.GetAllowedIps()[0] != wantCIDR {
		t.Errorf("AllowedIps[0] = %q, want %q", dbViewFromDev.GetAllowedIps()[0], wantCIDR)
	}

	// Re-register db — policy denies db → dev, so AllowedIps for the
	// dev peer in db's view should be empty.
	dbRefreshed, err := f.coord.Register(mctx, &bamboov1.RegisterRequest{
		Hostname: "db-host", WireguardPublicKey: dbPubKey,
	})
	if err != nil {
		t.Fatalf("db re-Register: %v", err)
	}
	var devViewFromDB *bamboov1.Peer
	for _, p := range dbRefreshed.GetPeers() {
		if p.GetId() == devResp.GetSelf().GetId() {
			devViewFromDB = p
			break
		}
	}
	if devViewFromDB == nil {
		t.Fatalf("db's peer list missing dev peer")
	}
	if len(devViewFromDB.GetAllowedIps()) != 0 {
		t.Errorf("db → dev should be denied: AllowedIps = %v, want empty", devViewFromDB.GetAllowedIps())
	}
}

func TestWatchPeers_ReceivesPolicyChanged(t *testing.T) {
	f := startFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mctx := f.outgoingCtx(ctx)

	self, err := f.coord.Register(mctx, &bamboov1.RegisterRequest{
		Hostname: "watcher", WireguardPublicKey: randomPubKey(t),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	stream, err := f.coord.WatchPeers(mctx, &bamboov1.WatchPeersRequest{
		PeerId: self.GetSelf().GetId(),
	})
	if err != nil {
		t.Fatalf("WatchPeers: %v", err)
	}

	// Give the subscribe goroutine a moment to register before we publish.
	time.Sleep(100 * time.Millisecond)

	resp, err := f.policy.PutPolicy(mctx, &bamboov1.PutPolicyRequest{
		HclSource: `rule "allow-all" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`,
	})
	if err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}

	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("stream.Recv: %v", err)
	}
	pc := event.GetPolicyChanged()
	if pc == nil {
		t.Fatalf("expected PolicyChanged event, got %T", event.GetEvent())
	}
	if pc.GetPolicyRevision() != resp.GetPolicy().GetRevision() {
		t.Errorf("event revision = %d, want %d",
			pc.GetPolicyRevision(), resp.GetPolicy().GetRevision())
	}
}
