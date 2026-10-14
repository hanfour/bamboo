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

	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
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

const e2eSessionSecret = "e2e-secret-with-at-least-32-bytes-padding"

// TestRESTHeartbeat_RenewsPeerSessionInsideWindow is the long-running
// agent contract: a peer session inside the renewal window comes back
// as a fresh token on heartbeat, and a token with most of its life
// left is left alone.
func TestRESTHeartbeat_RenewsPeerSessionInsideWindow(t *testing.T) {
	f := startFixture(t)
	pub := randomPubKey(t)
	peerID, token, _ := registerPeerOverREST(t, f, pub)
	claims, err := auth.VerifyPeerSessionToken([]byte(e2eSessionSecret), token)
	if err != nil {
		t.Fatalf("verify register token: %v", err)
	}
	short, err := auth.IssuePeerSessionToken([]byte(e2eSessionSecret), auth.PeerSessionClaims{
		TenantID:    claims.TenantID,
		PeerID:      claims.PeerID,
		WGPublicKey: claims.WGPublicKey,
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue short token: %v", err)
	}

	f.enableRequireAuth()

	fresh := postHeartbeat(t, f, peerID, token)
	if fresh.PeerSessionToken != "" {
		t.Fatalf("heartbeat renewed a token that still has about 24h; body token set")
	}

	renewed := postHeartbeat(t, f, peerID, short)
	if renewed.PeerSessionToken == "" || renewed.PeerSessionToken == short {
		t.Fatalf("expected a replacement peerSessionToken, got %q", renewed.PeerSessionToken)
	}
	if renewed.PeerSessionExpiresAt < time.Now().Add(20*time.Hour).Unix() {
		t.Errorf("renewed exp %d is not a fresh 24h token", renewed.PeerSessionExpiresAt)
	}
	got, err := auth.VerifyPeerSessionToken([]byte(e2eSessionSecret), renewed.PeerSessionToken)
	if err != nil {
		t.Fatalf("verify renewed token: %v", err)
	}
	if got.PeerID.String() != peerID || got.WGPublicKey != claims.WGPublicKey {
		t.Errorf("renewed claims = %+v, want peer %s key %s", got, peerID, claims.WGPublicKey)
	}

	again := postHeartbeat(t, f, peerID, renewed.PeerSessionToken)
	if again.PeerSessionToken != "" {
		t.Fatal("a just-renewed token was rotated again on the next heartbeat")
	}
}

// TestRESTRegister_PeerSessionReusesSamePeer proves a live peer
// session re-registers that peer after its single-use pre-auth key
// has already been consumed, and cannot enroll a different public key.
func TestRESTRegister_PeerSessionReusesSamePeer(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())
	created, err := f.auth.CreatePreAuthKey(ctx, &bamboov1.CreatePreAuthKeyRequest{
		Description: "peer-session-reregister",
		Reusable:    false,
	})
	if err != nil {
		t.Fatalf("CreatePreAuthKey: %v", err)
	}
	pub := randomPubKey(t)
	first := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "once",
		"wireguardPublicKey": pub,
		"os":                 "linux",
		"preAuthKeySecret":   created.GetSecret(),
		"tenantSlug":         f.tenantSlug,
	})
	if first.status != http.StatusOK {
		t.Fatalf("first register status = %d, body=%s", first.status, first.body)
	}
	var seeded struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
		PeerSessionToken string `json:"peerSessionToken"`
	}
	if err := json.Unmarshal(first.body, &seeded); err != nil {
		t.Fatalf("decode first register: %v", err)
	}
	if seeded.PeerSessionToken == "" || seeded.Self.ID == "" {
		t.Fatalf("seed register missing id or token: %s", first.body)
	}

	f.enableRequireAuth()

	againBody, _ := json.Marshal(map[string]any{
		"hostname":           "once",
		"wireguardPublicKey": pub,
		"os":                 "linux",
		"preAuthKeySecret":   created.GetSecret(),
	})
	again := postJSONBearer(t, f.httpURL+"/api/v1/peers/register", seeded.PeerSessionToken, againBody)
	if again.status != http.StatusOK {
		t.Fatalf("re-register with peer session status = %d, body=%s", again.status, again.body)
	}
	var second struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
		PeerSessionToken string `json:"peerSessionToken"`
	}
	if err := json.Unmarshal(again.body, &second); err != nil {
		t.Fatalf("decode re-register: %v", err)
	}
	if second.Self.ID != seeded.Self.ID {
		t.Errorf("re-register id = %s, want %s", second.Self.ID, seeded.Self.ID)
	}
	if second.PeerSessionToken == "" {
		t.Fatal("re-register did not mint a peer session token")
	}

	otherBody, _ := json.Marshal(map[string]any{
		"hostname":           "other",
		"wireguardPublicKey": randomPubKey(t),
		"os":                 "linux",
	})
	other := postJSONBearer(t, f.httpURL+"/api/v1/peers/register", seeded.PeerSessionToken, otherBody)
	if other.status != http.StatusForbidden {
		t.Fatalf("peer session enrolling a new key status = %d, body=%s, want 403", other.status, other.body)
	}
}

type heartbeatTokenResponse struct {
	PeerSessionToken     string `json:"peerSessionToken"`
	PeerSessionExpiresAt int64  `json:"peerSessionExpiresAt"`
}

func postHeartbeat(t *testing.T, f *fixture, peerID, token string) heartbeatTokenResponse {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"peerId": peerID})
	resp := postJSONBearer(t, f.httpURL+"/api/v1/peers/heartbeat", token, body)
	if resp.status != http.StatusOK {
		t.Fatalf("heartbeat status = %d, body=%s", resp.status, resp.body)
	}
	var out heartbeatTokenResponse
	if err := json.Unmarshal(resp.body, &out); err != nil {
		t.Fatalf("decode heartbeat: %v body=%s", err, resp.body)
	}
	return out
}

func postJSONBearer(t *testing.T, url, token string, body []byte) jsonResponse {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return jsonResponse{status: resp.StatusCode, body: raw}
}
