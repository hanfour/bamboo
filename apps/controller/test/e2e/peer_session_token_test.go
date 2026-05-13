// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// TestRESTRegister_IssuesPeerSessionToken verifies the REST register
// response now includes a peer session token + expiry so clients can
// hold a peer-bound credential without trusting an unauthenticated
// tenant slug header on subsequent calls.
func TestRESTRegister_IssuesPeerSessionToken(t *testing.T) {
	f := startFixture(t)

	body := map[string]any{
		"hostname":           "rest-peer-session",
		"wireguardPublicKey": randomPubKey(t),
		"os":                 "darwin",
		"tenantSlug":         f.tenantSlug,
	}
	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", body)
	if resp.status != http.StatusOK {
		t.Fatalf("register status = %d, body=%s", resp.status, resp.body)
	}

	var got struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
		PeerSessionToken     string `json:"peerSessionToken"`
		PeerSessionExpiresAt int64  `json:"peerSessionExpiresAt"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp.body)
	}
	if got.PeerSessionToken == "" {
		t.Fatalf("expected non-empty peerSessionToken; body=%s", resp.body)
	}
	if got.PeerSessionExpiresAt <= time.Now().Unix() {
		t.Errorf("peerSessionExpiresAt %d should be in the future", got.PeerSessionExpiresAt)
	}
	if got.Self.ID == "" {
		t.Errorf("missing self.id; body=%s", resp.body)
	}
}

// TestRESTRelayToken_AcceptsPeerSessionBearer registers a peer over
// REST and then mints a relay token using the peer-session bearer
// instead of the X-Tenant-Slug header path. This is the credential
// channel the prod-mode gate (follow-up PR) will rely on.
func TestRESTRelayToken_AcceptsPeerSessionBearer(t *testing.T) {
	f := startFixture(t)

	pubKey := randomPubKey(t)
	regResp := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "relay-peer",
		"wireguardPublicKey": pubKey,
		"os":                 "darwin",
		"tenantSlug":         f.tenantSlug,
	})
	if regResp.status != http.StatusOK {
		t.Fatalf("register status = %d, body=%s", regResp.status, regResp.body)
	}
	var reg struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
		PeerSessionToken string `json:"peerSessionToken"`
	}
	if err := json.Unmarshal(regResp.body, &reg); err != nil {
		t.Fatalf("decode register: %v", err)
	}
	if reg.PeerSessionToken == "" {
		t.Fatalf("expected peerSessionToken; body=%s", regResp.body)
	}

	// Mint a relay token using only the peer-session bearer — no
	// X-Tenant-Slug header. The handler must trust the token's
	// embedded tenant_id over the absent slug.
	mintBody, _ := json.Marshal(map[string]any{
		"peerId":             reg.Self.ID,
		"wireguardPublicKey": pubKey,
	})
	req, err := http.NewRequest(http.MethodPost, f.httpURL+"/api/v1/relay-token", bytes.NewReader(mintBody))
	if err != nil {
		t.Fatalf("build relay-token request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+reg.PeerSessionToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST relay-token: %v", err)
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("relay-token status = %d, body=%s", resp.StatusCode, bodyBytes)
	}
	var rt struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.Unmarshal(bodyBytes, &rt); err != nil {
		t.Fatalf("decode relay-token: %v; body=%s", err, bodyBytes)
	}
	if rt.Token == "" {
		t.Errorf("relay-token response missing token; body=%s", bodyBytes)
	}
}
