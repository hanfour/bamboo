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
