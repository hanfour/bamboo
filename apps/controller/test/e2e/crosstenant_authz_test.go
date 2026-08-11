// SPDX-License-Identifier: AGPL-3.0-or-later

// Cross-tenant authorization regression tests for the peer-id-scoped
// REST endpoints (/peers/watch, /peers/heartbeat).
//
// These endpoints are dispatched BEFORE the routeAPI auth gate and
// self-police via HTTPServer.peerCredentialStatus. Under prod-mode
// (require_auth=true) that helper accepts ANY valid user-session JWT
// without comparing the JWT's tenant to the tenant that owns the
// target peerId (see api.go peerCredentialStatus: the trailing
// `authenticate(r); if authn.claims != nil { return true }` branch).
// SubscribePeer / Heartbeat then act on the *named* peer's tenant.
//
// Net effect: a user authenticated in tenant A can read tenant B's
// live netmap (watch) and rewrite tenant B's WireGuard endpoints
// (heartbeat). These tests encode the DESIRED contract — cross-tenant
// access must be refused (401/403/404) — so they FAIL against the
// current code, pinning the break until it is fixed. The existing
// TestRESTGetPeer_CrossTenantIsolation etc. only cover the GET/PATCH/
// DELETE handlers, which is why this gap shipped untested.
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
)

// registerVictimPeerInSeparateTenant registers a peer in a fresh tenant
// (distinct from f.tenantSlug) via the dev-fallback register path, which
// must run BEFORE require_auth is flipped on. Returns the peer id + slug.
func registerVictimPeerInSeparateTenant(t *testing.T, f *fixture, endpoints []string) (peerID, slug string) {
	t.Helper()
	slug = "e2e-victim-" + uuid.NewString()[:8]
	t.Cleanup(func() { cleanupTenant(f.pool, slug) })
	payload := map[string]any{
		"hostname":           "victim-peer",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         slug,
	}
	if len(endpoints) > 0 {
		payload["endpoints"] = endpoints
	}
	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", payload)
	if reg.status != http.StatusOK {
		t.Fatalf("register victim peer: status=%d body=%s", reg.status, reg.body)
	}
	var out struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	if err := json.Unmarshal(reg.body, &out); err != nil {
		t.Fatalf("decode victim register: %v", err)
	}
	if out.Self.ID == "" {
		t.Fatalf("victim register returned empty peer id; body=%s", reg.body)
	}
	return out.Self.ID, slug
}

// getStatusWithBearer issues a GET carrying a session JWT and returns the
// response status only. The watch handler flushes its 200 + SSE headers
// and then streams indefinitely, so we bound the request with a context
// deadline and read only the status line — a secured handler returns its
// 401/403/404 before any streaming begins.
func getStatusWithBearer(t *testing.T, url, bearer string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// postJSONWithBearer issues a POST with a session JWT + JSON body.
func postJSONWithBearer(t *testing.T, url, bearer string, payload any) jsonResponse {
	t.Helper()
	buf, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("build POST %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return jsonResponse{status: resp.StatusCode, body: body}
}

// TestCrossTenant_WatchStreamRejectsForeignPeer: a user-session JWT from
// tenant A must NOT be able to open the watch stream for a peer that
// belongs to tenant B. Currently returns 200 and streams tenant B's
// netmap events → the assertion below fails, which is the point.
func TestCrossTenant_WatchStreamRejectsForeignPeer(t *testing.T) {
	f := startFixture(t)

	victimPeerID, _ := registerVictimPeerInSeparateTenant(t, f, nil)

	// Prod-mode auth, then an attacker JWT minted in a DIFFERENT tenant
	// (f.tenantSlug). mintJWTWithUser creates a non-admin user there.
	f.enableRequireAuth()
	attackerJWT, _ := f.mintJWTWithUser(t, false)

	status := getStatusWithBearer(t,
		f.httpURL+"/api/v1/peers/watch?peerId="+victimPeerID, attackerJWT)

	if status != http.StatusNotFound {
		t.Errorf("CROSS-TENANT READ: tenant-A user JWT opened the watch stream "+
			"for tenant-B peer %s (status=%d, want 404 — same not-found response "+
			"as GET /peers/{id}, so a foreign caller can't probe peer existence). "+
			"peerCredentialStatus must bind the JWT to the target peer's tenant.",
			victimPeerID, status)
	}
}

// TestCrossTenant_HeartbeatRejectsForeignPeerEndpoints: a user-session
// JWT from tenant A must NOT be able to drive heartbeat — and thus
// rewrite WireGuard endpoints — on a peer owned by tenant B.
func TestCrossTenant_HeartbeatRejectsForeignPeerEndpoints(t *testing.T) {
	f := startFixture(t)

	const legitEndpoint = "203.0.113.10:51820"
	const poisonEndpoint = "198.51.100.66:51820" // attacker-controlled
	victimPeerID, _ := registerVictimPeerInSeparateTenant(t, f, []string{legitEndpoint})

	f.enableRequireAuth()
	attackerJWT, _ := f.mintJWTWithUser(t, false)

	resp := postJSONWithBearer(t, f.httpURL+"/api/v1/peers/heartbeat", attackerJWT, map[string]any{
		"peerId":    victimPeerID,
		"endpoints": []string{poisonEndpoint},
	})
	if resp.status != http.StatusNotFound {
		t.Errorf("CROSS-TENANT WRITE: tenant-A user JWT drove heartbeat on tenant-B "+
			"peer %s (status=%d, want 404).", victimPeerID, resp.status)
	}

	// Data proof: the victim's endpoints must be untouched. Best-effort —
	// if the array scan isn't supported we still have the status assertion.
	var eps []string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT endpoints FROM peers WHERE id = $1`, victimPeerID).Scan(&eps); err == nil {
		for _, e := range eps {
			if e == poisonEndpoint {
				t.Errorf("CROSS-TENANT WRITE: victim peer %s endpoints were poisoned "+
					"across the tenant boundary: %v", victimPeerID, eps)
			}
		}
	}
}
