// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// TestRESTCreatePreAuthKey_HappyPath POSTs the create endpoint,
// verifies the plaintext secret is returned, and confirms a
// preauthkey.create audit row was written.
func TestRESTCreatePreAuthKey_HappyPath(t *testing.T) {
	f := startFixture(t)

	resp := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/preauth-keys", f.tenantSlug,
		map[string]any{
			"description": "han's iphone",
			"reusable":    false,
			"ephemeral":   false,
		})
	if resp.status != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.status, resp.body)
	}
	var key struct {
		ID          string `json:"id"`
		Description string `json:"description"`
		Reusable    bool   `json:"reusable"`
		Secret      string `json:"secret"`
		CreatedAt   string `json:"createdAt"`
	}
	if err := json.Unmarshal(resp.body, &key); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp.body)
	}
	if key.ID == "" {
		t.Error("id missing")
	}
	if key.Description != "han's iphone" {
		t.Errorf("description = %q, want han's iphone", key.Description)
	}
	if key.Reusable {
		t.Error("reusable should default to false")
	}
	// The secret format is opaque to the API; just confirm it's
	// non-empty and long enough to plausibly be a random token.
	// The actual format check lives in auth/preauthkey_test.go.
	if len(key.Secret) < 16 {
		t.Errorf("secret too short: %d chars", len(key.Secret))
	}

	// audit_log row recorded.
	var count int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE action='preauthkey.create' AND resource_id = $1`,
		key.ID).Scan(&count); err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if count != 1 {
		t.Errorf("audit rows = %d, want 1", count)
	}
}

// TestRESTCreatePreAuthKey_RejectsGet verifies the route is POST-only.
func TestRESTCreatePreAuthKey_RejectsGet(t *testing.T) {
	f := startFixture(t)
	got := getJSON(t, f.httpURL+"/api/v1/preauth-keys", f.tenantSlug)
	if got.status != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405; body=%s", got.status, got.body)
	}
}

// TestRESTCreatePreAuthKey_RejectsNonAdmin verifies the auth gate:
// when the request carries a session JWT for a non-admin user,
// the handler returns 403 before any DB write. The dev-fallback
// path (no JWT) is still allowed — that's covered by the happy-
// path test above and is a deliberate design choice so local
// dev without OIDC keeps working.
func TestRESTCreatePreAuthKey_RejectsNonAdmin(t *testing.T) {
	f := startFixture(t)
	ctx := context.Background()

	// Resolve the tenant the way the dev-fallback would, so the
	// user's TenantID matches what apiCreatePreAuthKey will see.
	tenants := repo.NewTenants(f.pool)
	tenant, err := tenants.GetOrCreate(ctx, f.tenantSlug, "Default Tenant", "100.64.0.0/24")
	if err != nil {
		t.Fatalf("get tenant: %v", err)
	}

	// Create a non-admin user (IsAdmin defaults to false).
	users := repo.NewUsers(f.pool)
	user, err := users.UpsertOIDC(ctx, &repo.User{
		TenantID:     tenant.ID,
		Email:        "non-admin@example.com",
		DisplayName:  "Not An Admin",
		OIDCProvider: "test",
		OIDCSubject:  uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("UpsertOIDC: %v", err)
	}

	// Mint a JWT with the e2e fixture's known secret (see
	// buildHTTPMux in setup_test.go).
	tok, err := auth.IssueSessionToken(
		[]byte("e2e-secret-with-at-least-32-bytes-padding"),
		auth.SessionClaims{UserID: user.ID, TenantID: tenant.ID},
		1*time.Hour,
	)
	if err != nil {
		t.Fatalf("IssueSessionToken: %v", err)
	}

	// POST with Authorization: Bearer — bypasses the dev-fallback
	// path entirely, so the handler must enforce IsAdmin.
	body, _ := json.Marshal(map[string]any{"description": "should be rejected"})
	req, _ := http.NewRequest(http.MethodPost, f.httpURL+"/api/v1/preauth-keys", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body=%s", resp.StatusCode, respBody)
	}
}

// TestRESTCreatePreAuthKey_DefaultsForEmptyBody allows minting a
// key with no fields set; description is empty, reusable + ephemeral
// default to false.
func TestRESTCreatePreAuthKey_DefaultsForEmptyBody(t *testing.T) {
	f := startFixture(t)
	resp := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/preauth-keys", f.tenantSlug,
		map[string]any{})
	if resp.status != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.status, resp.body)
	}
	var key struct {
		Description string `json:"description"`
		Reusable    bool   `json:"reusable"`
		Ephemeral   bool   `json:"ephemeral"`
		Secret      string `json:"secret"`
	}
	_ = json.Unmarshal(resp.body, &key)
	if key.Description != "" {
		t.Errorf("description = %q, want empty", key.Description)
	}
	if key.Reusable || key.Ephemeral {
		t.Errorf("flags should default false; got reusable=%v ephemeral=%v", key.Reusable, key.Ephemeral)
	}
	if key.Secret == "" {
		t.Error("secret missing")
	}
}
