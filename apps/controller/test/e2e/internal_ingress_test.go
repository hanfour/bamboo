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

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
)

// issueIngressToken gives the fixture a fresh signing key and issues a
// token for peerID through the admin REST endpoint (dev-fallback admin).
func issueIngressToken(t *testing.T, f *fixture, peerID string) string {
	t.Helper()
	seed, _, err := auth.GenerateRelayKeypair()
	if err != nil {
		t.Fatal(err)
	}
	key, err := auth.ParseRelaySigningKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	f.httpAPI.SetIngressConfig(key, "svc.example.test")
	r := ingressCall(t, http.MethodPost, f.httpURL+"/api/v1/ingress/token", "", f.tenantSlug, map[string]any{"peer_id": peerID})
	if r.status != http.StatusCreated {
		t.Fatalf("issue token: %d %s", r.status, r.body)
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(r.body, &out)
	return out.Token
}

func internalGet(t *testing.T, url, token, ifNoneMatch string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

// makeLabeledLiveService creates, approves, and enables a service via REST
// and returns its id and label (makeLiveService in ingress_liveness_test.go
// returns only the id).
func makeLabeledLiveService(t *testing.T, f *fixture, peerID string) (id, label string) {
	t.Helper()
	label = "svc-" + uuid.NewString()[:8]
	base := f.httpURL + "/api/v1"
	svc := decodeIngressSvc(t, ingressCall(t, http.MethodPost, base+"/peers/"+peerID+"/ingress", "", f.tenantSlug,
		map[string]any{"label": label, "target_port": 47800}))
	if r := ingressCall(t, http.MethodPost, base+"/ingress/"+svc.ID+"/approve", "", f.tenantSlug, nil); r.status != http.StatusOK {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	if r := ingressCall(t, http.MethodPatch, base+"/ingress/"+svc.ID, "", f.tenantSlug, map[string]any{"enabled": true}); r.status != http.StatusOK {
		t.Fatalf("enable: %d %s", r.status, r.body)
	}
	return svc.ID, label
}

func TestInternalIngress_TokenIssueRequiresIngressPeer(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	seed, _, _ := auth.GenerateRelayKeypair()
	key, _ := auth.ParseRelaySigningKey(seed)

	// No key configured ⇒ 503.
	if r := ingressCall(t, http.MethodPost, f.httpURL+"/api/v1/ingress/token", "", f.tenantSlug, map[string]any{"peer_id": mac.id}); r.status != http.StatusServiceUnavailable {
		t.Errorf("unconfigured: %d, want 503", r.status)
	}
	f.httpAPI.SetIngressConfig(key, "svc.example.test")
	// Untagged peer ⇒ 400.
	if r := ingressCall(t, http.MethodPost, f.httpURL+"/api/v1/ingress/token", "", f.tenantSlug, map[string]any{"peer_id": mac.id}); r.status != http.StatusBadRequest {
		t.Errorf("untagged peer: %d, want 400 (%s)", r.status, r.body)
	}
}

func TestInternalIngress_AskAndRoutes(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	vps := registerIngressTestPeer(t, f, "vps")
	tagPeer(t, f, vps.id, "ingress")
	token := issueIngressToken(t, f, vps.id)
	svcID, label := makeLabeledLiveService(t, f, mac.id)

	ask := f.httpURL + "/internal/v1/ingress/ask?domain="
	if resp, _ := internalGet(t, ask+label+".svc.example.test", "", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("ask live: %d, want 200", resp.StatusCode)
	}
	for _, d := range []string{"nope-" + uuid.NewString()[:6] + ".svc.example.test", label + ".other.test", ""} {
		if resp, _ := internalGet(t, ask+d, "", ""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("ask %q: %d, want 404", d, resp.StatusCode)
		}
	}

	routesURL := f.httpURL + "/internal/v1/ingress/routes"
	if resp, _ := internalGet(t, routesURL, "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("routes without token: %d, want 401", resp.StatusCode)
	}
	resp, body := internalGet(t, routesURL, token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("routes: %d %s", resp.StatusCode, body)
	}
	var routes []struct {
		ServiceID  string `json:"service_id"`
		Label      string `json:"label"`
		PeerIP     string `json:"peer_ip"`
		TargetPort int    `json:"target_port"`
	}
	if err := json.Unmarshal(body, &routes); err != nil {
		t.Fatalf("decode routes: %v %s", err, body)
	}
	if len(routes) != 1 || routes[0].ServiceID != svcID || routes[0].Label != label || routes[0].PeerIP != mac.ip || routes[0].TargetPort != 47800 {
		t.Fatalf("routes = %+v", routes)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("routes response has no ETag")
	}
	if resp2, _ := internalGet(t, routesURL, token, etag); resp2.StatusCode != http.StatusNotModified {
		t.Errorf("If-None-Match: %d, want 304", resp2.StatusCode)
	}

	// Disable ⇒ ask 404, ETag changes, routes empty.
	ingressCall(t, http.MethodPatch, f.httpURL+"/api/v1/ingress/"+svcID, "", f.tenantSlug, map[string]any{"enabled": false})
	if resp, _ := internalGet(t, ask+label+".svc.example.test", "", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("ask after disable: %d, want 404", resp.StatusCode)
	}
	resp3, body3 := internalGet(t, routesURL, token, etag)
	if resp3.StatusCode != http.StatusOK || string(body3) != "[]" {
		t.Errorf("routes after disable: %d %s, want 200 []", resp3.StatusCode, body3)
	}

	// Rotation revokes the old token.
	newToken := issueIngressTokenSameKey(t, f, vps.id)
	if resp, _ := internalGet(t, routesURL, token, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("rotated-out token: %d, want 401", resp.StatusCode)
	}
	if resp, _ := internalGet(t, routesURL, newToken, ""); resp.StatusCode != http.StatusOK {
		t.Errorf("new token: %d, want 200", resp.StatusCode)
	}
}

// TestInternalIngress_RoutesAreTenantScoped: a token only sees its tenant.
func TestInternalIngress_RoutesAreTenantScoped(t *testing.T) {
	a := startFixture(t)
	macA := registerIngressTestPeer(t, a, "mac-a")
	makeLabeledLiveService(t, a, macA.id)

	b := startFixture(t)
	vpsB := registerIngressTestPeer(t, b, "vps-b")
	tagPeer(t, b, vpsB.id, "ingress")
	tokenB := issueIngressToken(t, b, vpsB.id)
	resp, body := internalGet(t, b.httpURL+"/internal/v1/ingress/routes", tokenB, "")
	if resp.StatusCode != http.StatusOK || string(body) != "[]" {
		t.Errorf("tenant B routes = %d %s, want 200 []", resp.StatusCode, body)
	}
}

// issueIngressTokenSameKey re-issues with the key already on the fixture.
func issueIngressTokenSameKey(t *testing.T, f *fixture, peerID string) string {
	t.Helper()
	r := ingressCall(t, http.MethodPost, f.httpURL+"/api/v1/ingress/token", "", f.tenantSlug, map[string]any{"peer_id": peerID})
	if r.status != http.StatusCreated {
		t.Fatalf("re-issue: %d %s", r.status, r.body)
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(r.body, &out)
	return out.Token
}

// TestInternalIngress_AskStrictDomain: ask folds case and one trailing dot,
// accepts exactly one label under the suffix, and leaks nothing in bodies.
func TestInternalIngress_AskStrictDomain(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	vps := registerIngressTestPeer(t, f, "vps")
	tagPeer(t, f, vps.id, "ingress")
	issueIngressToken(t, f, vps.id) // ask answers live only with a current token
	_, label := makeLabeledLiveService(t, f, mac.id)
	base := f.httpURL + "/internal/v1/ingress/ask"

	resp, body := internalGet(t, base+"?domain="+strings.ToUpper(label)+".SVC.Example.Test.", "", "")
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Errorf("ask mixed case + trailing dot: %d %q, want 200 ok", resp.StatusCode, body)
	}
	for name, q := range map[string]string{
		"missing param":     "",
		"two labels":        "?domain=a." + label + ".svc.example.test",
		"bare suffix":       "?domain=svc.example.test",
		"other domain":      "?domain=" + label + ".example.test",
		"two trailing dots": "?domain=" + label + ".svc.example.test..",
		"oversized":         "?domain=" + strings.Repeat("a", 4096) + ".svc.example.test",
	} {
		resp, body := internalGet(t, base+q, "", "")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("ask %s: %d, want 404", name, resp.StatusCode)
		}
		if strings.Contains(string(body), label) || strings.Contains(string(body), f.tenantSlug) || strings.Contains(string(body), mac.id) {
			t.Errorf("ask %s: body leaks details: %q", name, body)
		}
	}
	if r := ingressCall(t, http.MethodPost, base+"?domain="+label+".svc.example.test", "", "", nil); r.status != http.StatusMethodNotAllowed {
		t.Errorf("POST ask: %d, want 405", r.status)
	}
}

// TestInternalIngress_RoutesETagAndStorage: 304 carries no body, the ETag
// is a strong validator over the response bytes, and neither
// ingress_tokens nor audit_log ever hold the token plaintext.
func TestInternalIngress_RoutesETagAndStorage(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	vps := registerIngressTestPeer(t, f, "vps")
	tagPeer(t, f, vps.id, "ingress")
	token := issueIngressToken(t, f, vps.id)
	makeLabeledLiveService(t, f, mac.id)
	routesURL := f.httpURL + "/internal/v1/ingress/routes"

	resp, body := internalGet(t, routesURL, token, "")
	etag := resp.Header.Get("ETag")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(etag, `"`) || strings.HasPrefix(etag, "W/") {
		t.Fatalf("routes: %d etag=%q", resp.StatusCode, etag)
	}
	if again, body2 := internalGet(t, routesURL, token, ""); again.Header.Get("ETag") != etag || string(body2) != string(body) {
		t.Errorf("unchanged table: etag %q body %s, want %q %s", again.Header.Get("ETag"), body2, etag, body)
	}
	nm, nmBody := internalGet(t, routesURL, token, `"other", `+etag)
	if nm.StatusCode != http.StatusNotModified || len(nmBody) != 0 {
		t.Errorf("If-None-Match list: %d body=%q, want 304 empty", nm.StatusCode, nmBody)
	}

	sig := token[strings.LastIndex(token, ".")+1:]
	var n int
	if err := f.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM ingress_tokens t WHERE t::text LIKE '%' || $1 || '%'`, sig).Scan(&n); err != nil || n != 0 {
		t.Errorf("ingress_tokens contains token material: n=%d err=%v", n, err)
	}
	if err := f.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log a WHERE a::text LIKE '%' || $1 || '%'`, sig).Scan(&n); err != nil || n != 0 {
		t.Errorf("audit_log contains token material: n=%d err=%v", n, err)
	}
}

// TestInternalIngress_TokenRejectedWhenPeerIneligible: the current token
// stops working once its ingress peer loses tag:ingress, is un-approved,
// or is deleted.
func TestInternalIngress_TokenRejectedWhenPeerIneligible(t *testing.T) {
	f := startFixture(t)
	vps := registerIngressTestPeer(t, f, "vps")
	tagPeer(t, f, vps.id, "ingress")
	token := issueIngressToken(t, f, vps.id)
	routesURL := f.httpURL + "/internal/v1/ingress/routes"
	expect := func(step string, want int) {
		t.Helper()
		if resp, _ := internalGet(t, routesURL, token, ""); resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", step, resp.StatusCode, want)
		}
	}
	expect("fresh token", http.StatusOK)
	tagPeer(t, f, vps.id)
	expect("tag removed", http.StatusUnauthorized)
	tagPeer(t, f, vps.id, "ingress")
	expect("tag restored", http.StatusOK)
	ctx := context.Background()
	if _, err := f.admin.Exec(ctx, `UPDATE peers SET approval_status = 'pending' WHERE id = $1`, vps.id); err != nil {
		t.Fatal(err)
	}
	expect("peer un-approved", http.StatusUnauthorized)
	if _, err := f.admin.Exec(ctx, `UPDATE peers SET approval_status = 'approved' WHERE id = $1`, vps.id); err != nil {
		t.Fatal(err)
	}
	expect("peer re-approved", http.StatusOK)
	if _, err := f.admin.Exec(ctx, `DELETE FROM peers WHERE id = $1`, vps.id); err != nil {
		t.Fatal(err)
	}
	expect("peer deleted", http.StatusUnauthorized)
	expect2 := func(name, tok string) {
		t.Helper()
		if resp, _ := internalGet(t, routesURL, tok, ""); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, resp.StatusCode)
		}
	}
	expect2("garbage", "x.y")
	expect2("tampered", token+"A")
}

func postIngressEvents(t *testing.T, url, token string, payload any) jsonResponse {
	t.Helper()
	buf, ok := payload.([]byte)
	if !ok {
		buf, _ = json.Marshal(payload)
	}
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST events: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return jsonResponse{status: resp.StatusCode, body: body}
}

func ingressEvent(sid string) map[string]any {
	return map[string]any{"service_id": sid, "client_ip": "203.0.113.7", "status": 200,
		"bytes_in": 10, "bytes_out": 20, "duration_ms": 5, "occurred_at": time.Now().UTC()}
}

func TestInternalIngress_EventsAcceptsOnlyOwnServices(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	vps := registerIngressTestPeer(t, f, "vps")
	tagPeer(t, f, vps.id, "ingress")
	token := issueIngressToken(t, f, vps.id)
	svcID := makeLiveService(t, f, mac.id)
	url := f.httpURL + "/internal/v1/ingress/events"
	ev := ingressEvent

	if r := postIngressEvents(t, url, "", map[string]any{"events": []any{ev(svcID)}}); r.status != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", r.status)
	}
	r := postIngressEvents(t, url, token, map[string]any{"events": []any{ev(svcID), ev(uuid.NewString()), ev("not-a-uuid")}})
	if r.status != http.StatusAccepted || string(bytes.TrimSpace(r.body)) != `{"accepted":1}` {
		t.Errorf("events: %d %s, want 202 {\"accepted\":1}", r.status, r.body)
	}
	tooMany := make([]any, 1001)
	for i := range tooMany {
		tooMany[i] = ev(svcID)
	}
	if r := postIngressEvents(t, url, token, map[string]any{"events": tooMany}); r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("1001 events: %d, want 413", r.status)
	}
}

// TestInternalIngress_EventsStrictPayload: the client cannot pick the
// tenant or peer, and cannot smuggle extra fields (headers, bodies).
func TestInternalIngress_EventsStrictPayload(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	vps := registerIngressTestPeer(t, f, "vps")
	tagPeer(t, f, vps.id, "ingress")
	token := issueIngressToken(t, f, vps.id)
	svcID := makeLiveService(t, f, mac.id)
	url := f.httpURL + "/internal/v1/ingress/events"

	with := func(k string, v any) map[string]any {
		e := ingressEvent(svcID)
		e[k] = v
		return map[string]any{"events": []any{e}}
	}
	bad := map[string]any{
		"event tenant_id":     with("tenant_id", uuid.NewString()),
		"event peer_id":       with("peer_id", uuid.NewString()),
		"event authorization": with("authorization", "Bearer x"),
		"event body":          with("body", "secret"),
		"top-level tenant_id": map[string]any{"tenant_id": uuid.NewString(), "events": []any{ingressEvent(svcID)}},
		"wrong type":          with("status", "200"),
		"trailing data":       []byte(`{"events":[]} {"events":[]}`),
		"not an object":       []byte(`[1,2]`),
	}
	for name, payload := range bad {
		if r := postIngressEvents(t, url, token, payload); r.status != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, r.status, r.body)
		}
	}
	huge := []byte(`{"events":[],"pad":"` + strings.Repeat("a", 1<<20) + `"}`)
	if r := postIngressEvents(t, url, token, huge); r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("over 1 MiB: %d, want 413", r.status)
	}
	if r := postIngressEvents(t, url, token, map[string]any{"events": []any{}}); r.status != http.StatusAccepted || string(bytes.TrimSpace(r.body)) != `{"accepted":0}` {
		t.Errorf("empty batch: %d %s, want 202 {\"accepted\":0}", r.status, r.body)
	}
}

// TestInternalIngress_EventsTenantScoped: tenant B's token cannot report
// requests for tenant A's service.
func TestInternalIngress_EventsTenantScoped(t *testing.T) {
	a := startFixture(t)
	macA := registerIngressTestPeer(t, a, "mac-a")
	svcA := makeLiveService(t, a, macA.id)

	b := startFixture(t)
	vpsB := registerIngressTestPeer(t, b, "vps-b")
	tagPeer(t, b, vpsB.id, "ingress")
	tokenB := issueIngressToken(t, b, vpsB.id)
	r := postIngressEvents(t, b.httpURL+"/internal/v1/ingress/events", tokenB, map[string]any{"events": []any{ingressEvent(svcA)}})
	if r.status != http.StatusAccepted || string(bytes.TrimSpace(r.body)) != `{"accepted":0}` {
		t.Errorf("cross-tenant: %d %s, want 202 {\"accepted\":0}", r.status, r.body)
	}
}

// TestInternalIngress_AskRequiresIngressToken: a live service in a tenant
// that never issued an ingress token gets no certificate.
func TestInternalIngress_AskRequiresIngressToken(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	vps := registerIngressTestPeer(t, f, "vps")
	tagPeer(t, f, vps.id, "ingress")
	f.httpAPI.SetIngressConfig(nil, "svc.example.test")
	_, label := makeLabeledLiveService(t, f, mac.id)
	ask := f.httpURL + "/internal/v1/ingress/ask?domain=" + label + ".svc.example.test"
	if resp, _ := internalGet(t, ask, "", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("ask without token: %d, want 404", resp.StatusCode)
	}
	issueIngressToken(t, f, vps.id)
	if resp, _ := internalGet(t, ask, "", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("ask with token: %d, want 200", resp.StatusCode)
	}
}
