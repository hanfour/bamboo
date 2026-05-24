// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestRESTAPITokens_MintListRevoke covers the CRUD surface. Token
// plaintext leaks once on mint and never again — same convention
// as pre-auth keys and webhook secrets.
func TestRESTAPITokens_MintListRevoke(t *testing.T) {
	f := startFixture(t)

	mint := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/api-tokens", f.tenantSlug,
		map[string]any{"name": "github-actions", "description": "ci runner"})
	if mint.status != http.StatusCreated {
		t.Fatalf("mint: status=%d body=%s", mint.status, mint.body)
	}
	var minted struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(mint.body, &minted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if minted.Token == "" || !strings.HasPrefix(minted.Token, "bat_") {
		t.Errorf("mint missing bat_ token; body=%s", mint.body)
	}
	if minted.Name != "github-actions" {
		t.Errorf("name = %q", minted.Name)
	}

	list := getJSON(t, f.httpURL+"/api/v1/api-tokens", f.tenantSlug)
	if list.status != http.StatusOK {
		t.Fatalf("list: status=%d body=%s", list.status, list.body)
	}
	if strings.Contains(string(list.body), minted.Token) {
		t.Errorf("list LEAKED token; body=%s", list.body)
	}
	if !strings.Contains(string(list.body), minted.ID) {
		t.Errorf("list missing minted id; body=%s", list.body)
	}

	revoke := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/api-tokens/"+minted.ID+"/revoke", f.tenantSlug, nil)
	if revoke.status != http.StatusNoContent {
		t.Errorf("revoke: status=%d body=%s", revoke.status, revoke.body)
	}
	// Idempotent: second revoke also 204.
	revoke2 := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/api-tokens/"+minted.ID+"/revoke", f.tenantSlug, nil)
	if revoke2.status != http.StatusNoContent {
		t.Errorf("idempotent revoke: status=%d", revoke2.status)
	}
}

// TestRESTAPITokens_AuthenticatesAsAdminBearer is the seam test
// for the authn middleware: a freshly-minted token, presented as
// Authorization: Bearer bat_..., authenticates a subsequent admin
// request. Without this the token would mint but not actually
// be usable.
//
// We mint via the tenant-slug dev path, then use the token for a
// follow-up admin operation (listing webhooks) on the SAME tenant
// without the slug header — the token's tenant_id pin is what
// resolves the tenant.
func TestRESTAPITokens_AuthenticatesAsAdminBearer(t *testing.T) {
	f := startFixture(t)

	mint := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/api-tokens", f.tenantSlug,
		map[string]any{"name": "ci"})
	if mint.status != http.StatusCreated {
		t.Fatalf("mint: status=%d body=%s", mint.status, mint.body)
	}
	var minted struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(mint.body, &minted)

	// Use the token as Bearer — no X-Tenant-Slug. The middleware
	// must resolve the tenant from the token's own tenant_id and
	// authorize the admin call.
	req, _ := http.NewRequest(http.MethodGet, f.httpURL+"/api/v1/webhooks", nil)
	req.Header.Set("Authorization", "Bearer "+minted.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /webhooks: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Bearer bat_ authn: status=%d, want 200", resp.StatusCode)
	}
}

// TestRESTAPITokens_RevokedTokenDeniedAfterRevoke pins the revoke
// path's user-visible effect: a token that authenticates fine,
// gets revoked, then immediately stops authenticating. Without
// this the revoke endpoint would succeed but the token would
// keep working until cache miss.
func TestRESTAPITokens_RevokedTokenDeniedAfterRevoke(t *testing.T) {
	f := startFixture(t)

	mint := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/api-tokens", f.tenantSlug,
		map[string]any{"name": "soon-revoked"})
	var minted struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	_ = json.Unmarshal(mint.body, &minted)

	// Sanity: works before revoke.
	if !apiTokenWorks(t, f.httpURL, minted.Token) {
		t.Fatal("token didn't authenticate immediately after mint")
	}
	rev := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/api-tokens/"+minted.ID+"/revoke", f.tenantSlug, nil)
	if rev.status != http.StatusNoContent {
		t.Fatalf("revoke: status=%d body=%s", rev.status, rev.body)
	}
	if apiTokenWorks(t, f.httpURL, minted.Token) {
		t.Error("revoked token still authenticated; want 401")
	}
}

// TestRESTAPITokens_RejectsMalformedBearer pins the classifier:
// a Bearer bat_ token that fails to parse or has a wrong secret
// returns 401, NOT a fall-through to dev-tenant mode that might
// silently succeed in the test harness.
func TestRESTAPITokens_RejectsMalformedBearer(t *testing.T) {
	f := startFixture(t)
	for _, bad := range []string{
		"bat_malformed",
		"bat_aaaaaaaaaaaaaaaaaaaaaaaa_zzzzzzzzzzzzzzzzzzzzzz", // valid-shape but unknown id
	} {
		req, _ := http.NewRequest(http.MethodGet, f.httpURL+"/api/v1/webhooks", nil)
		req.Header.Set("Authorization", "Bearer "+bad)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /webhooks: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("bad token %q: status=%d, want 401", bad, resp.StatusCode)
		}
	}
}

func apiTokenWorks(t *testing.T, baseURL, token string) bool {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/webhooks", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
