// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
)

// simulateEdges runs the policy simulator on hcl and returns
// "src->dst" => allow.
func simulateEdges(t *testing.T, f *fixture, hcl string) map[string]bool {
	t.Helper()
	r := ingressCall(t, http.MethodPost, f.httpURL+"/api/v1/policy/simulate", "", f.tenantSlug, map[string]any{"hclSource": hcl})
	if r.status != http.StatusOK {
		t.Fatalf("simulate: %d %s", r.status, r.body)
	}
	var out struct {
		Edges []struct {
			SourceID string `json:"sourceId"`
			DestID   string `json:"destId"`
			Allow    bool   `json:"allow"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("decode: %v %s", err, r.body)
	}
	m := map[string]bool{}
	for _, e := range out.Edges {
		m[e.SourceID+"->"+e.DestID] = e.Allow
	}
	return m
}

// TestPolicySimulate_MatchesIngressEnforcement: the simulator matrix
// applies the generated ingress rules and the zero-rule ingress gate, so
// it shows what peers actually receive.
func TestPolicySimulate_MatchesIngressEnforcement(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	other := registerIngressTestPeer(t, f, "other")
	vps := registerIngressTestPeer(t, f, "vps")
	tagPeer(t, f, vps.id, "ingress")
	makeLiveService(t, f, mac.id)

	cases := []struct {
		name string
		hcl  string
		want map[string]bool
	}{
		{"zero rules", "", map[string]bool{
			vps.id + "->" + mac.id: true, mac.id + "->" + vps.id: true,
			vps.id + "->" + other.id: false, other.id + "->" + vps.id: false,
			mac.id + "->" + other.id: true, other.id + "->" + mac.id: true,
		}},
		{"authored rule", "rule \"dev-to-db\" {\n  action = \"allow\"\n  sources = [\"tag:dev\"]\n  destinations = [\"tag:db:5432\"]\n}\n", map[string]bool{
			vps.id + "->" + mac.id: true, mac.id + "->" + vps.id: true,
			vps.id + "->" + other.id: false, other.id + "->" + vps.id: false,
			mac.id + "->" + other.id: false,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := simulateEdges(t, f, c.hcl)
			for edge, want := range c.want {
				if got[edge] != want {
					t.Errorf("%s: allow=%v, want %v", edge, got[edge], want)
				}
			}
		})
	}
}
