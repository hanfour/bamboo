// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// TestPolicyValidate_HappyPath verifies that POST /api/v1/policy/validate
// returns 200 + rules count when the HCL parses cleanly. Mirrors the
// admin-tooling "Validate" button in the ACL editor (issue #134).
func TestPolicyValidate_HappyPath(t *testing.T) {
	f := startFixture(t)
	resp := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/policy/validate", f.tenantSlug,
		map[string]any{
			"hclSource": `rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:*"]
}`,
		})
	if resp.status != http.StatusOK {
		t.Fatalf("validate status=%d body=%s", resp.status, resp.body)
	}
	var out struct {
		Ok    bool `json:"ok"`
		Rules int  `json:"rules"`
	}
	if err := json.Unmarshal(resp.body, &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, resp.body)
	}
	if !out.Ok || out.Rules != 1 {
		t.Errorf("got ok=%v rules=%d, want ok=true rules=1", out.Ok, out.Rules)
	}
}

// TestPolicyValidate_RejectsBadHCL verifies that the validator returns
// 400 with the parser message on invalid input, without writing a row.
func TestPolicyValidate_RejectsBadHCL(t *testing.T) {
	f := startFixture(t)
	resp := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/policy/validate", f.tenantSlug,
		map[string]any{
			"hclSource": `rule "broken" {
  action = "INVALID_VERB"
}`,
		})
	if resp.status != http.StatusBadRequest {
		t.Errorf("status=%d, want 400; body=%s", resp.status, resp.body)
	}
	// No new policy row should have been written.
	policies := repo.NewPolicies(f.pool)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// The tenant doesn't have a row yet (no Put has run), so Get
	// returns ErrNotFound — that's the expected state. If a row WAS
	// created, validate would have committed a row, which is the bug
	// this test guards against.
	tenant := mustGetTenant(t, f)
	if rec, err := policies.Get(ctx, tenant.ID); err == nil && rec != nil {
		t.Errorf("validate created a policy row: revision=%d", rec.Revision)
	}
}

// TestPolicySimulate_ReturnsAllowDenyMatrix sets up 2 tagged peers +
// a policy that allows dev→db only and verifies the simulator's
// matrix matches the data plane's compile semantics.
func TestPolicySimulate_ReturnsAllowDenyMatrix(t *testing.T) {
	f := startFixture(t)
	devID := registerPeerDevFallback(t, f, "dev-laptop", randomPubKey(t))
	dbID := registerPeerDevFallback(t, f, "db-server", randomPubKey(t))
	if r := sendJSONWithTenant(t, http.MethodPatch,
		f.httpURL+"/api/v1/peers/"+devID, f.tenantSlug,
		map[string]any{"tags": []string{"dev"}}); r.status != http.StatusOK {
		t.Fatalf("tag dev: status=%d body=%s", r.status, r.body)
	}
	if r := sendJSONWithTenant(t, http.MethodPatch,
		f.httpURL+"/api/v1/peers/"+dbID, f.tenantSlug,
		map[string]any{"tags": []string{"db"}}); r.status != http.StatusOK {
		t.Fatalf("tag db: status=%d body=%s", r.status, r.body)
	}

	resp := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/policy/simulate", f.tenantSlug,
		map[string]any{
			"hclSource": `rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:*"]
}`,
		})
	if resp.status != http.StatusOK {
		t.Fatalf("simulate status=%d body=%s", resp.status, resp.body)
	}

	var out struct {
		Rules int `json:"rules"`
		Edges []struct {
			SourceID   string   `json:"sourceId"`
			DestID     string   `json:"destId"`
			Allow      bool     `json:"allow"`
			AllowedIPs []string `json:"allowedIps"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(resp.body, &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, resp.body)
	}
	if out.Rules != 1 {
		t.Errorf("Rules = %d, want 1", out.Rules)
	}
	// Expect exactly two edges (dev→db, db→dev) — no self-pairs.
	if len(out.Edges) != 2 {
		t.Fatalf("got %d edges, want 2; edges=%+v", len(out.Edges), out.Edges)
	}
	for _, e := range out.Edges {
		switch {
		case e.SourceID == devID && e.DestID == dbID:
			if !e.Allow {
				t.Errorf("dev→db must allow under the rule, got deny")
			}
			if len(e.AllowedIPs) == 0 {
				t.Errorf("dev→db allow but allowedIPs empty")
			}
		case e.SourceID == dbID && e.DestID == devID:
			if e.Allow {
				t.Errorf("db→dev must deny under the rule, got allow")
			}
		default:
			t.Errorf("unexpected edge %+v", e)
		}
	}
}

// TestPolicyRevisions_ListsAllPriorRevisions writes three revisions
// via PUT /api/v1/policy, then GETs the revisions list and verifies
// it returns all three newest-first.
func TestPolicyRevisions_ListsAllPriorRevisions(t *testing.T) {
	f := startFixture(t)
	ctx := context.Background()

	rule := func(name string) string {
		return `rule "` + name + `" {
  action       = "allow"
  sources      = ["tag:` + name + `"]
  destinations = ["tag:` + name + `:*"]
}`
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, err := f.policy.PutPolicy(f.outgoingCtx(ctx), &bamboov1.PutPolicyRequest{
			HclSource: rule(name),
		}); err != nil {
			t.Fatalf("PutPolicy %s: %v", name, err)
		}
	}

	resp := getJSON(t, f.httpURL+"/api/v1/policy/revisions", f.tenantSlug)
	var out struct {
		Revisions []struct {
			Revision  int64  `json:"revision"`
			HCLSource string `json:"hclSource"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal(resp.body, &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, resp.body)
	}
	if len(out.Revisions) != 3 {
		t.Fatalf("got %d revisions, want 3; revisions=%+v", len(out.Revisions), out.Revisions)
	}
	// Newest first.
	if !(out.Revisions[0].Revision > out.Revisions[1].Revision &&
		out.Revisions[1].Revision > out.Revisions[2].Revision) {
		t.Errorf("revisions out of order: %d %d %d",
			out.Revisions[0].Revision, out.Revisions[1].Revision, out.Revisions[2].Revision)
	}
	if !strings.Contains(out.Revisions[0].HCLSource, "gamma") {
		t.Errorf("newest revision missing gamma rule; hcl=%q", out.Revisions[0].HCLSource)
	}
}

// TestPolicyRollback_RestoresOlderHCL writes two revisions, rolls
// back to the first, and verifies that the current policy carries
// the older HCL under a new revision number.
func TestPolicyRollback_RestoresOlderHCL(t *testing.T) {
	f := startFixture(t)
	ctx := context.Background()

	if _, err := f.policy.PutPolicy(f.outgoingCtx(ctx), &bamboov1.PutPolicyRequest{
		HclSource: `rule "v1" {
  action       = "allow"
  sources      = ["tag:v1"]
  destinations = ["tag:v1:*"]
}`,
	}); err != nil {
		t.Fatalf("PutPolicy v1: %v", err)
	}
	if _, err := f.policy.PutPolicy(f.outgoingCtx(ctx), &bamboov1.PutPolicyRequest{
		HclSource: `rule "v2" {
  action       = "allow"
  sources      = ["tag:v2"]
  destinations = ["tag:v2:*"]
}`,
	}); err != nil {
		t.Fatalf("PutPolicy v2: %v", err)
	}

	// Roll back to revision 1.
	resp := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/policy/rollback", f.tenantSlug,
		map[string]any{"revision": 1})
	if resp.status != http.StatusOK {
		t.Fatalf("rollback status=%d body=%s", resp.status, resp.body)
	}

	// GET /api/v1/policy now should return revision >= 3 carrying the v1 rule.
	cur := getJSON(t, f.httpURL+"/api/v1/policy", f.tenantSlug)
	var got struct {
		Revision  int64  `json:"revision"`
		HCLSource string `json:"hclSource"`
	}
	if err := json.Unmarshal(cur.body, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, cur.body)
	}
	if got.Revision < 3 {
		t.Errorf("post-rollback revision = %d, want >= 3", got.Revision)
	}
	if !strings.Contains(got.HCLSource, "v1") {
		t.Errorf("post-rollback hcl missing v1 rule; got %q", got.HCLSource)
	}
	if strings.Contains(got.HCLSource, "v2") {
		t.Errorf("post-rollback hcl still contains v2; got %q", got.HCLSource)
	}

	// audit row recorded. Scope to THIS tenant — the fixture
	// shares a DB across tests in the package, so a tenant-agnostic
	// count would pick up rollback rows from other test runs.
	tenant := mustGetTenant(t, f)
	var count int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action='policy.rollback' AND tenant_id = $1`,
		tenant.ID).Scan(&count); err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if count != 1 {
		t.Errorf("policy.rollback audit rows = %d, want 1", count)
	}
}

// TestPolicyRollback_UnknownRevision returns 404 when the requested
// revision is not in the tenant's history.
func TestPolicyRollback_UnknownRevision(t *testing.T) {
	f := startFixture(t)
	resp := sendJSONWithTenant(t, http.MethodPost,
		f.httpURL+"/api/v1/policy/rollback", f.tenantSlug,
		map[string]any{"revision": 999})
	if resp.status != http.StatusNotFound {
		t.Errorf("status=%d, want 404; body=%s", resp.status, resp.body)
	}
}

// --- helpers ------------------------------------------------------

// mustGetTenant fetches the fixture's tenant row by slug — used by
// validate-no-write test to assert the policy table stays empty.
func mustGetTenant(t *testing.T, f *fixture) *repo.Tenant {
	t.Helper()
	tenants := repo.NewTenants(f.pool)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tenant, err := tenants.GetOrCreate(ctx, f.tenantSlug, "Default Tenant", "100.64.0.0/24")
	if err != nil {
		t.Fatalf("get tenant: %v", err)
	}
	return tenant
}

// Keep the linter satisfied: io / uuid are used by the test bodies
// above via the shared helpers from rest_peers_test.go and friends.
var _ = io.ReadAll
var _ = uuid.Nil
