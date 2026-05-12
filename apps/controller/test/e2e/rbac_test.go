// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// withBearer attaches the given JWT to the outgoing gRPC context.
func withBearer(ctx context.Context, tok string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
}

// ---- gRPC PutPolicy admin gate -------------------------------------------

func TestGRPCAdminGate_PutPolicy_RejectsNonAdmin(t *testing.T) {
	f := startFixture(t)
	tok := f.mintJWT(t, false)

	ctx := withBearer(f.outgoingCtx(context.Background()), tok)
	_, err := f.policy.PutPolicy(ctx, &bamboov1.PutPolicyRequest{
		HclSource: `rule "x" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("non-admin PutPolicy should be PermissionDenied, got code=%v err=%v",
			status.Code(err), err)
	}
}

func TestGRPCAdminGate_PutPolicy_AcceptsAdmin(t *testing.T) {
	f := startFixture(t)
	tok := f.mintJWT(t, true)

	ctx := withBearer(f.outgoingCtx(context.Background()), tok)
	if _, err := f.policy.PutPolicy(ctx, &bamboov1.PutPolicyRequest{
		HclSource: `rule "x" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`,
	}); err != nil {
		t.Errorf("admin PutPolicy should succeed, got err=%v", err)
	}
}

func TestGRPCAdminGate_PutPolicy_DevFallback(t *testing.T) {
	f := startFixture(t)
	// No JWT — dev fallback path. require_auth is off in the fixture
	// so the gate logs a warn and allows.
	ctx := f.outgoingCtx(context.Background())
	if _, err := f.policy.PutPolicy(ctx, &bamboov1.PutPolicyRequest{
		HclSource: `rule "x" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`,
	}); err != nil {
		t.Errorf("dev-fallback PutPolicy should succeed, got err=%v", err)
	}
}

// ---- gRPC CreatePreAuthKey admin gate ------------------------------------

func TestGRPCAdminGate_CreatePreAuthKey_RejectsNonAdmin(t *testing.T) {
	f := startFixture(t)
	tok := f.mintJWT(t, false)

	ctx := withBearer(f.outgoingCtx(context.Background()), tok)
	_, err := f.auth.CreatePreAuthKey(ctx, &bamboov1.CreatePreAuthKeyRequest{
		Description: "should be rejected",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("non-admin CreatePreAuthKey should be PermissionDenied, got code=%v err=%v",
			status.Code(err), err)
	}
}

func TestGRPCAdminGate_RevokePreAuthKey_RejectsNonAdmin(t *testing.T) {
	f := startFixture(t)

	// Mint a key under dev fallback so we have something to attempt revoking.
	created, err := f.auth.CreatePreAuthKey(f.outgoingCtx(context.Background()),
		&bamboov1.CreatePreAuthKeyRequest{Description: "to revoke"})
	if err != nil {
		t.Fatalf("CreatePreAuthKey: %v", err)
	}

	tok := f.mintJWT(t, false)
	ctx := withBearer(f.outgoingCtx(context.Background()), tok)
	_, err = f.auth.RevokePreAuthKey(ctx, &bamboov1.RevokePreAuthKeyRequest{Id: created.GetKey().GetId()})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("non-admin RevokePreAuthKey should be PermissionDenied, got code=%v err=%v",
			status.Code(err), err)
	}
}

// ---- REST PATCH/DELETE peers admin gate ----------------------------------

func TestRESTAdminGate_PatchPeer_RejectsNonAdmin(t *testing.T) {
	f := startFixture(t)

	// Register a peer under dev fallback.
	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "to-rename",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	if resp.status != http.StatusOK {
		t.Fatalf("register: status=%d body=%s", resp.status, resp.body)
	}
	var reg struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	_ = json.Unmarshal(resp.body, &reg)

	tok := f.mintJWT(t, false)
	body, _ := json.Marshal(map[string]any{"hostname": "renamed-by-non-admin"})
	req, _ := http.NewRequest(http.MethodPatch,
		f.httpURL+"/api/v1/peers/"+reg.Self.ID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /peers: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusForbidden {
		respBody, _ := io.ReadAll(httpResp.Body)
		t.Errorf("non-admin PATCH should 403, got status=%d body=%s",
			httpResp.StatusCode, respBody)
	}
}

func TestRESTAdminGate_DeletePeer_RejectsNonAdmin(t *testing.T) {
	f := startFixture(t)

	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "to-delete",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	if resp.status != http.StatusOK {
		t.Fatalf("register: status=%d body=%s", resp.status, resp.body)
	}
	var reg struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	_ = json.Unmarshal(resp.body, &reg)

	tok := f.mintJWT(t, false)
	req, _ := http.NewRequest(http.MethodDelete,
		f.httpURL+"/api/v1/peers/"+reg.Self.ID, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /peers: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusForbidden {
		respBody, _ := io.ReadAll(httpResp.Body)
		t.Errorf("non-admin DELETE should 403, got status=%d body=%s",
			httpResp.StatusCode, respBody)
	}
}

// ---- Tenant membership validation ----------------------------------------

func TestAuthenticate_RejectsStaleTenantClaim(t *testing.T) {
	f := startFixture(t)
	ctx := context.Background()

	// Mint a JWT for a user in the fixture tenant.
	tok, userID := f.mintJWTWithUser(t, false)

	// Move the user to a different tenant in the DB. The JWT still
	// carries the original TenantID claim; the controller must detect
	// the mismatch and reject the request.
	otherTenantID := uuid.New()
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO tenants (id, slug, name, ip_pool)
		VALUES ($1, $2, 'Other Tenant', '100.65.0.0/24')
	`, otherTenantID, "e2e-other-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("create other tenant: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE users SET tenant_id = $1 WHERE id = $2`,
		otherTenantID, userID); err != nil {
		t.Fatalf("move user: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, f.httpURL+"/api/v1/peers", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /peers: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("stale tenant claim should 401, got status=%d body=%s",
			resp.StatusCode, body)
	}
}
