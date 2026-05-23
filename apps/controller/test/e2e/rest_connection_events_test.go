// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestRESTPeerConnectionEvents_404ForUnknownPeer pins the tenant-
// scoping contract for the #138 v2 timeline endpoint: probing a
// random UUID collapses to 404 BEFORE the ClickHouse query runs, so
// a malicious caller can't fish for events in another tenant's data.
func TestRESTPeerConnectionEvents_404ForUnknownPeer(t *testing.T) {
	f := startFixture(t)

	resp := getJSON(t, f.httpURL+"/api/v1/peers/00000000-0000-0000-0000-000000000099/connection-events", f.tenantSlug)
	if resp.status != http.StatusNotFound {
		t.Errorf("connection-events for missing peer: status=%d, want 404; body=%s", resp.status, resp.body)
	}
}

// TestRESTPeerConnectionEvents_EmptyWhenCHUnavailable verifies the
// graceful-degrade contract: the e2e fixture wires ClickHouse as nil,
// so ListByPeer returns no rows. The endpoint must respond 200 with
// an empty events list, not a 500. This keeps self-hosted small
// deploys (no CH cluster) from breaking the PeerDrawer UI.
func TestRESTPeerConnectionEvents_EmptyWhenCHUnavailable(t *testing.T) {
	f := startFixture(t)

	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "ce-empty",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	peerID := mustField(t, reg.body, "self.id")

	resp := getJSON(t, f.httpURL+"/api/v1/peers/"+peerID+"/connection-events", f.tenantSlug)
	if resp.status != http.StatusOK {
		t.Fatalf("connection-events: status=%d body=%s", resp.status, resp.body)
	}
	var got struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, resp.body)
	}
	if len(got.Events) != 0 {
		t.Errorf("events = %v, want empty when CH not configured", got.Events)
	}
}

// TestRESTPeerConnectionEvents_RejectsCrossTenant verifies a peer in
// tenant A cannot be queried via tenant B's slug — the lookup short-
// circuits to 404 even though the UUID exists.
func TestRESTPeerConnectionEvents_RejectsCrossTenant(t *testing.T) {
	f := startFixture(t)

	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "ce-tenant-a",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	peerID := mustField(t, reg.body, "self.id")

	// Probe with a tenant slug that doesn't match.
	resp := getJSON(t, f.httpURL+"/api/v1/peers/"+peerID+"/connection-events", "other-tenant")
	if resp.status != http.StatusNotFound {
		t.Errorf("cross-tenant connection-events: status=%d, want 404; body=%s", resp.status, resp.body)
	}
}
