// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// TestNAT64EgressHealth_HeartbeatPersists proves the REST heartbeat
// nat64EgressHealthy side-channel reaches the new peer columns: false →
// unhealthy/'translator down', true → healthy/”, and a heartbeat WITHOUT
// the field leaves the columns unchanged (a non-egress / pre-C3 CLI).
func TestNAT64EgressHealth_HeartbeatPersists(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())
	bg := context.Background()
	peers := repo.NewPeers(f.pool)

	reg, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Hostname: "egress", WireguardPublicKey: randomPubKey(t), Os: "linux",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	id := uuid.MustParse(reg.GetSelf().GetId())

	hb := func(payload map[string]any) {
		t.Helper()
		resp := postJSON(t, f.httpURL+"/api/v1/peers/heartbeat", payload)
		if resp.status != http.StatusOK {
			t.Fatalf("heartbeat status=%d body=%s", resp.status, resp.body)
		}
	}
	statusReason := func() (string, string) {
		t.Helper()
		p, err := peers.GetByID(bg, id)
		if err != nil {
			t.Fatal(err)
		}
		var s, r string
		if p.NAT64EgressHealthStatus != nil {
			s = *p.NAT64EgressHealthStatus
		}
		if p.NAT64EgressHealthReason != nil {
			r = *p.NAT64EgressHealthReason
		}
		return s, r
	}

	// Report unhealthy.
	hb(map[string]any{"peerId": id.String(), "nat64EgressHealthy": false})
	if s, r := statusReason(); s != "unhealthy" || r != "translator down" {
		t.Errorf("after false: status=%q reason=%q, want unhealthy/'translator down'", s, r)
	}

	// Report healthy.
	hb(map[string]any{"peerId": id.String(), "nat64EgressHealthy": true})
	if s, r := statusReason(); s != "healthy" || r != "" {
		t.Errorf("after true: status=%q reason=%q, want healthy/''", s, r)
	}

	// A heartbeat WITHOUT the field leaves the columns unchanged.
	hb(map[string]any{"peerId": id.String()})
	if s, _ := statusReason(); s != "healthy" {
		t.Errorf("after fieldless heartbeat: status=%q, want unchanged 'healthy'", s)
	}
}
