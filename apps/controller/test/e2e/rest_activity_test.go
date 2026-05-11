// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestRESTActivity_HappyPath registers a peer (writes peer.register)
// then patches it (writes peer.update), and asserts the tenant-wide
// /activity feed returns both events newest-first with the
// resourceType/resourceId fields that distinguish it from the
// per-peer timeline.
func TestRESTActivity_HappyPath(t *testing.T) {
	f := startFixture(t)

	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "activity-peer",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	if reg.status != http.StatusOK {
		t.Fatalf("register status=%d body=%s", reg.status, reg.body)
	}
	var regOut struct {
		Self struct{ ID string }
	}
	_ = json.Unmarshal(reg.body, &regOut)

	patch := sendJSONWithTenant(t, http.MethodPatch,
		f.httpURL+"/api/v1/peers/"+regOut.Self.ID, f.tenantSlug,
		map[string]any{"hostname": "activity-peer-renamed"})
	if patch.status != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patch.status, patch.body)
	}

	got := getJSON(t, f.httpURL+"/api/v1/activity", f.tenantSlug)
	if got.status != http.StatusOK {
		t.Fatalf("activity status=%d body=%s", got.status, got.body)
	}
	var body struct {
		Events []struct {
			Action       string `json:"action"`
			ResourceType string `json:"resourceType"`
			ResourceID   string `json:"resourceId"`
			ActorType    string `json:"actorType"`
		} `json:"events"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, got.body)
	}
	if len(body.Events) < 2 {
		t.Fatalf("len(events) = %d, want >= 2 (register + update); body=%s", len(body.Events), got.body)
	}

	// Newest-first: peer.update before peer.register.
	if body.Events[0].Action != "peer.update" {
		t.Errorf("events[0].Action = %q, want peer.update", body.Events[0].Action)
	}
	if body.Events[1].Action != "peer.register" {
		t.Errorf("events[1].Action = %q, want peer.register", body.Events[1].Action)
	}

	// resourceType/resourceId must be set on tenant-wide activity
	// (the distinguishing feature vs the per-peer timeline output).
	for i, e := range body.Events[:2] {
		if e.ResourceType != "peer" {
			t.Errorf("events[%d].resourceType = %q, want peer", i, e.ResourceType)
		}
		if e.ResourceID != regOut.Self.ID {
			t.Errorf("events[%d].resourceId = %q, want %q", i, e.ResourceID, regOut.Self.ID)
		}
	}
}

// TestRESTActivity_CrossTenantIsolation writes events in tenant A,
// then queries /activity with X-Tenant-Slug pointing at tenant B.
// Tenant B must see an empty result, not A's events.
func TestRESTActivity_CrossTenantIsolation(t *testing.T) {
	f := startFixture(t)

	// Tenant A: register a peer (writes peer.register).
	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "iso-activity",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	if reg.status != http.StatusOK {
		t.Fatalf("register: status=%d body=%s", reg.status, reg.body)
	}

	// Tenant B: tenant auto-created on first request via the X-Tenant-Slug
	// dev fallback. Cleanup to avoid leaking between tests.
	otherSlug := "e2e-other-activity-" + f.tenantSlug[len("e2e-"):]
	t.Cleanup(func() { cleanupTenant(f.pool, otherSlug) })

	got := getJSON(t, f.httpURL+"/api/v1/activity", otherSlug)
	if got.status != http.StatusOK {
		t.Fatalf("activity status for other tenant=%d body=%s", got.status, got.body)
	}
	var body struct {
		Events []any `json:"events"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, got.body)
	}
	if len(body.Events) != 0 {
		t.Errorf("other tenant saw %d events (cross-tenant leak); body=%s", len(body.Events), got.body)
	}
}
