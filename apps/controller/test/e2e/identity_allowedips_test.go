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
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// TestRegister_UserAndGroupReachAllowedIPs is the wire contract for
// identity: a session register belongs to that user, a pre-auth peer
// belongs to the admin who minted the key and inherits the key's tags,
// and group: on either side of a rule shows up in AllowedIps.
func TestRegister_UserAndGroupReachAllowedIPs(t *testing.T) {
	f := startFixture(t)
	ctx := context.Background()

	tenants := repo.NewTenants(f.admin)
	tenant, err := tenants.GetOrCreate(ctx, f.tenantSlug, "Default Tenant", "100.64.0.0/24")
	if err != nil {
		t.Fatalf("get tenant: %v", err)
	}
	users := repo.NewUsers(f.admin)
	alice, err := users.UpsertOIDC(ctx, &repo.User{
		TenantID: tenant.ID, Email: "alice@example.com", DisplayName: "Alice",
		OIDCProvider: "test", OIDCSubject: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("upsert alice: %v", err)
	}
	dba, err := users.UpsertOIDC(ctx, &repo.User{
		TenantID: tenant.ID, Email: "dba@example.com", DisplayName: "DBA",
		OIDCProvider: "test", OIDCSubject: uuid.NewString(), IsAdmin: true,
	})
	if err != nil {
		t.Fatalf("upsert dba: %v", err)
	}
	carol, err := users.UpsertOIDC(ctx, &repo.User{
		TenantID: tenant.ID, Email: "carol@example.com", DisplayName: "Carol",
		OIDCProvider: "test", OIDCSubject: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("upsert carol: %v", err)
	}
	aliceTok := mustSession(t, alice.ID, tenant.ID)
	dbaTok := mustSession(t, dba.ID, tenant.ID)
	carolTok := mustSession(t, carol.ID, tenant.ID)

	put, err := f.policy.PutPolicy(f.outgoingCtx(ctx), &bamboov1.PutPolicyRequest{
		HclSource: `
groups = {
  "group:engineering" = ["alice@example.com"]
  "group:dba"         = ["dba@example.com"]
}
rule "eng-to-dba" {
  action       = "allow"
  sources      = ["group:engineering"]
  destinations = ["group:dba:*"]
}
`,
	})
	if err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	if put.GetPolicy().GetRevision() <= 0 {
		t.Fatalf("revision = %d", put.GetPolicy().GetRevision())
	}

	secret := mintTaggedPreAuthKey(t, f, dbaTok, []string{"db"})

	dbResp, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Credential:         &bamboov1.RegisterRequest_PreAuthKeySecret{PreAuthKeySecret: secret},
		Hostname:           "db-server",
		WireguardPublicKey: randomPubKey(t),
	})
	if err != nil {
		t.Fatalf("register db: %v", err)
	}
	dbPeer, err := repo.NewPeers(f.admin).GetByID(ctx, uuid.MustParse(dbResp.GetSelf().GetId()))
	if err != nil {
		t.Fatalf("load db peer: %v", err)
	}
	if dbPeer.UserID == nil || *dbPeer.UserID != dba.ID {
		t.Errorf("db owner = %v, want dba %s", dbPeer.UserID, dba.ID)
	}
	if dbPeer.OwnerEmail != "dba@example.com" {
		t.Errorf("db owner email = %q", dbPeer.OwnerEmail)
	}
	if len(dbPeer.Tags) != 1 || dbPeer.Tags[0] != "db" {
		t.Errorf("db tags = %v, want [db]", dbPeer.Tags)
	}
	if dbPeer.ApprovalStatus != "approved" {
		t.Errorf("db approval = %q, want approved", dbPeer.ApprovalStatus)
	}

	aliceResp, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Credential:         &bamboov1.RegisterRequest_BearerToken{BearerToken: aliceTok},
		Hostname:           "alice-laptop",
		WireguardPublicKey: randomPubKey(t),
	})
	if err != nil {
		t.Fatalf("register alice: %v", err)
	}
	if aliceResp.GetSelf().GetUserId() != alice.ID.String() {
		t.Errorf("alice user id = %q, want %s", aliceResp.GetSelf().GetUserId(), alice.ID)
	}
	dbView := peerByID(aliceResp.GetPeers(), dbResp.GetSelf().GetId())
	if dbView == nil {
		t.Fatal("alice's mesh is missing the db peer")
	}
	if len(dbView.GetAllowedIps()) == 0 || dbView.GetAllowedIps()[0] != dbResp.GetSelf().GetIp()+"/32" {
		t.Errorf("alice → dba-owned db AllowedIps = %v", dbView.GetAllowedIps())
	}

	carolResp, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Credential:         &bamboov1.RegisterRequest_BearerToken{BearerToken: carolTok},
		Hostname:           "carol-laptop",
		WireguardPublicKey: randomPubKey(t),
	})
	if err != nil {
		t.Fatalf("register carol: %v", err)
	}
	carolDB := peerByID(carolResp.GetPeers(), dbResp.GetSelf().GetId())
	if carolDB == nil {
		t.Fatal("carol's mesh is missing the db peer")
	}
	if len(carolDB.GetAllowedIps()) != 0 {
		t.Errorf("carol is not in engineering; AllowedIps = %v, want empty", carolDB.GetAllowedIps())
	}

	// Dev-fallback peer has no owner, so group:dba does not match it.
	loose, err := f.coord.Register(f.outgoingCtx(ctx), &bamboov1.RegisterRequest{
		Hostname: "loose", WireguardPublicKey: randomPubKey(t),
	})
	if err != nil {
		t.Fatalf("register loose: %v", err)
	}
	aliceAgain, err := f.coord.Register(ctx, &bamboov1.RegisterRequest{
		Credential:         &bamboov1.RegisterRequest_BearerToken{BearerToken: aliceTok},
		Hostname:           "alice-laptop",
		WireguardPublicKey: aliceResp.GetSelf().GetWireguardPublicKey(),
	})
	if err != nil {
		t.Fatalf("re-register alice: %v", err)
	}
	looseView := peerByID(aliceAgain.GetPeers(), loose.GetSelf().GetId())
	if looseView == nil {
		t.Fatal("alice's mesh is missing the ownerless peer")
	}
	if len(looseView.GetAllowedIps()) != 0 {
		t.Errorf("ownerless peer AllowedIps = %v, want empty", looseView.GetAllowedIps())
	}
}

func mustSession(t *testing.T, userID, tenantID uuid.UUID) string {
	t.Helper()
	tok, err := auth.IssueSessionToken(
		[]byte("e2e-secret-with-at-least-32-bytes-padding"),
		auth.SessionClaims{UserID: userID, TenantID: tenantID},
		time.Hour,
	)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	return tok
}

func mintTaggedPreAuthKey(t *testing.T, f *fixture, bearer string, tags []string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"description": "db service",
		"reusable":    true,
		"autoApprove": true,
		"tags":        tags,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, f.httpURL+"/api/v1/preauth-keys", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Tenant-Slug", f.tenantSlug)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST preauth: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mint key status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode key: %v body=%s", err, raw)
	}
	if out.Secret == "" {
		t.Fatalf("empty secret: %s", raw)
	}
	return out.Secret
}

func peerByID(peers []*bamboov1.Peer, id string) *bamboov1.Peer {
	for _, p := range peers {
		if p.GetId() == id {
			return p
		}
	}
	return nil
}
