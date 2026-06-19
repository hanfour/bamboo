// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestRESTDNS_PatchEnablesDNS64 is the regression for a routing bug: PATCH
// /api/v1/dns was shadowed by routeAPI's blanket GET-only method guard, so
// apiDNS's PATCH branch (→ apiDNSPatch) was dead code and DNS64 could not be
// toggled over REST at all — the NAT64 hardware E2E had to flip the flag via a
// direct DB UPDATE. PATCH must reach apiDNSPatch (200, not the guard's 405)
// and the change must read back on GET.
func TestRESTDNS_PatchEnablesDNS64(t *testing.T) {
	f := startFixture(t)

	// Baseline: the endpoint serves GET and a fresh tenant has DNS64 off.
	before := getJSON(t, f.httpURL+"/api/v1/dns", f.tenantSlug)
	if before.status != http.StatusOK {
		t.Fatalf("GET /api/v1/dns: status=%d body=%s", before.status, before.body)
	}

	// Enable DNS64 over REST. Before the fix this returned 405 from the
	// GET-only guard instead of reaching apiDNSPatch.
	patch := sendJSONWithTenant(t, http.MethodPatch,
		f.httpURL+"/api/v1/dns", f.tenantSlug,
		map[string]any{"dns64Enabled": true})
	if patch.status != http.StatusOK {
		t.Fatalf("PATCH /api/v1/dns: status=%d body=%s", patch.status, patch.body)
	}

	// The change must be readable back.
	after := getJSON(t, f.httpURL+"/api/v1/dns", f.tenantSlug)
	var dns struct {
		DNS64Enabled bool `json:"dns64Enabled"`
	}
	if err := json.Unmarshal(after.body, &dns); err != nil {
		t.Fatalf("decode GET /api/v1/dns: %v body=%s", err, after.body)
	}
	if !dns.DNS64Enabled {
		t.Errorf("after PATCH, GET shows dns64Enabled=false, want true; body=%s", after.body)
	}
}
