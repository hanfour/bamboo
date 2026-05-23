// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestRESTPeerRouteConflicts_DuplicateApprovedCIDR drives the happy
// path: register two peers, advertise the same /24 on both, admin
// approves both, the conflicts endpoint surfaces the overlap on
// either peer's view.
func TestRESTPeerRouteConflicts_DuplicateApprovedCIDR(t *testing.T) {
	f := startFixture(t)

	a := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "office-a",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
		"advertisedRoutes":   []string{"192.168.1.0/24"},
	})
	aID := mustField(t, a.body, "self.id")
	b := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "office-b",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
		"advertisedRoutes":   []string{"192.168.1.0/24"},
	})
	bID := mustField(t, b.body, "self.id")

	for _, peerID := range []string{aID, bID} {
		r := sendJSONWithTenant(t, http.MethodPost,
			f.httpURL+"/api/v1/peers/"+peerID+"/routes", f.tenantSlug,
			map[string]any{"routes": []string{"192.168.1.0/24"}})
		if r.status != http.StatusOK {
			t.Fatalf("approve %s: status=%d body=%s", peerID, r.status, r.body)
		}
	}

	resp := getJSON(t, f.httpURL+"/api/v1/peers/"+aID+"/route-conflicts", f.tenantSlug)
	if resp.status != http.StatusOK {
		t.Fatalf("conflicts: status=%d body=%s", resp.status, resp.body)
	}
	var got struct {
		Conflicts []struct {
			Cidr          string `json:"cidr"`
			Kind          string `json:"kind"`
			OtherPeerID   string `json:"otherPeerId"`
			OtherHostname string `json:"otherHostname"`
			OtherCidr     string `json:"otherCidr"`
		} `json:"conflicts"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, resp.body)
	}
	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts = %d, want 1; rows=%+v", len(got.Conflicts), got.Conflicts)
	}
	c := got.Conflicts[0]
	if c.Cidr != "192.168.1.0/24" || c.OtherCidr != "192.168.1.0/24" {
		t.Errorf("cidrs = %s vs %s, want both 192.168.1.0/24", c.Cidr, c.OtherCidr)
	}
	if c.Kind != "duplicate" {
		t.Errorf("kind = %q, want duplicate", c.Kind)
	}
	if c.OtherPeerID != bID || c.OtherHostname != "office-b" {
		t.Errorf("other peer = (%s, %s), want (%s, office-b)", c.OtherPeerID, c.OtherHostname, bID)
	}
}

// TestRESTPeerRouteConflicts_NoConflictsReturnsEmptyArray verifies
// the negative case — when peer's approved routes don't overlap
// anyone else's, the endpoint returns a JSON array (not null) so
// the Web type stays Conflict[] rather than Conflict[] | null.
func TestRESTPeerRouteConflicts_NoConflictsReturnsEmptyArray(t *testing.T) {
	f := startFixture(t)

	gw := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "lonely-gw",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
		"advertisedRoutes":   []string{"10.50.0.0/24"},
	})
	gwID := mustField(t, gw.body, "self.id")
	approve := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/peers/"+gwID+"/routes", f.tenantSlug,
		map[string]any{"routes": []string{"10.50.0.0/24"}})
	if approve.status != http.StatusOK {
		t.Fatalf("approve: %d body=%s", approve.status, approve.body)
	}

	resp := getJSON(t, f.httpURL+"/api/v1/peers/"+gwID+"/route-conflicts", f.tenantSlug)
	if resp.status != http.StatusOK {
		t.Fatalf("conflicts: status=%d body=%s", resp.status, resp.body)
	}
	if !json.Valid(resp.body) {
		t.Fatalf("body is not valid JSON: %s", resp.body)
	}
	// The literal `"conflicts":[]` must appear — not `"conflicts":null`
	// — so the TS type at the Web boundary stays a real array.
	if want := `"conflicts":[]`; !contains(string(resp.body), want) {
		t.Errorf("body %s does not contain %s", resp.body, want)
	}
}

// TestRESTPeerRouteConflicts_404ForCrossTenantPeer pins the same
// tenant-scope short-circuit the other peer-scoped endpoints rely
// on: a peer UUID that exists but isn't in the caller's tenant
// resolves to 404 before the detector runs.
func TestRESTPeerRouteConflicts_404ForCrossTenantPeer(t *testing.T) {
	f := startFixture(t)

	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "rc-tenant-a",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	peerID := mustField(t, reg.body, "self.id")

	resp := getJSON(t, f.httpURL+"/api/v1/peers/"+peerID+"/route-conflicts", "other-tenant")
	if resp.status != http.StatusNotFound {
		t.Errorf("cross-tenant: status=%d, want 404; body=%s", resp.status, resp.body)
	}
}

// contains is a tiny strings.Contains stand-in — pulled inline so
// this file doesn't grow an import section just for one call.
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	n := len(sub)
	for i := 0; i+n <= len(s); i++ {
		if s[i:i+n] == sub {
			return i
		}
	}
	return -1
}
