// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
)

// TestPeerApproval_PendingByDefaultUnderManualKey verifies that a
// peer registered via a pre-auth key with auto_approve=false (the new
// safe default) lands in approval_status='pending'. The register
// response carries an empty Peers list — the pending peer must not
// see the rest of the tailnet until an admin approves.
func TestPeerApproval_PendingByDefaultUnderManualKey(t *testing.T) {
	f := startFixture(t)

	// Existing peer in the tenant — has to exist BEFORE the pending
	// register call so we can verify the response peer list is empty
	// for the pending peer (vs the trivially-empty "no other peers"
	// case that would also pass the assertion).
	existing := registerPeerDevFallback(t, f, "exists-already", randomPubKey(t))

	// Mint a manual-approval key. AutoApprove omitted = false by
	// default — that's the lever this test pins.
	keySecret := mintPreAuthKeyREST(t, f, false)

	// Register a new peer with the manual key.
	body := map[string]any{
		"hostname":           "kiosk-1",
		"wireguardPublicKey": randomPubKey(t),
		"os":                 "linux",
		"clientVersion":      "0.0.1",
		"preAuthKeySecret":         keySecret,
		"tenantSlug":         f.tenantSlug,
	}
	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", body)
	if resp.status != http.StatusOK {
		t.Fatalf("register status = %d, body=%s", resp.status, resp.body)
	}

	var got struct {
		Self struct {
			ID             string `json:"id"`
			ApprovalStatus string `json:"approvalStatus"`
		} `json:"self"`
		Peers []map[string]any `json:"peers"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp.body)
	}
	if got.Self.ApprovalStatus != "pending" {
		t.Errorf("Self.ApprovalStatus = %q, want pending", got.Self.ApprovalStatus)
	}
	// Pending peer must not see other peers.
	if len(got.Peers) != 0 {
		t.Errorf("pending peer sees %d peers, want 0 (incl. existing %s)", len(got.Peers), existing)
	}
}

// TestPeerApproval_AutoApproveKeySkipsQueue verifies that a pre-auth
// key minted with auto_approve=true lands its registered peer in the
// approved state directly. This is the CI / kiosk workflow opt-out
// from the manual queue.
func TestPeerApproval_AutoApproveKeySkipsQueue(t *testing.T) {
	f := startFixture(t)

	keySecret := mintPreAuthKeyREST(t, f, true)

	body := map[string]any{
		"hostname":           "ci-runner-1",
		"wireguardPublicKey": randomPubKey(t),
		"preAuthKeySecret":         keySecret,
		"tenantSlug":         f.tenantSlug,
	}
	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", body)
	if resp.status != http.StatusOK {
		t.Fatalf("register status = %d, body=%s", resp.status, resp.body)
	}
	var got struct {
		Self struct {
			ApprovalStatus string `json:"approvalStatus"`
		} `json:"self"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp.body)
	}
	if got.Self.ApprovalStatus != "approved" {
		t.Errorf("Self.ApprovalStatus = %q, want approved", got.Self.ApprovalStatus)
	}
}

// TestPeerApproval_DevFallbackAutoApproves verifies the require_auth=
// false path: peers registered without any credential land approved.
// This preserves the `make local-up + make local-bootstrap` UX:
// nothing in the dev-mode flow requires an admin click before the
// first peer can join the mesh.
func TestPeerApproval_DevFallbackAutoApproves(t *testing.T) {
	f := startFixture(t)

	id := registerPeerDevFallback(t, f, "local-1", randomPubKey(t))

	row := getPeerJSON(t, f, id)
	if row.ApprovalStatus != "approved" {
		t.Errorf("ApprovalStatus = %q, want approved (dev-fallback)", row.ApprovalStatus)
	}
}

// TestPeerApproval_ApproveFlow drives the full pending → approve →
// visible-to-others sequence the Web UI exercises.
func TestPeerApproval_ApproveFlow(t *testing.T) {
	f := startFixture(t)

	// First peer (existing tailnet) — auto-approve via dev-fallback so
	// it's part of the visible set.
	firstPubKey := randomPubKey(t)
	first := registerPeerDevFallback(t, f, "existing", firstPubKey)

	// Second peer — manual key, lands pending.
	keySecret := mintPreAuthKeyREST(t, f, false)
	second := registerPeerWithKey(t, f, "new-laptop", keySecret)

	// Verify second is pending.
	if got := getPeerJSON(t, f, second).ApprovalStatus; got != "pending" {
		t.Fatalf("pre-approve ApprovalStatus = %q, want pending", got)
	}

	// Admin approves via POST /api/v1/peers/{id}/approve.
	adminTok := f.mintJWT(t, true)
	req, _ := http.NewRequest(http.MethodPost, f.httpURL+"/api/v1/peers/"+second+"/approve", nil)
	req.Header.Set("Authorization", "Bearer "+adminTok)
	respHTTP, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("approve POST: %v", err)
	}
	defer respHTTP.Body.Close()
	bodyBytes, _ := io.ReadAll(respHTTP.Body)
	if respHTTP.StatusCode != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", respHTTP.StatusCode, string(bodyBytes))
	}
	var approvedRow struct {
		ID             string `json:"id"`
		ApprovalStatus string `json:"approvalStatus"`
		ApprovedAt     string `json:"approvedAt"`
	}
	if err := json.Unmarshal(bodyBytes, &approvedRow); err != nil {
		t.Fatalf("decode approve response: %v body=%s", err, bodyBytes)
	}
	if approvedRow.ApprovalStatus != "approved" {
		t.Errorf("approved.ApprovalStatus = %q, want approved", approvedRow.ApprovalStatus)
	}
	if approvedRow.ApprovedAt == "" {
		t.Errorf("approvedAt empty after approve")
	}

	// audit row written.
	var auditCount int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE action='peer.approve' AND resource_id = $1`,
		second).Scan(&auditCount); err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("peer.approve audit rows = %d, want 1", auditCount)
	}

	// After approval the second peer's next register sees the first
	// peer in its Peers list.
	rebody := map[string]any{
		"hostname":           "new-laptop",
		"wireguardPublicKey": getPeerPubKey(t, f, second),
		"tenantSlug":         f.tenantSlug,
	}
	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", rebody)
	if resp.status != http.StatusOK {
		t.Fatalf("re-register status = %d, body=%s", resp.status, resp.body)
	}
	var rec struct {
		Peers []map[string]any `json:"peers"`
	}
	if err := json.Unmarshal(resp.body, &rec); err != nil {
		t.Fatalf("decode re-register: %v body=%s", err, resp.body)
	}
	if len(rec.Peers) != 1 {
		t.Errorf("approved peer sees %d others, want 1 (existing %s)", len(rec.Peers), first)
	}
}

// TestPeerApproval_RejectFlow exercises the reject path. After
// rejection: peer never visible to others; re-approve attempt is a
// 409 (rejected is terminal — admin must delete + re-register).
func TestPeerApproval_RejectFlow(t *testing.T) {
	f := startFixture(t)

	keySecret := mintPreAuthKeyREST(t, f, false)
	peerID := registerPeerWithKey(t, f, "suspicious-1", keySecret)

	adminTok := f.mintJWT(t, true)

	// Reject.
	req, _ := http.NewRequest(http.MethodPost, f.httpURL+"/api/v1/peers/"+peerID+"/reject", nil)
	req.Header.Set("Authorization", "Bearer "+adminTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("reject POST: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reject status=%d body=%s", resp.StatusCode, string(body))
	}

	if got := getPeerJSON(t, f, peerID).ApprovalStatus; got != "rejected" {
		t.Errorf("post-reject ApprovalStatus = %q, want rejected", got)
	}

	// Second approve attempt now → 409 conflict.
	req2, _ := http.NewRequest(http.MethodPost, f.httpURL+"/api/v1/peers/"+peerID+"/approve", nil)
	req2.Header.Set("Authorization", "Bearer "+adminTok)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("re-approve POST: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("re-approve after reject status=%d, want 409", resp2.StatusCode)
	}
}

// TestPeerApproval_RejectsNonAdmin verifies the approve/reject
// endpoints honor the admin gate. Non-admin JWT → 403.
func TestPeerApproval_RejectsNonAdmin(t *testing.T) {
	f := startFixture(t)

	keySecret := mintPreAuthKeyREST(t, f, false)
	peerID := registerPeerWithKey(t, f, "pending-row", keySecret)

	memberTok := f.mintJWT(t, false)
	req, _ := http.NewRequest(http.MethodPost, f.httpURL+"/api/v1/peers/"+peerID+"/approve", nil)
	req.Header.Set("Authorization", "Bearer "+memberTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("approve POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin approve status=%d, want 403", resp.StatusCode)
	}
}

// --- helpers -------------------------------------------------------

type peerJSONShape struct {
	ID             string `json:"id"`
	Hostname       string `json:"hostname"`
	ApprovalStatus string `json:"approvalStatus"`
}

// mintPreAuthKeyREST creates a pre-auth key via the REST mint
// endpoint (distinct from the same-tenant direct-SQL helper in
// prod_onboarding_gate_test.go — that one bypasses the JSON layer
// and can't exercise the autoApprove request field).
// autoApprove controls the new column wired in this PR.
func mintPreAuthKeyREST(t *testing.T, f *fixture, autoApprove bool) string {
	t.Helper()
	resp := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/preauth-keys", f.tenantSlug,
		map[string]any{
			"description": "approval-test",
			"reusable":    true,
			"autoApprove": autoApprove,
		})
	if resp.status != http.StatusOK {
		t.Fatalf("mint preauth-key status=%d body=%s", resp.status, resp.body)
	}
	var k struct {
		Secret      string `json:"secret"`
		AutoApprove bool   `json:"autoApprove"`
	}
	if err := json.Unmarshal(resp.body, &k); err != nil {
		t.Fatalf("decode mint: %v body=%s", err, resp.body)
	}
	if k.AutoApprove != autoApprove {
		t.Fatalf("AutoApprove echo: got %v, want %v", k.AutoApprove, autoApprove)
	}
	if k.Secret == "" {
		t.Fatalf("mint returned empty secret: %s", resp.body)
	}
	return k.Secret
}

// registerPeerDevFallback registers a peer via the X-Tenant-Slug
// dev-fallback path (no credential, no require_auth). Returns the
// peer.id from the response.
func registerPeerDevFallback(t *testing.T, f *fixture, hostname, pubKey string) string {
	t.Helper()
	body := map[string]any{
		"hostname":           hostname,
		"wireguardPublicKey": pubKey,
		"tenantSlug":         f.tenantSlug,
	}
	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", body)
	if resp.status != http.StatusOK {
		t.Fatalf("register %s status=%d body=%s", hostname, resp.status, resp.body)
	}
	var got struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode register: %v body=%s", err, resp.body)
	}
	return got.Self.ID
}

func registerPeerWithKey(t *testing.T, f *fixture, hostname, keySecret string) string {
	t.Helper()
	body := map[string]any{
		"hostname":           hostname,
		"wireguardPublicKey": randomPubKey(t),
		"preAuthKeySecret":         keySecret,
		"tenantSlug":         f.tenantSlug,
	}
	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", body)
	if resp.status != http.StatusOK {
		t.Fatalf("register %s status=%d body=%s", hostname, resp.status, resp.body)
	}
	var got struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode register: %v body=%s", err, resp.body)
	}
	return got.Self.ID
}

// getPeerJSON fetches /api/v1/peers/{id} and decodes the subset of
// fields the approval tests care about. The fixture uses the
// X-Tenant-Slug fallback so the admin doesn't need a JWT — the
// approval tests are about state transitions, not auth coverage
// (which lives in require_auth_test.go).
func getPeerJSON(t *testing.T, f *fixture, peerID string) peerJSONShape {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, f.httpURL+"/api/v1/peers/"+peerID, nil)
	req.Header.Set("X-Tenant-Slug", f.tenantSlug)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get peer: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get peer %s status=%d body=%s", peerID, resp.StatusCode, body)
	}
	var p peerJSONShape
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode get peer: %v body=%s", err, body)
	}
	return p
}

// getPeerPubKey reaches into the DB (rather than a JSON shape that
// might omit pubkey) so a re-register call in the same test can carry
// the same key and trigger the FindByPubKey idempotency path.
func getPeerPubKey(t *testing.T, f *fixture, peerID string) string {
	t.Helper()
	var pubKey string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT wireguard_public_key FROM peers WHERE id = $1`,
		peerID).Scan(&pubKey); err != nil {
		t.Fatalf("query pubkey for %s: %v", peerID, err)
	}
	return pubKey
}

// postJSON / sendJSONWithTenant live in rest_peers_test.go and
// rest_preauth_test.go respectively; the linker complains if we
// re-declare. We use them as-is. The intentionally-unused stub below
// keeps gofmt + import bookkeeping consistent if future refactors
// move the helpers.
var _ = bytes.NewReader
var _ = fmt.Sprintf
