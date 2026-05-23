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

// TestRESTPeerConnectionEvents_HeartbeatProducesTimelineRows is the
// end-to-end seam test for the #138 v2 wire: a sequence of heartbeats
// with different connectionPath values must produce one row per
// transition in connection_events, and the REST read endpoint must
// surface them with prev_path filled in correctly. Without this the
// repo / clickhouse / handler tests pass in isolation while the glue
// between them silently drifts.
//
// Asserts:
//   - First non-empty path observed (NULL → direct) → one row,
//     prev_path empty.
//   - Same path repeated with new latency → no new row (RTT flicker
//     filter at the repo layer).
//   - Real transition (direct → relay) → one new row, prev_path = "direct".
//   - Newest-first ordering at the REST layer.
func TestRESTPeerConnectionEvents_HeartbeatProducesTimelineRows(t *testing.T) {
	f := startCHFixture(t)

	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "ce-flow",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	peerID := mustField(t, reg.body, "self.id")

	// Three heartbeats: NULL→direct, direct→direct (latency-only,
	// no row), direct→relay.
	for _, hb := range []map[string]any{
		{"peerId": peerID, "knownPolicyRevision": int64(0), "connectionPath": "direct", "latencyMs": int32(10)},
		{"peerId": peerID, "knownPolicyRevision": int64(0), "connectionPath": "direct", "latencyMs": int32(25)},
		{"peerId": peerID, "knownPolicyRevision": int64(0), "connectionPath": "relay", "latencyMs": int32(40)},
	} {
		r := postJSON(t, f.httpURL+"/api/v1/peers/heartbeat", hb)
		if r.status != http.StatusOK {
			t.Fatalf("heartbeat path=%v: status=%d body=%s", hb["connectionPath"], r.status, r.body)
		}
	}

	resp := getJSON(t, f.httpURL+"/api/v1/peers/"+peerID+"/connection-events", f.tenantSlug)
	if resp.status != http.StatusOK {
		t.Fatalf("connection-events: status=%d body=%s", resp.status, resp.body)
	}
	var got struct {
		Events []struct {
			EventType string `json:"eventType"`
			Path      string `json:"path"`
			PrevPath  string `json:"prevPath"`
			RTTMs     uint32 `json:"rttMs"`
		} `json:"events"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, resp.body)
	}
	if len(got.Events) != 2 {
		t.Fatalf("events = %d, want 2 (NULL→direct + direct→relay; latency-only must not log a row); rows=%+v", len(got.Events), got.Events)
	}
	// Newest-first: index 0 is the direct→relay flip.
	if got.Events[0].Path != "relay" || got.Events[0].PrevPath != "direct" {
		t.Errorf("events[0] = %+v, want path=relay prevPath=direct", got.Events[0])
	}
	if got.Events[0].EventType != "path_change" {
		t.Errorf("events[0].EventType = %q, want path_change", got.Events[0].EventType)
	}
	if got.Events[0].RTTMs != 40 {
		t.Errorf("events[0].RTTMs = %d, want 40 (transition heartbeat reported latencyMs=40)", got.Events[0].RTTMs)
	}
	// Index 1 is the NULL → direct first observation.
	if got.Events[1].Path != "direct" || got.Events[1].PrevPath != "" {
		t.Errorf("events[1] = %+v, want path=direct prevPath=\"\"", got.Events[1])
	}
}

// TestRESTPeerConnectionEvents_NegativeLatencyClampedToZero pins the
// uint32 cast guard: a buggy client reporting negative latencyMs must
// not wrap into ~4 billion ms in CH. The guard in logPathTransition
// zeros it instead.
func TestRESTPeerConnectionEvents_NegativeLatencyClampedToZero(t *testing.T) {
	f := startCHFixture(t)

	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "ce-neg-latency",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	peerID := mustField(t, reg.body, "self.id")

	hb := postJSON(t, f.httpURL+"/api/v1/peers/heartbeat", map[string]any{
		"peerId":              peerID,
		"knownPolicyRevision": int64(0),
		"connectionPath":      "direct",
		"latencyMs":           int32(-50),
	})
	if hb.status != http.StatusOK {
		t.Fatalf("heartbeat: status=%d body=%s", hb.status, hb.body)
	}

	resp := getJSON(t, f.httpURL+"/api/v1/peers/"+peerID+"/connection-events", f.tenantSlug)
	if resp.status != http.StatusOK {
		t.Fatalf("connection-events: status=%d body=%s", resp.status, resp.body)
	}
	var got struct {
		Events []struct {
			RTTMs uint32 `json:"rttMs"`
		} `json:"events"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, resp.body)
	}
	if len(got.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(got.Events))
	}
	if got.Events[0].RTTMs != 0 {
		t.Errorf("RTTMs = %d, want 0 (negative latency must be clamped, not unsigned-wrapped)", got.Events[0].RTTMs)
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
