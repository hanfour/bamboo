// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"encoding/json"
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

// TestIngressLiveness_ServiceMutationsBumpRevision: a service change bumps
// the policy revision (so connected peers re-register) exactly when it
// changes liveness. Filing or approving a disabled service, or deleting a
// service that is not live, leaves the tenant's peers alone.
func TestIngressLiveness_ServiceMutationsBumpRevision(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	base := f.httpURL + "/api/v1"
	var sid string
	approve := func() jsonResponse {
		return ingressCall(t, http.MethodPost, base+"/ingress/"+sid+"/approve", "", f.tenantSlug, nil)
	}
	patch := func(body map[string]any) func() jsonResponse {
		return func() jsonResponse { return ingressCall(t, http.MethodPatch, base+"/ingress/"+sid, "", f.tenantSlug, body) }
	}
	del := func() jsonResponse { return ingressCall(t, http.MethodDelete, base+"/ingress/"+sid, "", f.tenantSlug, nil) }
	create := func() jsonResponse {
		r := ingressCall(t, http.MethodPost, base+"/peers/"+mac.id+"/ingress", "", f.tenantSlug,
			map[string]any{"label": "svc-" + uuid.NewString()[:8], "target_port": 47800})
		sid = decodeIngressSvc(t, r).ID
		return r
	}
	steps := []struct {
		name     string
		call     func() jsonResponse
		wantBump bool
	}{
		{"create", create, false},
		{"approve while disabled", approve, false},
		{"enable (goes live)", patch(map[string]any{"enabled": true}), true},
		{"port change (live -> pending)", patch(map[string]any{"target_port": 47801}), true},
		{"port change while pending", patch(map[string]any{"target_port": 47802}), false},
		{"approve (goes live again)", approve, true},
		{"disable (goes dark)", patch(map[string]any{"enabled": false}), true},
		{"delete while dark", del, false},
		{"create again", create, false},
		{"enable while pending", patch(map[string]any{"enabled": true}), false},
		{"approve (goes live)", approve, true},
		{"delete while live", del, true},
	}
	for _, s := range steps {
		before := policyRevision(t, f)
		r := s.call()
		if r.status >= 300 {
			t.Fatalf("%s: %d %s", s.name, r.status, r.body)
		}
		if bumped := policyRevision(t, f) > before; bumped != s.wantBump {
			t.Errorf("%s: bump=%v, want %v", s.name, bumped, s.wantBump)
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

// TestRESTIngress_ReapproveIsNoOp: approving an approved service keeps the
// original approver and time, writes no audit row, and does not bump.
func TestRESTIngress_ReapproveIsNoOp(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	base := f.httpURL + "/api/v1"
	created := ingressCall(t, http.MethodPost, base+"/peers/"+mac.id+"/ingress", "", f.tenantSlug,
		map[string]any{"label": "svc-" + uuid.NewString()[:8], "target_port": 47800})
	sid := decodeIngressSvc(t, created).ID
	first := ingressCall(t, http.MethodPost, base+"/ingress/"+sid+"/approve", "", f.tenantSlug, nil)
	if first.status != http.StatusOK {
		t.Fatalf("approve: %d %s", first.status, first.body)
	}
	readApproval := func() (approvedAt string) {
		if err := f.admin.QueryRow(context.Background(),
			`SELECT approved_at::text FROM ingress_services WHERE id = $1`, sid).Scan(&approvedAt); err != nil {
			t.Fatalf("read approval: %v", err)
		}
		return approvedAt
	}
	countApprovals := func() (n int) {
		if err := f.admin.QueryRow(context.Background(),
			`SELECT count(*) FROM audit_log WHERE action = 'ingress.approve' AND diff->>'service_id' = $1`, sid).Scan(&n); err != nil {
			t.Fatalf("count audit: %v", err)
		}
		return n
	}
	at, rev := readApproval(), policyRevision(t, f)
	second := ingressCall(t, http.MethodPost, base+"/ingress/"+sid+"/approve", "", f.tenantSlug, nil)
	if second.status != http.StatusOK || !decodeIngressSvc(t, second).Approved {
		t.Fatalf("re-approve: %d %s", second.status, second.body)
	}
	if got := readApproval(); got != at {
		t.Errorf("approved_at %s -> %s; re-approve must not overwrite", at, got)
	}
	if n := countApprovals(); n != 1 {
		t.Errorf("ingress.approve audit rows = %d, want 1", n)
	}
	if got := policyRevision(t, f); got != rev {
		t.Errorf("policy revision %d -> %d; re-approve must not bump", rev, got)
	}
}

// TestIngressRegister_TaggedPreAuthKeyEnforces: a peer that registers with
// a pre-auth key tagged ingress on a fresh tenant (revision 0) lifts the
// tenant to revision >= 1, so clients enforce AllowedIps and bystanders
// drop the ingress peer instead of falling back to its /32.
func TestIngressRegister_TaggedPreAuthKeyEnforces(t *testing.T) {
	f := startFixture(t)
	bystander := registerIngressTestPeer(t, f, "bystander")
	if rev := policyRevision(t, f); rev != 0 {
		t.Fatalf("fresh tenant revision = %d, want 0", rev)
	}
	key := ingressCall(t, http.MethodPost, f.httpURL+"/api/v1/preauth-keys", "", f.tenantSlug,
		map[string]any{"description": "vps", "autoApprove": true, "tags": []string{"ingress"}})
	if key.status != http.StatusOK && key.status != http.StatusCreated {
		t.Fatalf("mint key: %d %s", key.status, key.body)
	}
	var k struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(key.body, &k); err != nil || k.Secret == "" {
		t.Fatalf("decode key: %v %s", err, key.body)
	}
	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname": "vps", "wireguardPublicKey": randomPubKey(t), "preAuthKeySecret": k.Secret,
	})
	if reg.status != http.StatusOK {
		t.Fatalf("register vps: %d %s", reg.status, reg.body)
	}
	var got struct {
		Self struct {
			ID   string   `json:"id"`
			IP   string   `json:"ip"`
			Tags []string `json:"tags"`
		} `json:"self"`
	}
	if err := json.Unmarshal(reg.body, &got); err != nil {
		t.Fatalf("decode register: %v", err)
	}
	if rev := policyRevision(t, f); rev < 1 {
		t.Errorf("revision after tagged register = %d, want >= 1", rev)
	}
	if gwAllowedIPsContain(t, reRegisterBody(t, f, bystander.id), got.Self.ID, got.Self.IP+"/32") {
		t.Error("bystander's view lists the ingress peer")
	}
}
