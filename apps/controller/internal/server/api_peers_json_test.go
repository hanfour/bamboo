// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// TestPeerToJSON_SurfacesIP6 is the regression for the admin peers endpoint
// dropping the peer's overlay IPv6 (NAT64 Phase A). repo.Peer carries IP6 and
// every DB read path SELECTs host(peers.ip6) into it, but apiPeerJSON had no
// ip6 field so peerToJSON silently discarded it — the admin API and Web never
// showed a peer's ip6. The address must survive to the JSON wire.
func TestPeerToJSON_SurfacesIP6(t *testing.T) {
	const wantIP6 = "fdba:1100::6440:7"
	p := &repo.Peer{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		IP:       "100.127.0.5",
		IP6:      wantIP6,
	}

	b, err := json.Marshal(peerToJSON(p))
	if err != nil {
		t.Fatalf("marshal peerToJSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := m["ip6"].(string); got != wantIP6 {
		t.Errorf("peers JSON ip6 = %q, want %q; full=%s", got, wantIP6, b)
	}
}
