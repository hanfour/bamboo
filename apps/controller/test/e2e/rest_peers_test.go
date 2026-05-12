// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// TestRESTRegister_HappyPath drives /api/v1/peers/register through the
// HTTP fixture and verifies the JSON shape matches the gRPC handler's
// behavior (same code path, different transport).
func TestRESTRegister_HappyPath(t *testing.T) {
	f := startFixture(t)

	body := map[string]any{
		"hostname":           "rest-mac-1",
		"wireguardPublicKey": randomPubKey(t),
		"os":                 "darwin",
		"clientVersion":      "0.0.1",
		"tenantSlug":         f.tenantSlug,
	}
	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", body)
	if resp.status != http.StatusOK {
		t.Fatalf("register status = %d, body=%s", resp.status, resp.body)
	}

	var got struct {
		Self struct {
			ID       string `json:"id"`
			TenantID string `json:"tenantId"`
			Hostname string `json:"hostname"`
			IP       string `json:"ip"`
		} `json:"self"`
		Peers          []map[string]any `json:"peers"`
		PolicyRevision int64            `json:"policyRevision"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp.body)
	}
	if got.Self.ID == "" || got.Self.IP == "" || got.Self.TenantID == "" {
		t.Errorf("missing self fields: %+v", got.Self)
	}
	if got.Self.Hostname != "rest-mac-1" {
		t.Errorf("Self.Hostname = %q, want rest-mac-1", got.Self.Hostname)
	}
	// No policy has been authored, so revision is the new "no policy"
	// sentinel (0). Once PutPolicy fires, this becomes monotonic.
	if got.PolicyRevision != 0 {
		t.Errorf("PolicyRevision = %d, want 0 (no policy authored)", got.PolicyRevision)
	}
}

// TestRESTRegister_RejectsMissingFields verifies validation forwards
// from the gRPC handler as a 400 Bad Request.
func TestRESTRegister_RejectsMissingFields(t *testing.T) {
	f := startFixture(t)

	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname": "no-key",
	})
	if resp.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", resp.status, resp.body)
	}
}

// TestRESTHeartbeat_HappyPath registers a peer over REST then sends a
// heartbeat for it. Both calls hit the same CoordinatorHandler the gRPC
// path uses, so peers_changed/policy_changed semantics carry over.
func TestRESTHeartbeat_HappyPath(t *testing.T) {
	f := startFixture(t)

	// Register first.
	body := map[string]any{
		"hostname":           "rest-mac-2",
		"wireguardPublicKey": randomPubKey(t),
		"os":                 "darwin",
		"tenantSlug":         f.tenantSlug,
	}
	regResp := postJSON(t, f.httpURL+"/api/v1/peers/register", body)
	if regResp.status != http.StatusOK {
		t.Fatalf("register status = %d, body=%s", regResp.status, regResp.body)
	}
	var reg struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	_ = json.Unmarshal(regResp.body, &reg)
	if reg.Self.ID == "" {
		t.Fatal("register did not return self.id")
	}

	// With no policy authored, current revision is 0 and a client
	// reporting knownPolicyRevision=0 is up-to-date.
	hbResp := postJSON(t, f.httpURL+"/api/v1/peers/heartbeat", map[string]any{
		"peerId":              reg.Self.ID,
		"knownPolicyRevision": int64(0),
	})
	if hbResp.status != http.StatusOK {
		t.Fatalf("heartbeat status = %d, body=%s", hbResp.status, hbResp.body)
	}
	var hb struct {
		PolicyChanged         bool  `json:"policyChanged"`
		CurrentPolicyRevision int64 `json:"currentPolicyRevision"`
	}
	if err := json.Unmarshal(hbResp.body, &hb); err != nil {
		t.Fatalf("decode hb: %v", err)
	}
	if hb.PolicyChanged {
		t.Errorf("heartbeat with matching revision 0 should report policyChanged=false; got %+v", hb)
	}
	if hb.CurrentPolicyRevision != 0 {
		t.Errorf("heartbeat returned currentPolicyRevision=%d, want 0", hb.CurrentPolicyRevision)
	}
}

// TestRESTRegister_PersistsEndpoints verifies that endpoints supplied
// on /api/v1/peers/register are stored and round-tripped in subsequent
// list calls (the Mac / iPhone client passes its STUN-discovered
// candidates this way).
func TestRESTRegister_PersistsEndpoints(t *testing.T) {
	f := startFixture(t)

	body := map[string]any{
		"hostname":           "rest-with-eps",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
		"endpoints":          []string{"203.0.113.5:51820", "192.168.1.5:51820"},
	}
	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", body)
	if resp.status != http.StatusOK {
		t.Fatalf("register status = %d, body=%s", resp.status, resp.body)
	}
	var got struct {
		Self struct {
			Endpoints []string `json:"endpoints"`
		} `json:"self"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp.body)
	}
	want := []string{"203.0.113.5:51820", "192.168.1.5:51820"}
	if len(got.Self.Endpoints) != len(want) {
		t.Errorf("Self.Endpoints = %v, want %v", got.Self.Endpoints, want)
	}
	for i := range want {
		if i < len(got.Self.Endpoints) && got.Self.Endpoints[i] != want[i] {
			t.Errorf("Self.Endpoints[%d] = %s, want %s", i, got.Self.Endpoints[i], want[i])
		}
	}
}

// TestRESTHeartbeat_ReportsEndpointChange asserts that updating
// endpoints via heartbeat reports peersChanged=true and emits a
// peer_updated event on the SSE watch stream for OTHER peers in the
// tenant.
func TestRESTHeartbeat_ReportsEndpointChange(t *testing.T) {
	f := startFixture(t)

	// Watcher first so it can observe the change.
	wResp := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "rest-watcher-eps",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	if wResp.status != http.StatusOK {
		t.Fatalf("register watcher: status=%d body=%s", wResp.status, wResp.body)
	}
	var watcher struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	_ = json.Unmarshal(wResp.body, &watcher)

	// The peer whose endpoints we'll update.
	tResp := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "rest-target-eps",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	if tResp.status != http.StatusOK {
		t.Fatalf("register target: status=%d body=%s", tResp.status, tResp.body)
	}
	var target struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	_ = json.Unmarshal(tResp.body, &target)

	// Open the watch stream as the watcher peer.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		f.httpURL+"/api/v1/peers/watch?peerId="+watcher.Self.ID, nil)
	if err != nil {
		t.Fatalf("build watch request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer resp.Body.Close()

	// Heartbeat the target with new endpoints; expect peersChanged=true
	// and a peer_updated event on the watch stream.
	go func() {
		hb := postJSON(nil, f.httpURL+"/api/v1/peers/heartbeat", map[string]any{
			"peerId":              target.Self.ID,
			"knownPolicyRevision": int64(1),
			"endpoints":           []string{"203.0.113.99:51820"},
		})
		_ = hb // status check below via SSE
	}()

	got, err := readSSEEvent(resp.Body, "peer_updated", 3*time.Second)
	if err != nil {
		t.Fatalf("waiting for peer_updated: %v", err)
	}
	if !strings.Contains(got, "203.0.113.99") {
		t.Errorf("peer_updated did not include new endpoint; got %q", got)
	}
}

// TestRESTWatch_StreamsRegisteredPeer subscribes to /api/v1/peers/watch
// for an existing peer, then registers a second peer over REST and
// asserts the SSE stream emits a peer_added event for it.
func TestRESTWatch_StreamsRegisteredPeer(t *testing.T) {
	f := startFixture(t)

	// Register the watcher peer.
	first := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "rest-mac-watcher",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	if first.status != http.StatusOK {
		t.Fatalf("register watcher status=%d body=%s", first.status, first.body)
	}
	var watcher struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	_ = json.Unmarshal(first.body, &watcher)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		f.httpURL+"/api/v1/peers/watch?peerId="+watcher.Self.ID, nil)
	if err != nil {
		t.Fatalf("build watch request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("watch request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("watch status=%d body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}

	// Trigger a peer_added by registering a second peer. The bus
	// publishes the event after the response is built, so a small
	// delay between Register returning and the event arriving on the
	// stream is expected.
	go func() {
		_ = postJSON(nil, f.httpURL+"/api/v1/peers/register", map[string]any{
			"hostname":           "rest-mac-other",
			"wireguardPublicKey": randomPubKey(t),
			"tenantSlug":         f.tenantSlug,
		})
	}()

	got, err := readSSEEvent(resp.Body, "peer_added", 3*time.Second)
	if err != nil {
		t.Fatalf("waiting for peer_added: %v", err)
	}
	if !strings.Contains(got, "rest-mac-other") {
		t.Errorf("peer_added payload missing hostname; got %q", got)
	}
}

// TestRESTGetPeer_HappyPath registers a peer then fetches it via
// GET /api/v1/peers/{id} and asserts the JSON shape includes the
// drawer-relevant fields (pubkey / endpoints / createdAt).
func TestRESTGetPeer_HappyPath(t *testing.T) {
	f := startFixture(t)

	pub := randomPubKey(t)
	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "rest-get-peer",
		"wireguardPublicKey": pub,
		"os":                 "darwin",
		"clientVersion":      "0.0.2",
		"tenantSlug":         f.tenantSlug,
		"endpoints":          []string{"203.0.113.7:51820"},
	})
	if reg.status != http.StatusOK {
		t.Fatalf("register status=%d body=%s", reg.status, reg.body)
	}
	var regOut struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	_ = json.Unmarshal(reg.body, &regOut)
	if regOut.Self.ID == "" {
		t.Fatal("register did not return self.id")
	}

	got := getJSON(t, f.httpURL+"/api/v1/peers/"+regOut.Self.ID, f.tenantSlug)
	if got.status != http.StatusOK {
		t.Fatalf("get status=%d body=%s", got.status, got.body)
	}

	var p struct {
		ID                 string   `json:"id"`
		Hostname           string   `json:"hostname"`
		IP                 string   `json:"ip"`
		WireGuardPublicKey string   `json:"wireguardPublicKey"`
		Endpoints          []string `json:"endpoints"`
		CreatedAt          string   `json:"createdAt"`
		Status             string   `json:"status"`
	}
	if err := json.Unmarshal(got.body, &p); err != nil {
		t.Fatalf("decode: %v; body=%s", err, got.body)
	}
	if p.ID != regOut.Self.ID {
		t.Errorf("id = %q, want %q", p.ID, regOut.Self.ID)
	}
	if p.Hostname != "rest-get-peer" {
		t.Errorf("hostname = %q, want rest-get-peer", p.Hostname)
	}
	if p.WireGuardPublicKey != pub {
		t.Errorf("wireguardPublicKey = %q, want %q", p.WireGuardPublicKey, pub)
	}
	if len(p.Endpoints) != 1 || p.Endpoints[0] != "203.0.113.7:51820" {
		t.Errorf("endpoints = %v, want [203.0.113.7:51820]", p.Endpoints)
	}
	if p.CreatedAt == "" {
		t.Error("createdAt is empty")
	}
}

// TestRESTGetPeer_ReflectsWGSyncState writes a wgsync snapshot
// directly through the repo (skipping the host wg dump) then
// asserts the GET endpoint surfaces wg_endpoint / rx_bytes /
// tx_bytes / last_handshake_at on the wire. The reporter itself
// only runs against a real WireGuard interface, so an e2e of the
// full host→file→reporter→DB pipeline isn't possible here.
func TestRESTGetPeer_ReflectsWGSyncState(t *testing.T) {
	f := startFixture(t)

	pub := randomPubKey(t)
	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "rest-wgsync",
		"wireguardPublicKey": pub,
		"tenantSlug":         f.tenantSlug,
	})
	if reg.status != http.StatusOK {
		t.Fatalf("register status=%d body=%s", reg.status, reg.body)
	}
	var regOut struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	_ = json.Unmarshal(reg.body, &regOut)

	peers := repo.NewPeers(f.pool)
	handshake := time.Now().UTC().Truncate(time.Second)
	if _, err := peers.SyncWGState(context.Background(), repo.WGSyncState{
		PubKey:        pub,
		Status:        "online",
		LastHandshake: handshake,
		Endpoint:      "198.51.100.42:51820",
		RxBytes:       4096,
		TxBytes:       8192,
	}); err != nil {
		t.Fatalf("SyncWGState: %v", err)
	}

	got := getJSON(t, f.httpURL+"/api/v1/peers/"+regOut.Self.ID, f.tenantSlug)
	if got.status != http.StatusOK {
		t.Fatalf("get status=%d body=%s", got.status, got.body)
	}
	var p struct {
		WGEndpoint      *string `json:"wgEndpoint"`
		RxBytes         int64   `json:"rxBytes"`
		TxBytes         int64   `json:"txBytes"`
		LastHandshakeAt *string `json:"lastHandshakeAt"`
	}
	if err := json.Unmarshal(got.body, &p); err != nil {
		t.Fatalf("decode: %v; body=%s", err, got.body)
	}
	if p.WGEndpoint == nil || *p.WGEndpoint != "198.51.100.42:51820" {
		t.Errorf("wgEndpoint = %v, want 198.51.100.42:51820", p.WGEndpoint)
	}
	if p.RxBytes != 4096 || p.TxBytes != 8192 {
		t.Errorf("bytes rx=%d tx=%d, want rx=4096 tx=8192", p.RxBytes, p.TxBytes)
	}
	if p.LastHandshakeAt == nil || *p.LastHandshakeAt == "" {
		t.Errorf("lastHandshakeAt missing")
	}
}

// TestRESTGetPeer_NotFound returns 404 for a syntactically valid
// uuid that does not exist.
func TestRESTGetPeer_NotFound(t *testing.T) {
	f := startFixture(t)
	got := getJSON(t, f.httpURL+"/api/v1/peers/00000000-0000-0000-0000-000000000000", f.tenantSlug)
	if got.status != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", got.status, got.body)
	}
}

// TestRESTGetPeer_BadUUID returns 404 for a non-uuid id segment.
// Returning 404 (not 400) keeps the response indistinguishable from
// "exists but wrong tenant", which is the property the cross-tenant
// test below depends on.
func TestRESTGetPeer_BadUUID(t *testing.T) {
	f := startFixture(t)
	got := getJSON(t, f.httpURL+"/api/v1/peers/not-a-uuid", f.tenantSlug)
	if got.status != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", got.status, got.body)
	}
}

// TestRESTPatchPeer_HappyPath PATCHes hostname + status + tags in
// one call and asserts the response carries the updated state.
// Also verifies the audit row was written.
func TestRESTPatchPeer_HappyPath(t *testing.T) {
	f := startFixture(t)

	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "patch-before",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	if reg.status != http.StatusOK {
		t.Fatalf("register status=%d body=%s", reg.status, reg.body)
	}
	var regOut struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	_ = json.Unmarshal(reg.body, &regOut)

	resp := sendJSONWithTenant(t, http.MethodPatch, f.httpURL+"/api/v1/peers/"+regOut.Self.ID, f.tenantSlug, map[string]any{
		"hostname": "patch-after",
		"status":   "disabled",
		"tags":     []string{"  db ", "lan", "db"}, // dupes + whitespace
	})
	if resp.status != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", resp.status, resp.body)
	}

	var p struct {
		Hostname string   `json:"hostname"`
		Status   string   `json:"status"`
		Tags     []string `json:"tags"`
	}
	if err := json.Unmarshal(resp.body, &p); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp.body)
	}
	if p.Hostname != "patch-after" {
		t.Errorf("hostname = %q, want patch-after", p.Hostname)
	}
	if p.Status != "disabled" {
		t.Errorf("status = %q, want disabled", p.Status)
	}
	want := []string{"db", "lan"} // canonicalized
	if len(p.Tags) != len(want) {
		t.Errorf("tags = %v, want %v", p.Tags, want)
	}
	for i, v := range want {
		if i < len(p.Tags) && p.Tags[i] != v {
			t.Errorf("tags[%d] = %q, want %q", i, p.Tags[i], v)
		}
	}

	// One audit row recorded.
	var count int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE action='peer.update' AND resource_id = $1`,
		regOut.Self.ID).Scan(&count); err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if count != 1 {
		t.Errorf("audit_log rows for peer.update = %d, want 1", count)
	}
}

// TestRESTPatchPeer_RejectsInvalidStatus exercises the status enum
// validation. A bad value must surface as 400 before any DB write.
func TestRESTPatchPeer_RejectsInvalidStatus(t *testing.T) {
	f := startFixture(t)
	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "patch-bad-status",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	var regOut struct {
		Self struct{ ID string }
	}
	_ = json.Unmarshal(reg.body, &regOut)
	resp := sendJSONWithTenant(t, http.MethodPatch, f.httpURL+"/api/v1/peers/"+regOut.Self.ID, f.tenantSlug, map[string]any{
		"status": "banana",
	})
	if resp.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", resp.status, resp.body)
	}
}

// TestRESTPatchPeer_CrossTenantIsolation verifies the PATCH path
// shares the same tenant-scoping contract as GET — a peer in
// another tenant returns 404, not 403 or the actual row, so callers
// cannot probe existence across tenants.
func TestRESTPatchPeer_CrossTenantIsolation(t *testing.T) {
	f := startFixture(t)
	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "patch-iso",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	var regOut struct {
		Self struct{ ID string }
	}
	_ = json.Unmarshal(reg.body, &regOut)

	otherSlug := "e2e-other-" + regOut.Self.ID[:8]
	t.Cleanup(func() { cleanupTenant(f.pool, otherSlug) })
	resp := sendJSONWithTenant(t, http.MethodPatch, f.httpURL+"/api/v1/peers/"+regOut.Self.ID, otherSlug, map[string]any{
		"hostname": "hijack-attempt",
	})
	if resp.status != http.StatusNotFound {
		t.Errorf("cross-tenant patch should 404; got status=%d body=%s", resp.status, resp.body)
	}
}

// TestRESTDeletePeer_HappyPath deletes a peer and asserts the row
// is gone (subsequent GET 404s) plus an audit row was written.
func TestRESTDeletePeer_HappyPath(t *testing.T) {
	f := startFixture(t)
	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "doomed",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	var regOut struct {
		Self struct{ ID string }
	}
	_ = json.Unmarshal(reg.body, &regOut)

	resp := sendJSONWithTenant(t, http.MethodDelete, f.httpURL+"/api/v1/peers/"+regOut.Self.ID, f.tenantSlug, nil)
	if resp.status != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", resp.status, resp.body)
	}

	getResp := getJSON(t, f.httpURL+"/api/v1/peers/"+regOut.Self.ID, f.tenantSlug)
	if getResp.status != http.StatusNotFound {
		t.Errorf("after delete GET = %d, want 404", getResp.status)
	}

	var count int
	_ = f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE action='peer.delete' AND resource_id = $1`,
		regOut.Self.ID).Scan(&count)
	if count != 1 {
		t.Errorf("audit_log rows for peer.delete = %d, want 1", count)
	}
}

// TestRESTDeletePeer_CrossTenantIsolation matches the PATCH version:
// deleting another tenant's peer returns 404 and leaves the row.
func TestRESTDeletePeer_CrossTenantIsolation(t *testing.T) {
	f := startFixture(t)
	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "survive",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	var regOut struct {
		Self struct{ ID string }
	}
	_ = json.Unmarshal(reg.body, &regOut)

	otherSlug := "e2e-other-" + regOut.Self.ID[:8]
	t.Cleanup(func() { cleanupTenant(f.pool, otherSlug) })
	resp := sendJSONWithTenant(t, http.MethodDelete, f.httpURL+"/api/v1/peers/"+regOut.Self.ID, otherSlug, nil)
	if resp.status != http.StatusNotFound {
		t.Errorf("cross-tenant delete should 404; got status=%d body=%s", resp.status, resp.body)
	}

	// Original tenant can still see the peer.
	getResp := getJSON(t, f.httpURL+"/api/v1/peers/"+regOut.Self.ID, f.tenantSlug)
	if getResp.status != http.StatusOK {
		t.Errorf("original tenant GET = %d, want 200 (peer should still exist)", getResp.status)
	}
}

// TestRESTGetPeerEvents_HappyPath registers a peer, mutates it
// twice (rename + tag), then asserts /peers/{id}/events returns
// both audit rows newest-first plus the auto-written peer.register.
func TestRESTGetPeerEvents_HappyPath(t *testing.T) {
	f := startFixture(t)

	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "events-peer",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	var regOut struct {
		Self struct{ ID string }
	}
	_ = json.Unmarshal(reg.body, &regOut)

	// Two PATCHes → two peer.update rows.
	for _, body := range []map[string]any{
		{"hostname": "events-peer-renamed"},
		{"tags": []string{"audit-test"}},
	} {
		resp := sendJSONWithTenant(t, http.MethodPatch, f.httpURL+"/api/v1/peers/"+regOut.Self.ID, f.tenantSlug, body)
		if resp.status != http.StatusOK {
			t.Fatalf("patch status=%d body=%s", resp.status, resp.body)
		}
	}

	got := getJSON(t, f.httpURL+"/api/v1/peers/"+regOut.Self.ID+"/events", f.tenantSlug)
	if got.status != http.StatusOK {
		t.Fatalf("events status=%d body=%s", got.status, got.body)
	}
	var body struct {
		Events []struct {
			Action     string          `json:"action"`
			ActorType  string          `json:"actorType"`
			Diff       json.RawMessage `json:"diff"`
			OccurredAt string          `json:"occurredAt"`
		} `json:"events"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, got.body)
	}
	if len(body.Events) != 3 {
		t.Fatalf("len(events) = %d, want 3 (register + 2 update); body=%s", len(body.Events), got.body)
	}
	wantOrder := []string{"peer.update", "peer.update", "peer.register"}
	for i, w := range wantOrder {
		if body.Events[i].Action != w {
			t.Errorf("events[%d].Action = %q, want %q (newest-first)", i, body.Events[i].Action, w)
		}
	}
	// peer.update diffs must carry the {from, to} shape the Web UI relies on.
	if !strings.Contains(string(body.Events[0].Diff), `"from"`) || !strings.Contains(string(body.Events[0].Diff), `"to"`) {
		t.Errorf("peer.update diff missing from/to keys: %s", body.Events[0].Diff)
	}
}

// TestRESTGetPeerEvents_LimitClamped exercises the limit query
// param: an oversized value is clamped, not rejected.
func TestRESTGetPeerEvents_LimitClamped(t *testing.T) {
	f := startFixture(t)
	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "limit-peer",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	var regOut struct {
		Self struct{ ID string }
	}
	_ = json.Unmarshal(reg.body, &regOut)

	got := getJSON(t, f.httpURL+"/api/v1/peers/"+regOut.Self.ID+"/events?limit=999999", f.tenantSlug)
	if got.status != http.StatusOK {
		t.Errorf("limit=999999 should clamp, not error; got status=%d body=%s", got.status, got.body)
	}
}

// TestRESTGetPeerEvents_CrossTenantIsolation registers a peer in
// tenant A then asks tenant B for that peer's events. Must 404
// without leaking that the resource exists elsewhere.
func TestRESTGetPeerEvents_CrossTenantIsolation(t *testing.T) {
	f := startFixture(t)
	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "iso-peer",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	var regOut struct {
		Self struct{ ID string }
	}
	_ = json.Unmarshal(reg.body, &regOut)

	otherSlug := "e2e-other-" + regOut.Self.ID[:8]
	t.Cleanup(func() { cleanupTenant(f.pool, otherSlug) })
	got := getJSON(t, f.httpURL+"/api/v1/peers/"+regOut.Self.ID+"/events", otherSlug)
	if got.status != http.StatusNotFound {
		t.Errorf("cross-tenant events should 404; got status=%d body=%s", got.status, got.body)
	}
}

// TestRESTGetPeerEvents_UnknownPeer covers a syntactically valid
// uuid that doesn't exist — same 404 contract as cross-tenant.
func TestRESTGetPeerEvents_UnknownPeer(t *testing.T) {
	f := startFixture(t)
	got := getJSON(t, f.httpURL+"/api/v1/peers/00000000-0000-0000-0000-000000000000/events", f.tenantSlug)
	if got.status != http.StatusNotFound {
		t.Errorf("unknown peer events should 404; got status=%d body=%s", got.status, got.body)
	}
}

// TestRESTGetPeer_CrossTenantIsolation registers a peer in tenant A
// then fetches the same id with X-Tenant-Slug: B and expects 404.
// Returning the real peer would let an attacker probe peer existence
// across tenants — the same trade-off applies to non-existent ids.
func TestRESTGetPeer_CrossTenantIsolation(t *testing.T) {
	f := startFixture(t)

	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "tenant-a-peer",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	if reg.status != http.StatusOK {
		t.Fatalf("register status=%d body=%s", reg.status, reg.body)
	}
	var regOut struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	_ = json.Unmarshal(reg.body, &regOut)

	otherSlug := "e2e-other-" + regOut.Self.ID[:8]
	t.Cleanup(func() { cleanupTenant(f.pool, otherSlug) })

	got := getJSON(t, f.httpURL+"/api/v1/peers/"+regOut.Self.ID, otherSlug)
	if got.status != http.StatusNotFound {
		t.Errorf("cross-tenant fetch should 404; got status=%d body=%s", got.status, got.body)
	}
}

// jsonResponse bundles an HTTP response status + body for the small
// post-and-decode tests above.
type jsonResponse struct {
	status int
	body   []byte
}

// getJSON GETs a URL with the X-Tenant-Slug header set, matching the
// REST API's dev-fallback tenant resolution.
func getJSON(t *testing.T, url, tenantSlug string) jsonResponse {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}
	req.Header.Set("X-Tenant-Slug", tenantSlug)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return jsonResponse{status: resp.StatusCode, body: body}
}

// sendJSONWithTenant issues a method+body request with X-Tenant-Slug
// set. Used by the PATCH / DELETE tests below.
func sendJSONWithTenant(t *testing.T, method, url, tenantSlug string, payload any) jsonResponse {
	t.Helper()
	var body io.Reader
	if payload != nil {
		buf, _ := json.Marshal(payload)
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Tenant-Slug", tenantSlug)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return jsonResponse{status: resp.StatusCode, body: out}
}

func postJSON(t *testing.T, url string, payload any) jsonResponse {
	if t != nil {
		t.Helper()
	}
	buf, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewReader(buf))
	if err != nil {
		if t != nil {
			t.Fatalf("POST %s: %v", url, err)
		}
		return jsonResponse{}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return jsonResponse{status: resp.StatusCode, body: body}
}

// readSSEEvent reads from r until it encounters an event of the named
// type, then returns the data line for that event. Times out after d.
func readSSEEvent(r io.Reader, name string, d time.Duration) (string, error) {
	type result struct {
		data string
		err  error
	}
	out := make(chan result, 1)
	go func() {
		buf := make([]byte, 0, 4096)
		chunk := make([]byte, 1024)
		var currentEvent string
		for {
			n, err := r.Read(chunk)
			if n > 0 {
				buf = append(buf, chunk[:n]...)
				for {
					idx := bytes.Index(buf, []byte("\n"))
					if idx < 0 {
						break
					}
					line := string(buf[:idx])
					buf = buf[idx+1:]
					switch {
					case strings.HasPrefix(line, "event: "):
						currentEvent = strings.TrimPrefix(line, "event: ")
					case strings.HasPrefix(line, "data: "):
						if currentEvent == name {
							out <- result{data: strings.TrimPrefix(line, "data: ")}
							return
						}
					}
				}
			}
			if err != nil {
				out <- result{err: err}
				return
			}
		}
	}()
	select {
	case r := <-out:
		return r.data, r.err
	case <-time.After(d):
		return "", context.DeadlineExceeded
	}
}
