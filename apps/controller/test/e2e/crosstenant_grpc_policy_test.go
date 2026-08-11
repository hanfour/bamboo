// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"testing"
	"time"

	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/metadata"
)

// TestCrossTenant_GRPCGetPolicyBindsToJWTTenant: PolicyService.resolveTenant
// derived the tenant from the client-supplied x-tenant-slug metadata, so a
// caller authenticated in tenant A could read (GetPolicy) or overwrite
// (PutPolicy) another tenant's ACL by spoofing the slug. Both go through
// resolveTenant; this pins the read path. The tenant must come from the
// verified bearer, not the header.
func TestCrossTenant_GRPCGetPolicyBindsToJWTTenant(t *testing.T) {
	f := startFixture(t)
	f.enableRequireAuth()
	jwt, _ := f.mintJWTWithUser(t, false) // user in tenant A (f.tenantSlug)

	var tenantAID string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id FROM tenants WHERE slug = $1`, f.tenantSlug).Scan(&tenantAID); err != nil {
		t.Fatalf("resolve tenant A id: %v", err)
	}
	spoof := "victim-" + f.tenantSlug
	t.Cleanup(func() { cleanupTenant(f.pool, spoof) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Authenticated as tenant A, but spoofing x-tenant-slug to another tenant.
	ctx = metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer "+jwt,
		"x-tenant-slug", spoof)

	resp, err := f.policy.GetPolicy(ctx, &bamboov1.GetPolicyRequest{})
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if got := resp.GetPolicy().GetTenantId(); got != tenantAID {
		t.Errorf("PolicyService honored x-tenant-slug over the JWT: returned tenant %s, "+
			"want the JWT's tenant %s (resolveTenant must bind to the verified bearer, "+
			"not the slug)", got, tenantAID)
	}
}

// TestCrossTenant_GRPCPutPolicyBindsToJWTTenant is the write-path counterpart:
// an admin authenticated in tenant A, spoofing x-tenant-slug to tenant B, must
// NOT overwrite tenant B's ACL — the write must land in the JWT's tenant. Both
// GetPolicy and PutPolicy share resolveTenant, so this pins the scary side
// (cross-tenant ACL overwrite).
func TestCrossTenant_GRPCPutPolicyBindsToJWTTenant(t *testing.T) {
	f := startFixture(t)

	_, slugB := registerVictimPeerInSeparateTenant(t, f, nil) // victim tenant B
	f.enableRequireAuth()
	adminJWT, _ := f.mintJWTWithUser(t, true) // admin in tenant A (f.tenantSlug)

	const hcl = `rule "xt" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["cidr:0.0.0.0/0:443"]
}`

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer "+adminJWT,
		"x-tenant-slug", slugB)

	if _, err := f.policy.PutPolicy(ctx, &bamboov1.PutPolicyRequest{HclSource: hcl}); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}

	countFor := func(slug string) int {
		var n int
		if err := f.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM acl_policies WHERE tenant_id = (SELECT id FROM tenants WHERE slug = $1)`,
			slug).Scan(&n); err != nil {
			t.Fatalf("count policies for %s: %v", slug, err)
		}
		return n
	}
	if b := countFor(slugB); b != 0 {
		t.Errorf("CROSS-TENANT WRITE: PutPolicy with spoofed x-tenant-slug wrote %d ACL "+
			"row(s) into tenant B; want 0 (write must bind to the JWT tenant)", b)
	}
	// Guard against a false pass from invalid HCL: the write must have landed
	// in the JWT's own tenant A.
	if a := countFor(f.tenantSlug); a != 1 {
		t.Errorf("PutPolicy should have written 1 ACL row into the JWT's tenant A; got %d", a)
	}
}

