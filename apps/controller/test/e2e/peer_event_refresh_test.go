// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// nextPeerEvents collects events from ch for d.
func nextPeerEvents(ch <-chan *bamboov1.WatchPeersEvent, d time.Duration) []*bamboov1.WatchPeersEvent {
	var out []*bamboov1.WatchPeersEvent
	timeout := time.After(d)
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		case <-timeout:
			return out
		}
	}
}

// policyChangedAfter reports whether a PolicyChanged follows the first
// event matching want.
func policyChangedAfter(evs []*bamboov1.WatchPeersEvent, want func(*bamboov1.WatchPeersEvent) bool) (sawEvent, sawPolicy bool) {
	for _, e := range evs {
		if !sawEvent {
			sawEvent = want(e)
			continue
		}
		if e.GetPolicyChanged() != nil {
			return true, true
		}
	}
	return sawEvent, false
}

func fixtureTenantID(t *testing.T, f *fixture) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.admin.QueryRow(context.Background(), `SELECT id FROM tenants WHERE slug = $1`, f.tenantSlug).Scan(&id); err != nil {
		t.Fatalf("tenant id: %v", err)
	}
	return id
}

// TestPeerEvents_PolicyChangedFollowsWhenEnforcing: PeerAdded and
// PeerUpdated carry no AllowedIps, which enforcing clients (revision > 0)
// read as "deny". The controller follows each with PolicyChanged so they
// re-register; at revision 0 nothing extra is published.
func TestPeerEvents_PolicyChangedFollowsWhenEnforcing(t *testing.T) {
	isAdded := func(e *bamboov1.WatchPeersEvent) bool { return e.GetPeerAdded() != nil }
	isUpdated := func(e *bamboov1.WatchPeersEvent) bool { return e.GetPeerUpdated() != nil }
	for _, enforcing := range []bool{false, true} {
		name := "rev0"
		if enforcing {
			name = "rev>0"
		}
		t.Run(name, func(t *testing.T) {
			f := startFixture(t)
			watcher := registerIngressTestPeer(t, f, "watcher")
			if enforcing {
				if _, err := f.coordSrv.BumpPolicyRevision(context.Background(), fixtureTenantID(t, f)); err != nil {
					t.Fatalf("bump: %v", err)
				}
			}
			ch, cancel, err := f.coordSrv.SubscribePeer(context.Background(), watcher.id)
			if err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			defer cancel()

			newbie := registerIngressTestPeer(t, f, "newbie") // auto-approved ⇒ PeerAdded
			saw, policy := policyChangedAfter(nextPeerEvents(ch, 300*time.Millisecond), isAdded)
			if !saw {
				t.Fatal("no PeerAdded for the new peer")
			}
			if policy != enforcing {
				t.Errorf("PeerAdded: PolicyChanged followed=%v, want %v", policy, enforcing)
			}

			if r := ingressCall(t, http.MethodPatch, f.httpURL+"/api/v1/peers/"+newbie.id, "", f.tenantSlug,
				map[string]any{"hostname": "renamed"}); r.status != http.StatusOK {
				t.Fatalf("patch: %d %s", r.status, r.body)
			}
			saw, policy = policyChangedAfter(nextPeerEvents(ch, 300*time.Millisecond), isUpdated)
			if !saw {
				t.Fatal("no PeerUpdated for the rename")
			}
			if policy != enforcing {
				t.Errorf("PeerUpdated: PolicyChanged followed=%v, want %v", policy, enforcing)
			}
		})
	}
}
