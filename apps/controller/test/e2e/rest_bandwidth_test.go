// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestRESTPeerBandwidth_HeartbeatWritesSampleAndEndpointReturnsIt
// is the §4 P2 seam test: a heartbeat that reports non-zero
// bytes lands as a connection_events bandwidth_sample row, and
// the new /bandwidth read endpoint surfaces it with the correct
// oldest-first ordering. Without this the wire field and the
// read endpoint could each work in isolation while the glue
// between them silently regresses.
func TestRESTPeerBandwidth_HeartbeatWritesSampleAndEndpointReturnsIt(t *testing.T) {
	f := startCHFixture(t)

	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "bw-flow",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	peerID := mustField(t, reg.body, "self.id")

	// Three heartbeats with cumulative byte counters that grow
	// monotonically — the typical wg-counter pattern. The
	// controller's job is to persist them as-is; delta
	// computation is the reader's responsibility.
	for _, hb := range []map[string]any{
		{"peerId": peerID, "knownPolicyRevision": int64(0), "connectionPath": "direct", "bytesSent": 1000, "bytesReceived": 500},
		{"peerId": peerID, "knownPolicyRevision": int64(0), "connectionPath": "direct", "bytesSent": 2500, "bytesReceived": 1200},
		{"peerId": peerID, "knownPolicyRevision": int64(0), "connectionPath": "relay", "bytesSent": 4000, "bytesReceived": 2000},
	} {
		r := postJSON(t, f.httpURL+"/api/v1/peers/heartbeat", hb)
		if r.status != http.StatusOK {
			t.Fatalf("heartbeat: status=%d body=%s", r.status, r.body)
		}
	}

	resp := getJSON(t, f.httpURL+"/api/v1/peers/"+peerID+"/bandwidth", f.tenantSlug)
	if resp.status != http.StatusOK {
		t.Fatalf("bandwidth: status=%d body=%s", resp.status, resp.body)
	}
	var got struct {
		Samples []struct {
			Path          string `json:"path"`
			BytesSent     uint64 `json:"bytesSent"`
			BytesReceived uint64 `json:"bytesReceived"`
		} `json:"samples"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, resp.body)
	}
	if len(got.Samples) != 3 {
		t.Fatalf("samples = %d, want 3; rows=%+v", len(got.Samples), got.Samples)
	}
	// Oldest-first: bytesSent must increase. Path on the third
	// sample switches to "relay" — verifies the path field
	// round-trips through CH.
	wantSent := []uint64{1000, 2500, 4000}
	for i, s := range got.Samples {
		if s.BytesSent != wantSent[i] {
			t.Errorf("samples[%d].BytesSent = %d, want %d", i, s.BytesSent, wantSent[i])
		}
	}
	if got.Samples[2].Path != "relay" {
		t.Errorf("samples[2].Path = %q, want relay", got.Samples[2].Path)
	}
}

// TestRESTPeerBandwidth_ZeroByteHeartbeatNotPersisted pins the
// noise-suppression contract: a heartbeat with both byte counters
// at zero (e.g. fresh wg interface, no traffic) does NOT land a
// row. Without this the time series would be flooded with
// zero-baseline samples that the chart has to filter anyway.
func TestRESTPeerBandwidth_ZeroByteHeartbeatNotPersisted(t *testing.T) {
	f := startCHFixture(t)

	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "bw-zero",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	peerID := mustField(t, reg.body, "self.id")

	// Five zero-byte heartbeats — none should land.
	for i := 0; i < 5; i++ {
		r := postJSON(t, f.httpURL+"/api/v1/peers/heartbeat", map[string]any{
			"peerId": peerID, "knownPolicyRevision": int64(0),
			"bytesSent": 0, "bytesReceived": 0,
		})
		if r.status != http.StatusOK {
			t.Fatalf("heartbeat[%d]: status=%d body=%s", i, r.status, r.body)
		}
	}

	resp := getJSON(t, f.httpURL+"/api/v1/peers/"+peerID+"/bandwidth", f.tenantSlug)
	var got struct {
		Samples []struct{} `json:"samples"`
	}
	_ = json.Unmarshal(resp.body, &got)
	if len(got.Samples) != 0 {
		t.Errorf("got %d samples for all-zero heartbeats, want 0", len(got.Samples))
	}
}

// TestRESTPeerBandwidth_RejectsCrossTenant pins the tenant-scope
// short-circuit. A peer UUID owned by another tenant resolves
// to 404 before any CH query runs.
func TestRESTPeerBandwidth_RejectsCrossTenant(t *testing.T) {
	f := startFixture(t)

	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "bw-tenant-a",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	peerID := mustField(t, reg.body, "self.id")

	resp := getJSON(t, f.httpURL+"/api/v1/peers/"+peerID+"/bandwidth", "wrong-tenant")
	if resp.status != http.StatusNotFound {
		t.Errorf("cross-tenant bandwidth: status=%d, want 404", resp.status)
	}
}
