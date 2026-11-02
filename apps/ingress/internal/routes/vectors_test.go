// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"encoding/json"
	"os"
	"testing"
)

// sharedVectors is testdata/ingress-labels.json at the repository root,
// shared with the controller and the web UI so the rules cannot drift.
type sharedVectors struct {
	Suffix string `json:"suffix"`
	Labels struct {
		Valid    []string `json:"valid"`
		Invalid  []string `json:"invalid"`
		Reserved []string `json:"reserved"`
	} `json:"labels"`
	Hosts struct {
		Match []struct {
			Host  string `json:"host"`
			Label string `json:"label"`
		} `json:"match"`
		Reject []string `json:"reject"`
	} `json:"hosts"`
}

func loadSharedVectors(t *testing.T) sharedVectors {
	t.Helper()
	raw, err := os.ReadFile("../../../../testdata/ingress-labels.json")
	if err != nil {
		t.Fatalf("read shared vectors: %v", err)
	}
	var v sharedVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode shared vectors: %v", err)
	}
	if v.Suffix == "" || len(v.Labels.Valid) == 0 || len(v.Hosts.Match) == 0 {
		t.Fatal("shared vectors are empty")
	}
	return v
}

// TestRoutes_SharedLabelVectors: the routes payload accepts exactly the
// label shapes the controller stores (reserved names are a controller
// policy, not a shape rule, so the proxy does not check them).
func TestRoutes_SharedLabelVectors(t *testing.T) {
	v := loadSharedVectors(t)
	for _, l := range v.Labels.Valid {
		if err := validRoute(testRoute(l, "100.64.0.5", 47800)); err != nil {
			t.Errorf("valid %q rejected: %v", l, err)
		}
	}
	for _, l := range v.Labels.Invalid {
		if err := validRoute(testRoute(l, "100.64.0.5", 47800)); err == nil {
			t.Errorf("invalid %q accepted", l)
		}
	}
}

// TestTable_SharedHostVectors: Host lookup agrees with the controller's
// ask parsing on the shared host vectors.
func TestTable_SharedHostVectors(t *testing.T) {
	v := loadSharedVectors(t)
	tb := NewTable(v.Suffix)
	rs := make([]Route, 0, len(v.Labels.Valid))
	for _, l := range v.Labels.Valid {
		rs = append(rs, testRoute(l, "100.64.0.5", 47800))
	}
	tb.Replace(rs)
	for _, m := range v.Hosts.Match {
		if e, ok := tb.Lookup(m.Host); !ok || e.Route.Label != m.Label {
			t.Errorf("Lookup(%q) = %v, %v; want label %q", m.Host, e, ok, m.Label)
		}
	}
	for _, h := range v.Hosts.Reject {
		if e, ok := tb.Lookup(h); ok {
			t.Errorf("Lookup(%q) matched %q, want reject", h, e.Route.Label)
		}
	}
}
