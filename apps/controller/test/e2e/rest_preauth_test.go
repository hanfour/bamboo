// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
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
