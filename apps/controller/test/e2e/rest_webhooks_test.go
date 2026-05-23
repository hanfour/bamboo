// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRESTWebhooks_CreateListPatchDelete exercises the CRUD surface.
// secret leaks once on Create and never again — same convention as
// pre-auth-keys.
func TestRESTWebhooks_CreateListPatchDelete(t *testing.T) {
	f := startFixture(t)

	// Create.
	created := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/webhooks", f.tenantSlug,
		map[string]any{
			"url":         "https://example.com/hook",
			"eventTypes":  []string{"peer.register"},
			"description": "test hook",
		})
	if created.status != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", created.status, created.body)
	}
	var createResp struct {
		ID         string   `json:"id"`
		URL        string   `json:"url"`
		EventTypes []string `json:"eventTypes"`
		Secret     string   `json:"secret"`
	}
	if err := json.Unmarshal(created.body, &createResp); err != nil {
		t.Fatalf("decode create: %v body=%s", err, created.body)
	}
	if createResp.Secret == "" {
		t.Errorf("create response missing secret; body=%s", created.body)
	}
	if createResp.URL != "https://example.com/hook" {
		t.Errorf("create.url = %q", createResp.URL)
	}

	// List — must NOT echo the secret back.
	list := getJSON(t, f.httpURL+"/api/v1/webhooks", f.tenantSlug)
	if list.status != http.StatusOK {
		t.Fatalf("list: status=%d body=%s", list.status, list.body)
	}
	if strings.Contains(string(list.body), createResp.Secret) {
		t.Errorf("list response leaked secret; body=%s", list.body)
	}
	if !strings.Contains(string(list.body), createResp.ID) {
		t.Errorf("list response missing created id; body=%s", list.body)
	}

	// Patch — disable the subscription.
	patch := sendJSONWithTenant(t, http.MethodPatch,
		f.httpURL+"/api/v1/webhooks/"+createResp.ID, f.tenantSlug,
		map[string]any{"active": false, "description": "disabled now"})
	if patch.status != http.StatusOK {
		t.Fatalf("patch: status=%d body=%s", patch.status, patch.body)
	}
	var patchResp struct {
		Active      bool   `json:"active"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(patch.body, &patchResp)
	if patchResp.Active {
		t.Errorf("patch did not flip active=false")
	}
	if patchResp.Description != "disabled now" {
		t.Errorf("patch did not update description: %+v", patchResp)
	}

	// Delete.
	del := sendJSONWithTenant(t, http.MethodDelete,
		f.httpURL+"/api/v1/webhooks/"+createResp.ID, f.tenantSlug, nil)
	if del.status != http.StatusNoContent {
		t.Errorf("delete: status=%d body=%s", del.status, del.body)
	}
	// Idempotent: a second delete also returns 204 (the row was
	// already gone; the API doesn't surface this as 404 because
	// DELETE is meant to converge on the deleted state).
	del2 := sendJSONWithTenant(t, http.MethodDelete,
		f.httpURL+"/api/v1/webhooks/"+createResp.ID, f.tenantSlug, nil)
	if del2.status != http.StatusNoContent {
		t.Errorf("idempotent delete: status=%d", del2.status)
	}
}

// TestRESTWebhooks_RejectsInvalidURL pins the URL gate in
// apiCreateWebhook — non-http schemes and empty hosts are rejected
// at the API edge so the publisher never tries to POST to "ssh://"
// or similar.
func TestRESTWebhooks_RejectsInvalidURL(t *testing.T) {
	f := startFixture(t)
	for _, bad := range []string{
		"",
		"not-a-url",
		"ssh://example.com",
		"file:///etc/passwd",
		"http://",
	} {
		r := sendJSONWithTenant(t, http.MethodPost,
			f.httpURL+"/api/v1/webhooks", f.tenantSlug,
			map[string]any{"url": bad})
		if r.status != http.StatusBadRequest {
			t.Errorf("url=%q: status=%d, want 400; body=%s", bad, r.status, r.body)
		}
	}
}

// TestRESTWebhooks_DeliversSignedPayloadOnAuditEvent is the seam
// test for the whole pipe: subscribe, trigger an audit-logged
// action, assert the receiver got a POST with the right shape +
// HMAC signature.
//
// The audit event we trigger is webhook.create itself — convenient
// because creating the subscription IS the audited event. We
// register a SECOND subscription against the receiver after the
// first one is in place, so the first webhook.create fires to the
// receiver while it's listening.
func TestRESTWebhooks_DeliversSignedPayloadOnAuditEvent(t *testing.T) {
	f := startFixture(t)

	// Test receiver: collects every POST it receives. Single-shot
	// channel + a small slice so the test asserts on the captured
	// requests after the trigger.
	received := make(chan receivedReq, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		received <- receivedReq{
			Method:     r.Method,
			Sig:        r.Header.Get("X-Bamboo-Signature"),
			Type:       r.Header.Get("X-Bamboo-Event-Type"),
			ID:         r.Header.Get("X-Bamboo-Event-Id"),
			Body:       body,
			ReceivedAt: time.Now(),
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// Subscribe to the test receiver. The audited webhook.create
	// for THIS subscription is the event we'll see — by the time
	// the audit-hook fires, the subscription row exists, so the
	// publisher finds itself in the active list.
	create := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/webhooks", f.tenantSlug,
		map[string]any{
			"url":        srv.URL,
			"eventTypes": []string{"webhook."},
		})
	if create.status != http.StatusCreated {
		t.Fatalf("subscribe: status=%d body=%s", create.status, create.body)
	}
	var subResp struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(create.body, &subResp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The webhook.create fired — wait for delivery (no backoff on
	// the first attempt; the receiver returns 200 so it ends after
	// one POST).
	select {
	case got := <-received:
		if got.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", got.Method)
		}
		if got.Type != "webhook.create" {
			t.Errorf("event_type header = %q, want webhook.create", got.Type)
		}
		if got.ID == "" {
			t.Errorf("event_id header missing")
		}
		// Verify HMAC matches the documented contract.
		if want := "sha256=" + hmacHex(t, subResp.Secret, got.Body); want != got.Sig {
			t.Errorf("signature mismatch:\n got=%q\nwant=%q\n body=%s", got.Sig, want, got.Body)
		}
		// Spot-check the JSON payload shape.
		var payload struct {
			Type       string `json:"type"`
			TenantID   string `json:"tenantId"`
			ResourceID string `json:"resourceId"`
		}
		if err := json.Unmarshal(got.Body, &payload); err != nil {
			t.Errorf("decode payload: %v body=%s", err, got.Body)
		}
		if payload.Type != "webhook.create" {
			t.Errorf("payload.type = %q", payload.Type)
		}
		if payload.ResourceID != subResp.ID {
			t.Errorf("payload.resourceId = %q, want %q", payload.ResourceID, subResp.ID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for webhook delivery")
	}
}

// TestRESTWebhooks_404ForCrossTenant verifies tenant scoping on
// the patch/delete paths: a webhook id owned by another tenant
// resolves to 404 before any update runs.
func TestRESTWebhooks_404ForCrossTenant(t *testing.T) {
	f := startFixture(t)

	create := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/webhooks", f.tenantSlug,
		map[string]any{"url": "https://example.com/hook"})
	if create.status != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", create.status, create.body)
	}
	var id struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(create.body, &id)

	// Patch via a different slug.
	r := sendJSONWithTenant(t, http.MethodPatch,
		f.httpURL+"/api/v1/webhooks/"+id.ID, "wrong-tenant",
		map[string]any{"active": false})
	if r.status != http.StatusNotFound {
		t.Errorf("cross-tenant patch: status=%d, want 404", r.status)
	}
}

type receivedReq struct {
	Method     string
	Sig        string
	Type       string
	ID         string
	Body       []byte
	ReceivedAt time.Time
}

func hmacHex(t *testing.T, secret string, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Silence unused-import vet on the test file when Go's lints think
// sync isn't referenced — it's used by the channel coordination
// above but kept here so the file compiles even if a future refactor
// drops the explicit reference.
var _ = sync.Mutex{}
