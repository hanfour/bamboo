// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// TestRequireAuth_DevModeStillAcceptsHeaderFallback is the regression
// guard for the default config path. With SetRequireAuth left off, the
// X-Tenant-Slug header is accepted and GetOrCreate auto-provisions the
// tenant — exactly what `make local-up` relies on today.
func TestRequireAuth_DevModeStillAcceptsHeaderFallback(t *testing.T) {
	f := startFixture(t)

	resp := sendJSONWithTenant(t, http.MethodGet,
		f.httpURL+"/api/v1/peers", f.tenantSlug, nil)
	if resp.status != http.StatusOK {
		t.Errorf("dev fallback should still succeed: status=%d body=%s",
			resp.status, resp.body)
	}
}

// TestRequireAuth_RejectsHeaderFallbackInProdMode confirms that once
// SetRequireAuth(true) flips the gate, the same request — JWT-less,
// only X-Tenant-Slug — gets a 401 instead of silently provisioning a
// tenant. This is the headline P0-2 outcome.
func TestRequireAuth_RejectsHeaderFallbackInProdMode(t *testing.T) {
	f := startFixture(t)
	f.enableRequireAuth()

	resp := sendJSONWithTenant(t, http.MethodGet,
		f.httpURL+"/api/v1/peers", f.tenantSlug, nil)
	if resp.status != http.StatusUnauthorized {
		t.Errorf("prod mode unauthenticated should 401: status=%d body=%s",
			resp.status, resp.body)
	}
}

// TestRequireAuth_AcceptsBearerInProdMode is the positive case: with a
// valid session JWT, the request goes through under prod-mode auth.
func TestRequireAuth_AcceptsBearerInProdMode(t *testing.T) {
	f := startFixture(t)
	f.enableRequireAuth()

	tok := f.mintJWT(t, false)

	req, _ := http.NewRequest(http.MethodGet, f.httpURL+"/api/v1/peers", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/peers: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status=%d body=%s, want 200", resp.StatusCode, body)
	}
}

// TestRequireAuth_MeRemainsAccessibleUnauthenticated keeps the Web's
// signed-out landing path working: /api/v1/me must respond 200 with
// authenticated:false even when require_auth is on. Without this, the
// Web's auth-aware header would never load.
func TestRequireAuth_MeRemainsAccessibleUnauthenticated(t *testing.T) {
	f := startFixture(t)
	f.enableRequireAuth()

	resp := sendJSONWithTenant(t, http.MethodGet,
		f.httpURL+"/api/v1/me", f.tenantSlug, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", resp.status, resp.body)
	}
	var me struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := json.Unmarshal(resp.body, &me); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp.body)
	}
	if me.Authenticated {
		t.Errorf("authenticated = true, want false (no JWT was provided)")
	}
}

// TestRequireAuth_PeerRegisterAcceptsUserSessionJWT is the regression
// guard for the OIDC sign-in flow. Without the REST adapter
// propagating the Authorization Bearer into the gRPC
// RegisterRequest's BearerToken credential, the coordinator's own
// require_auth gate rejected the call with
//
//	"Register requires a pre-auth key or bearer credential"
//
// even though the REST gate above had already authenticated the
// JWT. That presented as a confusing "failing about pre-auth" on
// the macOS app right after a successful Sign in with Google.
func TestRequireAuth_PeerRegisterAcceptsUserSessionJWT(t *testing.T) {
	f := startFixture(t)
	f.enableRequireAuth()

	tok := f.mintJWT(t, false)

	body, _ := json.Marshal(map[string]any{
		"hostname":           "oidc-registered-peer",
		"wireguardPublicKey": randomPubKey(t),
	})
	req, _ := http.NewRequest(http.MethodPost,
		f.httpURL+"/api/v1/peers/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/v1/peers/register: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s, want 200", resp.StatusCode, respBody)
	}
}

// TestRequireAuth_PeerRegisterStillAcceptsPreAuthKey confirms the peer
// onboarding path is unaffected by prod-mode auth. The pre-auth-key is
// validated by the handler itself; the require_auth gate only covers
// the JWT-or-401 read+admin REST surface.
func TestRequireAuth_PeerRegisterStillAcceptsPreAuthKey(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())

	// Mint a key under dev fallback first so we have a credential
	// to use after flipping the gate.
	created, err := f.auth.CreatePreAuthKey(ctx, &bamboov1.CreatePreAuthKeyRequest{
		Description: "require-auth-test",
		Reusable:    false,
	})
	if err != nil {
		t.Fatalf("CreatePreAuthKey: %v", err)
	}

	f.enableRequireAuth()

	body, _ := json.Marshal(map[string]any{
		"preAuthKeySecret":   created.GetSecret(),
		"hostname":           "registered-after-gate",
		"wireguardPublicKey": randomPubKey(t),
	})
	req, _ := http.NewRequest(http.MethodPost,
		f.httpURL+"/api/v1/peers/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/v1/peers/register: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Errorf("status=%d body=%s, want 200 (pre-auth-key path must remain open)",
			resp.StatusCode, respBody)
	}
}
