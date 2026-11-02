// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// failPolicyBumps installs a trigger that makes every acl_policies write
// for tenantID fail, and removes it on cleanup. It needs the table
// owner, so it connects with the raw test DSN.
func failPolicyBumps(t *testing.T, tenantID string) (restore func()) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL_TEST"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	fn := "fail_bump_" + tenantID[:8]
	stmts := []string{
		`CREATE FUNCTION ` + fn + `() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN
		   IF NEW.tenant_id = '` + tenantID + `'::uuid THEN
		     RAISE EXCEPTION 'injected policy bump failure';
		   END IF;
		   RETURN NEW;
		 END $$`,
		`CREATE TRIGGER ` + fn + ` BEFORE INSERT OR UPDATE ON acl_policies
		 FOR EACH ROW EXECUTE FUNCTION ` + fn + `()`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			_ = conn.Close(ctx)
			t.Fatalf("install failing trigger: %v", err)
		}
	}
	done := false
	restore = func() {
		if done {
			return
		}
		done = true
		_, _ = conn.Exec(ctx, `DROP TRIGGER IF EXISTS `+fn+` ON acl_policies`)
		_, _ = conn.Exec(ctx, `DROP FUNCTION IF EXISTS `+fn+`()`)
		_ = conn.Close(ctx)
	}
	t.Cleanup(restore)
	return restore
}

func tenantIDBySlug(t *testing.T, f *fixture) string {
	t.Helper()
	var id string
	if err := f.admin.QueryRow(context.Background(),
		`SELECT id::text FROM tenants WHERE slug = $1`, f.tenantSlug).Scan(&id); err != nil {
		t.Fatalf("tenant id: %v", err)
	}
	return id
}

// TestIngressRegister_BumpFailureLeavesNoHalfState: when lifting the
// tenant to revision >= 1 fails during an ingress-tagged registration,
// no peer row is left behind and the single-use key is still usable, so
// the operator can simply retry.
func TestIngressRegister_BumpFailureLeavesNoHalfState(t *testing.T) {
	f := startFixture(t)
	registerIngressTestPeer(t, f, "bystander") // creates the tenant
	key := ingressCall(t, http.MethodPost, f.httpURL+"/api/v1/preauth-keys", "", f.tenantSlug,
		map[string]any{"description": "vps", "autoApprove": true, "tags": []string{"ingress"}})
	if key.status != http.StatusOK && key.status != http.StatusCreated {
		t.Fatalf("mint key: %d %s", key.status, key.body)
	}
	var k struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(key.body, &k); err != nil || k.Secret == "" {
		t.Fatalf("decode key: %v %s", err, key.body)
	}
	tenantID := tenantIDBySlug(t, f)
	restore := failPolicyBumps(t, tenantID)

	pub := randomPubKey(t)
	register := func() jsonResponse {
		return postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
			"hostname": "vps", "wireguardPublicKey": pub, "preAuthKeySecret": k.Secret,
		})
	}
	if r := register(); r.status == http.StatusOK {
		t.Fatalf("register with failing bump: 200 %s, want an error", r.body)
	}
	var peers, uses int
	ctx := context.Background()
	if err := f.admin.QueryRow(ctx, `SELECT count(*) FROM peers WHERE wireguard_public_key = $1`, pub).Scan(&peers); err != nil {
		t.Fatalf("count peers: %v", err)
	}
	if peers != 0 {
		t.Errorf("peer rows after failed register = %d, want 0", peers)
	}
	if err := f.admin.QueryRow(ctx, `SELECT use_count FROM pre_auth_keys WHERE tenant_id = $1::uuid`, tenantID).Scan(&uses); err != nil {
		t.Fatalf("read key: %v", err)
	}
	if uses != 0 {
		t.Errorf("key use_count after failed register = %d, want 0", uses)
	}

	restore()
	if r := register(); r.status != http.StatusOK {
		t.Fatalf("retry register: %d %s", r.status, r.body)
	}
	if rev := policyRevision(t, f); rev < 1 {
		t.Errorf("revision after retry = %d, want >= 1", rev)
	}
}
