// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// TestReRegister_EndpointChangePublishesPeerUpdated: a peer that
// re-registers with a different endpoint list is announced to the tenant
// exactly once (with the usual coalesced PolicyChanged follow-up when
// enforcing); re-registering the same list publishes nothing.
func TestReRegister_EndpointChangePublishesPeerUpdated(t *testing.T) {
	f := startFixture(t)
	f.coordSrv.SetPeerRefreshWindow(50 * time.Millisecond)
	watcher := registerIngressTestPeer(t, f, "watcher")

	pub := randomPubKey(t)
	register := func(eps []string) {
		t.Helper()
		resp := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
			"hostname":           "mover",
			"wireguardPublicKey": pub,
			"tenantSlug":         f.tenantSlug,
			"endpoints":          eps,
		})
		if resp.status != http.StatusOK {
			t.Fatalf("register: %d %s", resp.status, resp.body)
		}
	}
	register([]string{"198.51.100.1:51820"})
	if _, err := f.admin.Exec(context.Background(),
		`UPDATE peers SET approval_status = 'approved', approved_at = now() WHERE wireguard_public_key = $1`, pub); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := f.coordSrv.BumpPolicyRevision(context.Background(), fixtureTenantID(t, f)); err != nil {
		t.Fatalf("bump: %v", err)
	}

	ch, cancel, err := f.coordSrv.SubscribePeer(context.Background(), watcher.id)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()

	count := func(evs []*bamboov1.WatchPeersEvent) (updated, policy int) {
		for _, e := range evs {
			if e.GetPeerUpdated() != nil {
				updated++
			}
			if e.GetPolicyChanged() != nil {
				policy++
			}
		}
		return
	}

	register([]string{"203.0.113.7:51820"})
	updated, policy := count(nextPeerEvents(ch, 400*time.Millisecond))
	if updated != 1 || policy != 1 {
		t.Errorf("changed endpoints: PeerUpdated=%d PolicyChanged=%d, want 1 and 1", updated, policy)
	}

	register([]string{"203.0.113.7:51820"})
	updated, policy = count(nextPeerEvents(ch, 400*time.Millisecond))
	if updated != 0 || policy != 0 {
		t.Errorf("same endpoints: PeerUpdated=%d PolicyChanged=%d, want 0 and 0", updated, policy)
	}
}
