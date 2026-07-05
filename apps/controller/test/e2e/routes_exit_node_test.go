// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestAdvertiseRoutes_PendingByDefault verifies that a peer
// registering with advertisedRoutes lands with that list on the
// row, but approved_routes stays empty until admin signs off
// (issue #136).
func TestAdvertiseRoutes_PendingByDefault(t *testing.T) {
	f := startFixture(t)

	body := map[string]any{
		"hostname":           "office-gw",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
		"advertisedRoutes":   []string{"192.168.1.0/24", "10.0.5.0/24"},
	}
	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", body)
	if resp.status != http.StatusOK {
		t.Fatalf("register status=%d body=%s", resp.status, resp.body)
	}
	var got struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode register: %v", err)
	}
	row := getPeerWithRoutes(t, f, got.Self.ID)
	if len(row.AdvertisedRoutes) != 2 {
		t.Errorf("advertised=%v, want 2 entries", row.AdvertisedRoutes)
	}
	if len(row.ApprovedRoutes) != 0 {
		t.Errorf("approved=%v, want empty before admin acts", row.ApprovedRoutes)
	}
}

// TestApproveRoutes_PropagatesToOtherPeers verifies the admin
// approval flow + the ACL compiler integration: peer A advertises
// 192.168.1.0/24, admin approves, peer B's register response sees
// the route in A's allowed_ips.
func TestApproveRoutes_PropagatesToOtherPeers(t *testing.T) {
	f := startFixture(t)

	// Peer A: advertises a subnet.
	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "office-gw",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
		"advertisedRoutes":   []string{"192.168.1.0/24"},
	})
	if resp.status != http.StatusOK {
		t.Fatalf("gw register: %d body=%s", resp.status, resp.body)
	}
	gwID := mustField(t, resp.body, "self.id")

	// Peer B: an existing peer that will see the approved route.
	bResp := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "laptop",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	if bResp.status != http.StatusOK {
		t.Fatalf("laptop register: %d body=%s", bResp.status, bResp.body)
	}

	// Admin approves the route.
	approve := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/peers/"+gwID+"/routes", f.tenantSlug,
		map[string]any{"routes": []string{"192.168.1.0/24"}})
	if approve.status != http.StatusOK {
		t.Fatalf("approve routes: %d body=%s", approve.status, approve.body)
	}

	// Peer B re-registers — its view of the gw peer should now
	// include 192.168.1.0/24 in allowedIps.
	re := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "laptop",
		"wireguardPublicKey": randomPubKey(t), // new pubkey is fine here; we just want peers list
		"tenantSlug":         f.tenantSlug,
	})
	if re.status != http.StatusOK {
		t.Fatalf("re-register: %d body=%s", re.status, re.body)
	}
	var parsed struct {
		Peers []struct {
			ID         string   `json:"id"`
			AllowedIps []string `json:"allowedIps"`
		} `json:"peers"`
	}
	if err := json.Unmarshal(re.body, &parsed); err != nil {
		t.Fatalf("decode re-register: %v body=%s", err, re.body)
	}
	var found bool
	for _, p := range parsed.Peers {
		if p.ID != gwID {
			continue
		}
		found = true
		var hasRoute bool
		for _, c := range p.AllowedIps {
			if c == "192.168.1.0/24" {
				hasRoute = true
			}
		}
		if !hasRoute {
			t.Errorf("gw's allowedIps = %v, missing 192.168.1.0/24 after approve", p.AllowedIps)
		}
	}
	if !found {
		t.Errorf("gw peer missing from laptop's re-register response")
	}
}

// TestApproveRoutes_RejectsCIDRNotAdvertised verifies admin can't
// approve a route the peer didn't actually request — the subset
// constraint prevents an admin from synthesizing arbitrary CIDRs.
func TestApproveRoutes_RejectsCIDRNotAdvertised(t *testing.T) {
	f := startFixture(t)

	gw := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "office-gw",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
		"advertisedRoutes":   []string{"192.168.1.0/24"},
	})
	gwID := mustField(t, gw.body, "self.id")

	r := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/peers/"+gwID+"/routes", f.tenantSlug,
		map[string]any{"routes": []string{"10.0.0.0/8"}}) // not advertised
	if r.status != http.StatusBadRequest {
		t.Errorf("approve unadvertised: status=%d, want 400; body=%s", r.status, r.body)
	}
}

// TestExitNode_AdvertiseThenApprove drives the exit-node round trip:
// peer registers with --advertise-exit-node, admin flips the
// approved bit, peerJSON reflects both fields.
func TestExitNode_AdvertiseThenApprove(t *testing.T) {
	f := startFixture(t)

	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "home-gw",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
		"advertiseExitNode":  true,
	})
	if resp.status != http.StatusOK {
		t.Fatalf("register: %d body=%s", resp.status, resp.body)
	}
	peerID := mustField(t, resp.body, "self.id")

	// Pre-approval state: exit_node_capable=true, exit_node_approved=false.
	row := getPeerWithRoutes(t, f, peerID)
	if !row.ExitNodeCapable {
		t.Errorf("ExitNodeCapable = false, want true after advertise")
	}
	if row.ExitNodeApproved {
		t.Errorf("ExitNodeApproved = true, want false before admin acts")
	}

	// Admin approves.
	a := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/peers/"+peerID+"/exit-node", f.tenantSlug,
		map[string]any{"approved": true})
	if a.status != http.StatusOK {
		t.Fatalf("approve exit-node: %d body=%s", a.status, a.body)
	}

	row = getPeerWithRoutes(t, f, peerID)
	if !row.ExitNodeApproved {
		t.Errorf("ExitNodeApproved = false after admin approval, want true")
	}
}

// TestExitNode_RejectsApprovalForUncapablePeer verifies admin
// can't approve a peer's exit-node role if the client never
// asked for it — the capability flag must come from the peer
// first.
func TestExitNode_RejectsApprovalForUncapablePeer(t *testing.T) {
	f := startFixture(t)

	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "regular-laptop",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
		// advertiseExitNode omitted → capable=false
	})
	peerID := mustField(t, resp.body, "self.id")

	a := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/peers/"+peerID+"/exit-node", f.tenantSlug,
		map[string]any{"approved": true})
	if a.status != http.StatusBadRequest {
		t.Errorf("approve uncapable exit-node: status=%d, want 400; body=%s", a.status, a.body)
	}
}

// TestApproveRoutes_BumpsPolicyRevision verifies #170: after admin
// approves a subnet route, heartbeat returns policyChanged=true so
// peers re-pull their allowed_ips without manual reconnect. Before the
// fix, admin approvals were invisible to the policy-revision channel
// and clients stayed pinned to the pre-approval routes until they
// disconnected.
func TestApproveRoutes_BumpsPolicyRevision(t *testing.T) {
	f := startFixture(t)

	// Peer A advertises a route.
	gwReg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "gw-bump",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
		"advertisedRoutes":   []string{"192.168.86.0/24"},
	})
	gwID := mustField(t, gwReg.body, "self.id")

	// Peer B observes via heartbeat. Capture the baseline revision
	// (zero in a fresh fixture with no policy authored).
	bReg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "laptop-bump",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	bID := mustField(t, bReg.body, "self.id")

	baseline := postJSON(t, f.httpURL+"/api/v1/peers/heartbeat", map[string]any{
		"peerId":              bID,
		"knownPolicyRevision": int64(0),
	})
	if baseline.status != http.StatusOK {
		t.Fatalf("baseline heartbeat: %d body=%s", baseline.status, baseline.body)
	}
	var baseHB struct {
		PolicyChanged         bool  `json:"policyChanged"`
		CurrentPolicyRevision int64 `json:"currentPolicyRevision"`
	}
	if err := json.Unmarshal(baseline.body, &baseHB); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	if baseHB.PolicyChanged {
		t.Errorf("baseline heartbeat: policyChanged=true before any approval, want false")
	}
	startRev := baseHB.CurrentPolicyRevision

	// Admin approves the route.
	approve := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/peers/"+gwID+"/routes", f.tenantSlug,
		map[string]any{"routes": []string{"192.168.86.0/24"}})
	if approve.status != http.StatusOK {
		t.Fatalf("approve: %d body=%s", approve.status, approve.body)
	}

	// Peer B's next heartbeat (still reporting the pre-approval
	// revision) must see policyChanged=true and a bumped current rev.
	after := postJSON(t, f.httpURL+"/api/v1/peers/heartbeat", map[string]any{
		"peerId":              bID,
		"knownPolicyRevision": startRev,
	})
	if after.status != http.StatusOK {
		t.Fatalf("post-approve heartbeat: %d body=%s", after.status, after.body)
	}
	var afterHB struct {
		PolicyChanged         bool  `json:"policyChanged"`
		CurrentPolicyRevision int64 `json:"currentPolicyRevision"`
	}
	if err := json.Unmarshal(after.body, &afterHB); err != nil {
		t.Fatalf("decode after: %v", err)
	}
	if !afterHB.PolicyChanged {
		t.Errorf("post-approve heartbeat: policyChanged=false, want true (issue #170 regression)")
	}
	if afterHB.CurrentPolicyRevision <= startRev {
		t.Errorf("post-approve currentRev=%d, want > baseline %d", afterHB.CurrentPolicyRevision, startRev)
	}
}

// TestApproveExitNode_BumpsPolicyRevision is the exit-node twin of
// TestApproveRoutes_BumpsPolicyRevision. Same #170 contract: flipping
// the exit-node bit must propagate through policy_revision.
func TestApproveExitNode_BumpsPolicyRevision(t *testing.T) {
	f := startFixture(t)

	gwReg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "exit-bump",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
		"advertiseExitNode":  true,
	})
	gwID := mustField(t, gwReg.body, "self.id")

	bReg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "laptop-exit",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	bID := mustField(t, bReg.body, "self.id")

	baseline := postJSON(t, f.httpURL+"/api/v1/peers/heartbeat", map[string]any{
		"peerId":              bID,
		"knownPolicyRevision": int64(0),
	})
	var baseHB struct {
		CurrentPolicyRevision int64 `json:"currentPolicyRevision"`
	}
	_ = json.Unmarshal(baseline.body, &baseHB)
	startRev := baseHB.CurrentPolicyRevision

	approve := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/peers/"+gwID+"/exit-node", f.tenantSlug,
		map[string]any{"approved": true})
	if approve.status != http.StatusOK {
		t.Fatalf("approve exit-node: %d body=%s", approve.status, approve.body)
	}

	after := postJSON(t, f.httpURL+"/api/v1/peers/heartbeat", map[string]any{
		"peerId":              bID,
		"knownPolicyRevision": startRev,
	})
	var afterHB struct {
		PolicyChanged         bool  `json:"policyChanged"`
		CurrentPolicyRevision int64 `json:"currentPolicyRevision"`
	}
	_ = json.Unmarshal(after.body, &afterHB)
	if !afterHB.PolicyChanged {
		t.Errorf("exit-node approve: policyChanged=false, want true")
	}
	if afterHB.CurrentPolicyRevision <= startRev {
		t.Errorf("exit-node approve currentRev=%d, want > %d", afterHB.CurrentPolicyRevision, startRev)
	}
}

// TestUseExitNode_SetAndClear drives the CONSUME side of exit nodes
// (the half that was previously a dead path: SetUsingExitNode had no
// caller). Admin selects an approved exit node for a peer; the peer's
// using_exit_node_peer_id is set and its view of the exit node's
// allowed_ips gains the 0.0.0.0/0 default route. Clearing removes both.
func TestUseExitNode_SetAndClear(t *testing.T) {
	f := startFixture(t)

	// Exit gateway: advertises + gets approved.
	gwReg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "exit-gw",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
		"advertiseExitNode":  true,
	})
	gwID := mustField(t, gwReg.body, "self.id")
	approve := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/peers/"+gwID+"/exit-node", f.tenantSlug,
		map[string]any{"approved": true})
	if approve.status != http.StatusOK {
		t.Fatalf("approve exit-node: %d body=%s", approve.status, approve.body)
	}

	// Client peer. Reuse its pubkey on re-register so it stays the same
	// row (register upserts by wireguard_public_key).
	lapKey := randomPubKey(t)
	lapReg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "laptop",
		"wireguardPublicKey": lapKey,
		"tenantSlug":         f.tenantSlug,
	})
	lapID := mustField(t, lapReg.body, "self.id")

	// Select the exit node.
	use := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/peers/"+lapID+"/use-exit-node", f.tenantSlug,
		map[string]any{"exitNodePeerId": gwID})
	if use.status != http.StatusOK {
		t.Fatalf("use-exit-node: %d body=%s", use.status, use.body)
	}
	if row := getPeerWithRoutes(t, f, lapID); row.UsingExitNodePeerID != gwID {
		t.Errorf("usingExitNodePeerId = %q, want %q", row.UsingExitNodePeerID, gwID)
	}

	// The laptop re-registers (same identity) and its view of the gw peer
	// must now carry the default route through the exit node.
	re := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "laptop",
		"wireguardPublicKey": lapKey,
		"tenantSlug":         f.tenantSlug,
	})
	if re.status != http.StatusOK {
		t.Fatalf("re-register: %d body=%s", re.status, re.body)
	}
	if !gwAllowedIPsContain(t, re.body, gwID, "0.0.0.0/0") {
		t.Errorf("after use-exit-node, gw allowedIps missing 0.0.0.0/0; body=%s", re.body)
	}

	// Clear the selection.
	clear := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/peers/"+lapID+"/use-exit-node", f.tenantSlug,
		map[string]any{"exitNodePeerId": ""})
	if clear.status != http.StatusOK {
		t.Fatalf("clear use-exit-node: %d body=%s", clear.status, clear.body)
	}
	if row := getPeerWithRoutes(t, f, lapID); row.UsingExitNodePeerID != "" {
		t.Errorf("after clear, usingExitNodePeerId = %q, want empty", row.UsingExitNodePeerID)
	}
	re2 := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "laptop",
		"wireguardPublicKey": lapKey,
		"tenantSlug":         f.tenantSlug,
	})
	if gwAllowedIPsContain(t, re2.body, gwID, "0.0.0.0/0") {
		t.Errorf("after clear, gw allowedIps still has 0.0.0.0/0; body=%s", re2.body)
	}
}

// TestUseExitNode_RejectsUnapprovedTarget: an exit-node-capable but
// not-yet-approved peer can't be selected as an exit node.
func TestUseExitNode_RejectsUnapprovedTarget(t *testing.T) {
	f := startFixture(t)

	gwReg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "unapproved-gw",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
		"advertiseExitNode":  true, // capable, but NOT approved
	})
	gwID := mustField(t, gwReg.body, "self.id")

	lapReg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "laptop",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	lapID := mustField(t, lapReg.body, "self.id")

	use := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/peers/"+lapID+"/use-exit-node", f.tenantSlug,
		map[string]any{"exitNodePeerId": gwID})
	if use.status != http.StatusBadRequest {
		t.Errorf("use unapproved exit node: status=%d, want 400; body=%s", use.status, use.body)
	}
}

// TestUseExitNode_RejectsSelfAndUnknown: a peer can't route through
// itself, and an unknown target id is a 400 (not a 500/panic).
func TestUseExitNode_RejectsSelfAndUnknown(t *testing.T) {
	f := startFixture(t)

	lapReg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "laptop",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	lapID := mustField(t, lapReg.body, "self.id")

	self := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/peers/"+lapID+"/use-exit-node", f.tenantSlug,
		map[string]any{"exitNodePeerId": lapID})
	if self.status != http.StatusBadRequest {
		t.Errorf("use self as exit node: status=%d, want 400; body=%s", self.status, self.body)
	}

	unknown := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/peers/"+lapID+"/use-exit-node", f.tenantSlug,
		map[string]any{"exitNodePeerId": "00000000-0000-0000-0000-000000000000"})
	if unknown.status != http.StatusBadRequest {
		t.Errorf("use unknown exit node: status=%d, want 400; body=%s", unknown.status, unknown.body)
	}
}

// --- helpers ------------------------------------------------------

// gwAllowedIPsContain reports whether the peer `gwID` in a register
// response's peers list has `cidr` among its allowedIps.
func gwAllowedIPsContain(t *testing.T, body []byte, gwID, cidr string) bool {
	t.Helper()
	var parsed struct {
		Peers []struct {
			ID         string   `json:"id"`
			AllowedIps []string `json:"allowedIps"`
		} `json:"peers"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode peers: %v body=%s", err, body)
	}
	for _, p := range parsed.Peers {
		if p.ID != gwID {
			continue
		}
		for _, c := range p.AllowedIps {
			if c == cidr {
				return true
			}
		}
	}
	return false
}

type peerRouteShape struct {
	ID                  string   `json:"id"`
	AdvertisedRoutes    []string `json:"advertisedRoutes"`
	ApprovedRoutes      []string `json:"approvedRoutes"`
	ExitNodeCapable     bool     `json:"exitNodeCapable"`
	ExitNodeApproved    bool     `json:"exitNodeApproved"`
	UsingExitNodePeerID string   `json:"usingExitNodePeerId"`
}

func getPeerWithRoutes(t *testing.T, f *fixture, peerID string) peerRouteShape {
	t.Helper()
	resp := getJSON(t, f.httpURL+"/api/v1/peers/"+peerID, f.tenantSlug)
	var p peerRouteShape
	if err := json.Unmarshal(resp.body, &p); err != nil {
		t.Fatalf("decode peer: %v body=%s", err, resp.body)
	}
	return p
}

// mustField extracts a dot-pathed JSON field (e.g. "self.id") for
// quick assertions in tests that only care about one nested value.
func mustField(t *testing.T, body []byte, path string) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode field %s: %v body=%s", path, err, body)
	}
	parts := strings.Split(path, ".")
	var cur any = doc
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %s: not an object at %q", path, p)
		}
		cur = m[p]
	}
	if s, ok := cur.(string); ok {
		return s
	}
	t.Fatalf("path %s: not a string (%T)", path, cur)
	return ""
}
