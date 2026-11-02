// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/google/uuid"
)

// policyRevision reads the fixture tenant's policy revision (0 when no
// acl_policies row exists yet).
func policyRevision(t *testing.T, f *fixture) int64 {
	t.Helper()
	var rev int64
	if err := f.admin.QueryRow(context.Background(), `
		SELECT COALESCE((SELECT revision FROM acl_policies
		                  WHERE tenant_id = (SELECT id FROM tenants WHERE slug = $1)), 0)`,
		f.tenantSlug).Scan(&rev); err != nil {
		t.Fatalf("read policy revision: %v", err)
	}
	return rev
}

// reRegisterBody re-registers an existing peer and returns the response.
func reRegisterBody(t *testing.T, f *fixture, peerID string) []byte {
	t.Helper()
	r := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname": "x", "wireguardPublicKey": getPeerPubKey(t, f, peerID), "tenantSlug": f.tenantSlug,
	})
	if r.status != http.StatusOK {
		t.Fatalf("re-register %s: %d %s", peerID, r.status, r.body)
	}
	return r.body
}

// makeLiveService files, approves, and enables a service on peerID through
// the dev-fallback REST path and returns its id.
func makeLiveService(t *testing.T, f *fixture, peerID string) string {
	t.Helper()
	base := f.httpURL + "/api/v1"
	created := ingressCall(t, http.MethodPost, base+"/peers/"+peerID+"/ingress", "", f.tenantSlug,
		map[string]any{"label": "svc-" + uuid.NewString()[:8], "target_port": 47800})
	if created.status != http.StatusCreated {
		t.Fatalf("create: %d %s", created.status, created.body)
	}
	sid := decodeIngressSvc(t, created).ID
	if r := ingressCall(t, http.MethodPost, base+"/ingress/"+sid+"/approve", "", f.tenantSlug, nil); r.status != http.StatusOK {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	if r := ingressCall(t, http.MethodPatch, base+"/ingress/"+sid, "", f.tenantSlug, map[string]any{"enabled": true}); r.status != http.StatusOK {
		t.Fatalf("enable: %d %s", r.status, r.body)
	}
	return sid
}

// TestIngressMesh_AllowAllTenantGatesIngressPeer: a tenant with no
// authored policy is full mesh, except that the tag:ingress peer only
// reaches live targets and only live targets list it.
func TestIngressMesh_AllowAllTenantGatesIngressPeer(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	bystander := registerIngressTestPeer(t, f, "bystander")
	vps := registerIngressTestPeer(t, f, "vps")
	tagPeer(t, f, vps.id, "ingress")

	vpsView := reRegisterBody(t, f, vps.id)
	if gwAllowedIPsContain(t, vpsView, mac.id, mac.ip+"/32") || gwAllowedIPsContain(t, vpsView, bystander.id, bystander.ip+"/32") {
		t.Fatal("ingress peer reaches the mesh before any service is live")
	}
	if gwAllowedIPsContain(t, reRegisterBody(t, f, mac.id), vps.id, vps.ip+"/32") {
		t.Fatal("non-target lists the ingress peer")
	}
	if !gwAllowedIPsContain(t, reRegisterBody(t, f, bystander.id), mac.id, mac.ip+"/32") {
		t.Fatal("allow-all tenant: ordinary peers must still reach each other")
	}

	makeLiveService(t, f, mac.id)

	vpsView = reRegisterBody(t, f, vps.id)
	if !gwAllowedIPsContain(t, vpsView, mac.id, mac.ip+"/32") {
		t.Error("ingress peer must reach the live target")
	}
	if gwAllowedIPsContain(t, vpsView, bystander.id, bystander.ip+"/32") {
		t.Error("ingress peer must not reach a non-target")
	}
	if !gwAllowedIPsContain(t, reRegisterBody(t, f, mac.id), vps.id, vps.ip+"/32") {
		t.Error("live target must list the ingress peer (return path)")
	}
	byView := reRegisterBody(t, f, bystander.id)
	if gwAllowedIPsContain(t, byView, vps.id, vps.ip+"/32") {
		t.Error("non-target must not list the ingress peer")
	}
	if !gwAllowedIPsContain(t, byView, mac.id, mac.ip+"/32") {
		t.Error("allow-all tenant: ordinary peers must still reach each other")
	}
}

// TestIngressLiveness_ServiceMutationsBumpRevision: every service change
// bumps the policy revision so connected peers re-register.
func TestIngressLiveness_ServiceMutationsBumpRevision(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	base := f.httpURL + "/api/v1"
	var sid string
	steps := []struct {
		name string
		call func() jsonResponse
	}{
		{"create", func() jsonResponse {
			r := ingressCall(t, http.MethodPost, base+"/peers/"+mac.id+"/ingress", "", f.tenantSlug,
				map[string]any{"label": "svc-" + uuid.NewString()[:8], "target_port": 47800})
			sid = decodeIngressSvc(t, r).ID
			return r
		}},
		{"approve", func() jsonResponse {
			return ingressCall(t, http.MethodPost, base+"/ingress/"+sid+"/approve", "", f.tenantSlug, nil)
		}},
		{"enable", func() jsonResponse {
			return ingressCall(t, http.MethodPatch, base+"/ingress/"+sid, "", f.tenantSlug, map[string]any{"enabled": true})
		}},
		{"port change", func() jsonResponse {
			return ingressCall(t, http.MethodPatch, base+"/ingress/"+sid, "", f.tenantSlug, map[string]any{"target_port": 47801})
		}},
		{"disable", func() jsonResponse {
			return ingressCall(t, http.MethodPatch, base+"/ingress/"+sid, "", f.tenantSlug, map[string]any{"enabled": false})
		}},
		{"delete", func() jsonResponse {
			return ingressCall(t, http.MethodDelete, base+"/ingress/"+sid, "", f.tenantSlug, nil)
		}},
	}
	for _, s := range steps {
		before := policyRevision(t, f)
		r := s.call()
		if r.status >= 300 {
			t.Fatalf("%s: %d %s", s.name, r.status, r.body)
		}
		if after := policyRevision(t, f); after <= before {
			t.Errorf("%s: policy revision %d -> %d, want a bump", s.name, before, after)
		}
	}
}

// TestIngressLiveness_PeerChangesBumpRevision: approving, rejecting, or
// deleting an ingress target or the ingress peer bumps the revision; the
// same actions on an unrelated peer keep today's behaviour (no bump).
func TestIngressLiveness_PeerChangesBumpRevision(t *testing.T) {
	type setup func(t *testing.T, f *fixture, peerID string)
	asTarget := func(t *testing.T, f *fixture, peerID string) {
		r := ingressCall(t, http.MethodPost, f.httpURL+"/api/v1/peers/"+peerID+"/ingress", "", f.tenantSlug,
			map[string]any{"label": "svc-" + uuid.NewString()[:8], "target_port": 47800})
		if r.status != http.StatusCreated {
			t.Fatalf("create: %d %s", r.status, r.body)
		}
	}
	asIngress := func(t *testing.T, f *fixture, peerID string) { tagPeer(t, f, peerID, "ingress") }
	unrelated := func(*testing.T, *fixture, string) {}

	cases := []struct {
		name     string
		setup    setup
		pending  bool
		method   string
		action   string // path suffix after /peers/{id}
		wantBump bool
	}{
		{"approve target", asTarget, true, http.MethodPost, "/approve", true},
		{"reject target", asTarget, true, http.MethodPost, "/reject", true},
		{"delete target", asTarget, false, http.MethodDelete, "", true},
		{"approve ingress peer", asIngress, true, http.MethodPost, "/approve", true},
		{"reject ingress peer", asIngress, true, http.MethodPost, "/reject", true},
		{"delete ingress peer", asIngress, false, http.MethodDelete, "", true},
		{"approve unrelated", unrelated, true, http.MethodPost, "/approve", false},
		{"delete unrelated", unrelated, false, http.MethodDelete, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := startFixture(t)
			p := registerIngressTestPeer(t, f, "p")
			c.setup(t, f, p.id)
			if c.pending {
				if _, err := f.admin.Exec(context.Background(),
					`UPDATE peers SET approval_status = 'pending', approved_at = NULL WHERE id = $1`, p.id); err != nil {
					t.Fatalf("set pending: %v", err)
				}
			}
			before := policyRevision(t, f)
			r := ingressCall(t, c.method, f.httpURL+"/api/v1/peers/"+p.id+c.action, "", f.tenantSlug, nil)
			if r.status >= 300 {
				t.Fatalf("%s: %d %s", c.name, r.status, r.body)
			}
			after := policyRevision(t, f)
			if bumped := after > before; bumped != c.wantBump {
				t.Errorf("policy revision %d -> %d; want bump=%v", before, after, c.wantBump)
			}
		})
	}
}

// TestIngressTag_OnlyAdminsCanAssign: tag:ingress grants a peer the
// public-services role, so members must not be able to attach it.
func TestIngressTag_OnlyAdminsCanAssign(t *testing.T) {
	f := startFixture(t)
	p := registerIngressTestPeer(t, f, "vps")
	f.enableRequireAuth()
	member := f.mintJWT(t, false)
	admin := f.mintJWT(t, true)
	base := f.httpURL + "/api/v1"

	if r := ingressCall(t, http.MethodPatch, base+"/peers/"+p.id, member, "", map[string]any{"tags": []string{"ingress"}}); r.status != http.StatusForbidden {
		t.Errorf("member PATCH tags=[ingress]: %d, want 403 (%s)", r.status, r.body)
	}
	if r := ingressCall(t, http.MethodPost, base+"/preauth-keys", member, "", map[string]any{"tags": []string{"ingress"}}); r.status != http.StatusForbidden {
		t.Errorf("member preauth key with tags=[ingress]: %d, want 403 (%s)", r.status, r.body)
	}
	if tags := peerTags(t, f, p.id); slices.Contains(tags, "ingress") {
		t.Fatalf("member attached tag:ingress: %v", tags)
	}
	if r := ingressCall(t, http.MethodPatch, base+"/peers/"+p.id, admin, "", map[string]any{"tags": []string{"ingress"}}); r.status != http.StatusOK {
		t.Fatalf("admin PATCH tags=[ingress]: %d %s", r.status, r.body)
	}
	if tags := peerTags(t, f, p.id); !slices.Contains(tags, "ingress") {
		t.Errorf("admin PATCH did not attach tag:ingress: %v", tags)
	}
}

func peerTags(t *testing.T, f *fixture, peerID string) []string {
	t.Helper()
	var tags []string
	if err := f.admin.QueryRow(context.Background(), `
		SELECT COALESCE(array_agg(tg.name ORDER BY tg.name), '{}')
		  FROM peer_tags pt JOIN tags tg ON tg.id = pt.tag_id
		 WHERE pt.peer_id = $1`, peerID).Scan(&tags); err != nil {
		t.Fatalf("read tags: %v", err)
	}
	return tags
}

// TestIngressLiveness_IngressTagChangeBumpsRevision: attaching or removing
// tag:ingress changes who the ingress peer is, so peers must re-register;
// other tag edits keep today's behaviour (no bump).
func TestIngressLiveness_IngressTagChangeBumpsRevision(t *testing.T) {
	f := startFixture(t)
	p := registerIngressTestPeer(t, f, "vps")
	url := f.httpURL + "/api/v1/peers/" + p.id
	steps := []struct {
		tags     []string
		wantBump bool
	}{
		{[]string{"web"}, false},
		{[]string{"web", "ingress"}, true},
		{[]string{"ingress"}, false},
		{[]string{}, true},
	}
	for _, s := range steps {
		before := policyRevision(t, f)
		if r := ingressCall(t, http.MethodPatch, url, "", f.tenantSlug, map[string]any{"tags": s.tags}); r.status != http.StatusOK {
			t.Fatalf("PATCH tags=%v: %d %s", s.tags, r.status, r.body)
		}
		if bumped := policyRevision(t, f) > before; bumped != s.wantBump {
			t.Errorf("PATCH tags=%v: bump=%v, want %v", s.tags, bumped, s.wantBump)
		}
	}
}
