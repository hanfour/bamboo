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

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

type ingressTestPeer struct {
	id string
	ip string
}

// registerIngressTestPeer registers a peer through the dev-fallback path
// and force-approves it, so tests do not depend on the approval queue.
// Call it before f.enableRequireAuth().
func registerIngressTestPeer(t *testing.T, f *fixture, hostname string) ingressTestPeer {
	t.Helper()
	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           hostname,
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	if resp.status != http.StatusOK {
		t.Fatalf("register %s: status=%d body=%s", hostname, resp.status, resp.body)
	}
	var got struct {
		Self struct {
			ID string `json:"id"`
			IP string `json:"ip"`
		} `json:"self"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode register: %v", err)
	}
	if _, err := f.admin.Exec(context.Background(),
		`UPDATE peers SET approval_status = 'approved', approved_at = now() WHERE id = $1`, got.Self.ID); err != nil {
		t.Fatalf("approve peer: %v", err)
	}
	return ingressTestPeer{id: got.Self.ID, ip: got.Self.IP}
}

func tagPeer(t *testing.T, f *fixture, peerID string, tags ...string) {
	t.Helper()
	if _, err := repo.NewPeers(f.admin).SetTags(context.Background(), uuid.MustParse(peerID), tags); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
}

// ingressCall sends a request with an optional bearer and, when tenantSlug
// is non-empty, the dev-fallback X-Tenant-Slug header.
func ingressCall(t *testing.T, method, url, bearer, tenantSlug string, payload any) jsonResponse {
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
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if tenantSlug != "" {
		req.Header.Set("X-Tenant-Slug", tenantSlug)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return jsonResponse{status: resp.StatusCode, body: out}
}

type ingressSvcShape struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	URL        string `json:"url"`
	TargetPort int    `json:"targetPort"`
	Enabled    bool   `json:"enabled"`
	Approved   bool   `json:"approved"`
	Live       bool   `json:"live"`
	ACLRule    string `json:"aclRule"`
}

func decodeIngressSvc(t *testing.T, r jsonResponse) ingressSvcShape {
	t.Helper()
	var s ingressSvcShape
	if err := json.Unmarshal(r.body, &s); err != nil {
		t.Fatalf("decode service: %v body=%s", err, r.body)
	}
	return s
}

func TestRESTIngress_Lifecycle(t *testing.T) {
	f := startFixture(t)
	f.httpAPI.SetIngressConfig(nil, "svc.example.test")
	mac := registerIngressTestPeer(t, f, "mac")
	f.enableRequireAuth()
	member := f.mintJWT(t, false)
	admin := f.mintJWT(t, true)
	label := "svc-" + uuid.NewString()[:8]
	base := f.httpURL + "/api/v1"

	created := ingressCall(t, http.MethodPost, base+"/peers/"+mac.id+"/ingress", member, "",
		map[string]any{"label": label, "target_port": 47800})
	if created.status != http.StatusCreated {
		t.Fatalf("create: %d %s", created.status, created.body)
	}
	svc := decodeIngressSvc(t, created)
	if svc.Approved || svc.Enabled || svc.Live || svc.URL != "https://"+label+".svc.example.test" {
		t.Fatalf("new service = %+v", svc)
	}

	for name, body := range map[string]map[string]any{
		"duplicate": {"label": label, "target_port": 47800},
		"invalid":   {"label": "Bad_Label", "target_port": 47800},
		"reserved":  {"label": "www", "target_port": 47800},
		"low port":  {"label": "svc-" + uuid.NewString()[:8], "target_port": 80},
	} {
		want := http.StatusBadRequest
		if name == "duplicate" {
			want = http.StatusConflict
		}
		if r := ingressCall(t, http.MethodPost, base+"/peers/"+mac.id+"/ingress", member, "", body); r.status != want {
			t.Errorf("%s: status=%d want %d body=%s", name, r.status, want, r.body)
		}
	}

	if r := ingressCall(t, http.MethodPost, base+"/ingress/"+svc.ID+"/approve", member, "", nil); r.status != http.StatusForbidden {
		t.Errorf("member approve: status=%d, want 403", r.status)
	}
	if r := ingressCall(t, http.MethodPatch, base+"/ingress/"+svc.ID, member, "", map[string]any{"enabled": true}); r.status != http.StatusForbidden {
		t.Errorf("member patch: status=%d, want 403", r.status)
	}
	appr := ingressCall(t, http.MethodPost, base+"/ingress/"+svc.ID+"/approve", admin, "", nil)
	if appr.status != http.StatusOK || !decodeIngressSvc(t, appr).Approved {
		t.Fatalf("admin approve: %d %s", appr.status, appr.body)
	}
	en := ingressCall(t, http.MethodPatch, base+"/ingress/"+svc.ID, admin, "", map[string]any{"enabled": true})
	enabled := decodeIngressSvc(t, en)
	if en.status != http.StatusOK || !enabled.Live || enabled.ACLRule != "tag:ingress -> "+mac.ip+":47800" {
		t.Fatalf("enable: %d %+v", en.status, enabled)
	}
	port := ingressCall(t, http.MethodPatch, base+"/ingress/"+svc.ID, admin, "", map[string]any{"target_port": 47801})
	moved := decodeIngressSvc(t, port)
	if port.status != http.StatusOK || moved.TargetPort != 47801 || moved.Approved || moved.Live {
		t.Fatalf("port change must reset approval: %d %+v", port.status, moved)
	}

	list := ingressCall(t, http.MethodGet, base+"/ingress", member, "", nil)
	if list.status != http.StatusOK || !strings.Contains(string(list.body), svc.ID) || !strings.Contains(string(list.body), `"suffix":"svc.example.test"`) {
		t.Fatalf("list: %d %s", list.status, list.body)
	}
	if r := ingressCall(t, http.MethodDelete, base+"/ingress/"+svc.ID, admin, "", nil); r.status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	if r := ingressCall(t, http.MethodDelete, base+"/ingress/"+svc.ID, admin, "", nil); r.status != http.StatusNotFound {
		t.Errorf("second delete: %d, want 404", r.status)
	}

	rows, err := f.admin.Query(context.Background(), `
		SELECT action FROM audit_log
		 WHERE tenant_id = (SELECT id FROM tenants WHERE slug = $1) AND action LIKE 'ingress.%'
		 ORDER BY occurred_at`, f.tenantSlug)
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	want := "ingress.create,ingress.approve,ingress.enable,ingress.update,ingress.delete"
	if got := strings.Join(actions, ","); got != want {
		t.Errorf("audit actions = %s, want %s", got, want)
	}
}

func TestRESTIngress_ForeignPeerIsNotFound(t *testing.T) {
	f := startFixture(t)
	foreignPeer, _ := registerVictimPeerInSeparateTenant(t, f, nil)
	r := ingressCall(t, http.MethodPost, f.httpURL+"/api/v1/peers/"+foreignPeer+"/ingress", "", f.tenantSlug,
		map[string]any{"label": "svc-" + uuid.NewString()[:8], "target_port": 47800})
	if r.status != http.StatusNotFound {
		t.Errorf("create on another tenant's peer: %d, want 404 (%s)", r.status, r.body)
	}
}

// TestRESTIngress_LivenessBumpsPolicyAndOpensACL checks the Task 2 seam end
// to end: a tenant with one authored rule denies ingress -> mac until the
// service is live, then the re-register response carries AllowedIPs both ways.
func TestRESTIngress_LivenessBumpsPolicyAndOpensACL(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	vps := registerIngressTestPeer(t, f, "vps")
	tagPeer(t, f, vps.id, "ingress")
	pol := ingressCall(t, http.MethodPut, f.httpURL+"/api/v1/policy", "", f.tenantSlug, map[string]any{
		"hclSource": "rule \"dev-to-db\" {\n  action = \"allow\"\n  sources = [\"tag:dev\"]\n  destinations = [\"tag:db:5432\"]\n}\n",
	})
	if pol.status != http.StatusOK {
		t.Fatalf("put policy: %d %s", pol.status, pol.body)
	}
	reRegister := func(peerID string) []byte {
		pub := getPeerPubKey(t, f, peerID)
		r := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
			"hostname": "x", "wireguardPublicKey": pub, "tenantSlug": f.tenantSlug,
		})
		if r.status != http.StatusOK {
			t.Fatalf("re-register: %d %s", r.status, r.body)
		}
		return r.body
	}
	if gwAllowedIPsContain(t, reRegister(vps.id), mac.id, mac.ip+"/32") {
		t.Fatal("ingress reaches mac before any service is live")
	}

	var revBefore int64
	_ = f.admin.QueryRow(context.Background(),
		`SELECT revision FROM acl_policies WHERE tenant_id = (SELECT id FROM tenants WHERE slug = $1)`, f.tenantSlug).Scan(&revBefore)

	base := f.httpURL + "/api/v1"
	svc := decodeIngressSvc(t, ingressCall(t, http.MethodPost, base+"/peers/"+mac.id+"/ingress", "", f.tenantSlug,
		map[string]any{"label": "svc-" + uuid.NewString()[:8], "target_port": 47800}))
	ingressCall(t, http.MethodPost, base+"/ingress/"+svc.ID+"/approve", "", f.tenantSlug, nil)
	ingressCall(t, http.MethodPatch, base+"/ingress/"+svc.ID, "", f.tenantSlug, map[string]any{"enabled": true})

	var revAfter int64
	_ = f.admin.QueryRow(context.Background(),
		`SELECT revision FROM acl_policies WHERE tenant_id = (SELECT id FROM tenants WHERE slug = $1)`, f.tenantSlug).Scan(&revAfter)
	if revAfter <= revBefore {
		t.Errorf("policy revision %d -> %d; enabling a live service must bump it", revBefore, revAfter)
	}
	if !gwAllowedIPsContain(t, reRegister(vps.id), mac.id, mac.ip+"/32") {
		t.Error("ingress peer's AllowedIPs for mac missing after the service went live")
	}
	if !gwAllowedIPsContain(t, reRegister(mac.id), vps.id, vps.ip+"/32") {
		t.Error("mac's AllowedIPs for the ingress peer missing (return path)")
	}

	ingressCall(t, http.MethodPatch, base+"/ingress/"+svc.ID, "", f.tenantSlug, map[string]any{"enabled": false})
	if gwAllowedIPsContain(t, reRegister(vps.id), mac.id, mac.ip+"/32") {
		t.Error("disabling the service must remove the generated rule")
	}
}

// TestRESTIngress_IngressPeerCannotBeTarget: the internet-facing ingress
// peer must not expose its own ports through itself. Filing a service on
// it, or approving one filed before it was tagged, answers 400.
func TestRESTIngress_IngressPeerCannotBeTarget(t *testing.T) {
	f := startFixture(t)
	vps := registerIngressTestPeer(t, f, "vps")
	base := f.httpURL + "/api/v1"
	created := ingressCall(t, http.MethodPost, base+"/peers/"+vps.id+"/ingress", "", f.tenantSlug,
		map[string]any{"label": "svc-" + uuid.NewString()[:8], "target_port": 47800})
	if created.status != http.StatusCreated {
		t.Fatalf("create before tagging: %d %s", created.status, created.body)
	}
	sid := decodeIngressSvc(t, created).ID
	tagPeer(t, f, vps.id, "ingress")

	r := ingressCall(t, http.MethodPost, base+"/peers/"+vps.id+"/ingress", "", f.tenantSlug,
		map[string]any{"label": "svc-" + uuid.NewString()[:8], "target_port": 47801})
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "tag:ingress") {
		t.Errorf("create on ingress peer: %d %s, want 400 naming tag:ingress", r.status, r.body)
	}
	r = ingressCall(t, http.MethodPost, base+"/ingress/"+sid+"/approve", "", f.tenantSlug, nil)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "tag:ingress") {
		t.Errorf("approve on ingress peer: %d %s, want 400 naming tag:ingress", r.status, r.body)
	}
}
