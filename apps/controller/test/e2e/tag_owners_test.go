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

	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// TestTagOwners_AdminWithoutOwnershipIsForbidden verifies that an
// admin whose email isn't in a tag's owner list gets 403 on PATCH
// /api/v1/peers/{id} when trying to assign that tag (issue #139).
// Tag owners is RBAC on top of the existing admin gate, so a
// non-owner admin still has admin powers for everything else.
func TestTagOwners_AdminWithoutOwnershipIsForbidden(t *testing.T) {
	f := startFixture(t)
	f.enableRequireAuth()

	// Mint two admin JWTs — alice (owns tag:dev) + bob (does not).
	aliceTok, aliceID := f.mintJWTWithUser(t, true)
	bobTok, _ := f.mintJWTWithUser(t, true)

	// Set alice's email so the policy's tagOwners list matches.
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE users SET email='alice@example.com' WHERE id=$1`, aliceID); err != nil {
		t.Fatalf("set alice email: %v", err)
	}

	// Register a peer using alice's bearer (auto-approves under
	// bearer admin path).
	peerID := registerPeerWithBearer(t, f, "kiosk", aliceTok)

	// Write the policy that owns tag:dev to alice only.
	if _, err := f.policy.PutPolicy(f.outgoingCtx(context.Background()),
		&bamboov1.PutPolicyRequest{
			HclSource: `
tagOwners = {
  "tag:dev" = ["alice@example.com"]
}

rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`,
		}); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}

	// Bob (admin but not in tagOwners) tries to add tag:dev → 403.
	resp := patchPeerWithBearer(t, f, peerID, bobTok, map[string]any{"tags": []string{"dev"}})
	if resp.status != http.StatusForbidden {
		t.Errorf("bob PATCH tag:dev → status=%d, want 403; body=%s", resp.status, resp.body)
	}

	// Alice (admin AND in tagOwners) succeeds.
	resp = patchPeerWithBearer(t, f, peerID, aliceTok, map[string]any{"tags": []string{"dev"}})
	if resp.status != http.StatusOK {
		t.Errorf("alice PATCH tag:dev → status=%d, want 200; body=%s", resp.status, resp.body)
	}
}

// TestTagOwners_UnlistedTagFreelyAssignable verifies the back-compat
// path — a tag not declared in tagOwners is freely assignable by any
// admin, so ad-hoc tagging (the existing pre-#139 workflow) keeps
// working.
func TestTagOwners_UnlistedTagFreelyAssignable(t *testing.T) {
	f := startFixture(t)
	f.enableRequireAuth()

	aliceTok, aliceID := f.mintJWTWithUser(t, true)
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE users SET email='alice@example.com' WHERE id=$1`, aliceID); err != nil {
		t.Fatalf("set alice email: %v", err)
	}
	bobTok, _ := f.mintJWTWithUser(t, true)

	peerID := registerPeerWithBearer(t, f, "kiosk", aliceTok)

	if _, err := f.policy.PutPolicy(f.outgoingCtx(context.Background()),
		&bamboov1.PutPolicyRequest{
			HclSource: `
tagOwners = {
  "tag:dev" = ["alice@example.com"]
}

rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`,
		}); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}

	// Bob assigns tag:experimental — not in tagOwners → OK.
	resp := patchPeerWithBearer(t, f, peerID, bobTok, map[string]any{"tags": []string{"experimental"}})
	if resp.status != http.StatusOK {
		t.Errorf("bob PATCH tag:experimental → status=%d, want 200 (unlisted tag); body=%s", resp.status, resp.body)
	}
}

// TestTagOwners_RemovalAlsoGated verifies a removal of a restricted
// tag requires the same ownership as adding — admin without
// ownership cannot strip a tag from a peer either, otherwise the
// audit trail would lie.
func TestTagOwners_RemovalAlsoGated(t *testing.T) {
	f := startFixture(t)
	f.enableRequireAuth()

	aliceTok, aliceID := f.mintJWTWithUser(t, true)
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE users SET email='alice@example.com' WHERE id=$1`, aliceID); err != nil {
		t.Fatalf("set alice email: %v", err)
	}
	bobTok, _ := f.mintJWTWithUser(t, true)

	peerID := registerPeerWithBearer(t, f, "kiosk", aliceTok)

	// Alice tags the peer with dev FIRST (no policy yet ⇒ allowed).
	if r := patchPeerWithBearer(t, f, peerID, aliceTok, map[string]any{"tags": []string{"dev"}}); r.status != http.StatusOK {
		t.Fatalf("alice initial tag set: %d body=%s", r.status, r.body)
	}

	// Now author policy restricting tag:dev to alice.
	if _, err := f.policy.PutPolicy(f.outgoingCtx(context.Background()),
		&bamboov1.PutPolicyRequest{
			HclSource: `
tagOwners = {
  "tag:dev" = ["alice@example.com"]
}

rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`,
		}); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}

	// Bob tries to remove tag:dev (set tags = []) → 403.
	resp := patchPeerWithBearer(t, f, peerID, bobTok, map[string]any{"tags": []string{}})
	if resp.status != http.StatusForbidden {
		t.Errorf("bob remove tag:dev → status=%d, want 403; body=%s", resp.status, resp.body)
	}
}

// --- helpers ------------------------------------------------------

// registerPeerWithBearer registers a peer using the given bearer
// token in the Authorization header (issue #133's auto-approve path
// fires when the bearer is a user-session JWT, so this peer lands
// in 'approved').
func registerPeerWithBearer(t *testing.T, f *fixture, hostname, bearer string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"hostname":           hostname,
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	req, _ := http.NewRequest(http.MethodPost, f.httpURL+"/api/v1/peers/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()
	rbody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register %s status=%d body=%s", hostname, resp.StatusCode, rbody)
	}
	var got struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	if err := json.Unmarshal(rbody, &got); err != nil {
		t.Fatalf("decode register: %v body=%s", err, rbody)
	}
	return got.Self.ID
}

// patchPeerWithBearer issues a PATCH with the given bearer token,
// returning the same jsonResponse shape the other helpers use.
func patchPeerWithBearer(t *testing.T, f *fixture, peerID, bearer string, payload any) jsonResponse {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPatch, f.httpURL+"/api/v1/peers/"+peerID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	cl := http.Client{Timeout: 10 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("PATCH peer: %v", err)
	}
	defer resp.Body.Close()
	rbody, _ := io.ReadAll(resp.Body)
	return jsonResponse{status: resp.StatusCode, body: rbody}
}
