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

// --- helpers ------------------------------------------------------

type peerRouteShape struct {
	ID               string   `json:"id"`
	AdvertisedRoutes []string `json:"advertisedRoutes"`
	ApprovedRoutes   []string `json:"approvedRoutes"`
	ExitNodeCapable  bool     `json:"exitNodeCapable"`
	ExitNodeApproved bool     `json:"exitNodeApproved"`
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
