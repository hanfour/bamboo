// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"os"
	"testing"
)

// TestIngressLabelFromDomain_SharedVectors runs the ask endpoint's
// domain parsing against testdata/ingress-labels.json, which the ingress
// proxy's Host lookup also consumes.
func TestIngressLabelFromDomain_SharedVectors(t *testing.T) {
	raw, err := os.ReadFile("../../../../testdata/ingress-labels.json")
	if err != nil {
		t.Fatalf("read shared vectors: %v", err)
	}
	var v struct {
		Suffix string `json:"suffix"`
		Hosts  struct {
			Match []struct {
				Host  string `json:"host"`
				Label string `json:"label"`
			} `json:"match"`
			Reject []string `json:"reject"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode shared vectors: %v", err)
	}
	if v.Suffix == "" || len(v.Hosts.Match) == 0 || len(v.Hosts.Reject) == 0 {
		t.Fatal("shared vectors are empty")
	}
	for _, m := range v.Hosts.Match {
		if got, ok := ingressLabelFromDomain(m.Host, v.Suffix); !ok || got != m.Label {
			t.Errorf("%q = %q, %v; want %q", m.Host, got, ok, m.Label)
		}
	}
	for _, h := range v.Hosts.Reject {
		if got, ok := ingressLabelFromDomain(h, v.Suffix); ok {
			t.Errorf("%q matched label %q, want reject", h, got)
		}
	}
}
