// SPDX-License-Identifier: AGPL-3.0-or-later

package repo_test

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// ingressVectors is testdata/ingress-labels.json at the repository root,
// shared with apps/ingress and apps/web so the label rules cannot drift.
type ingressVectors struct {
	Labels struct {
		Valid    []string `json:"valid"`
		Invalid  []string `json:"invalid"`
		Reserved []string `json:"reserved"`
	} `json:"labels"`
}

func TestValidateIngressLabel_SharedVectors(t *testing.T) {
	raw, err := os.ReadFile("../../../../../testdata/ingress-labels.json")
	if err != nil {
		t.Fatalf("read shared vectors: %v", err)
	}
	var v ingressVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode shared vectors: %v", err)
	}
	if len(v.Labels.Valid) == 0 || len(v.Labels.Invalid) == 0 || len(v.Labels.Reserved) == 0 {
		t.Fatal("shared vectors are empty")
	}
	for _, l := range v.Labels.Valid {
		if err := repo.ValidateIngressLabel(l); err != nil {
			t.Errorf("valid %q: %v", l, err)
		}
	}
	for _, l := range v.Labels.Invalid {
		if err := repo.ValidateIngressLabel(l); !errors.Is(err, repo.ErrIngressLabelInvalid) {
			t.Errorf("invalid %q: %v, want ErrIngressLabelInvalid", l, err)
		}
	}
	for _, l := range v.Labels.Reserved {
		if err := repo.ValidateIngressLabel(l); !errors.Is(err, repo.ErrIngressLabelReserved) {
			t.Errorf("reserved %q: %v, want ErrIngressLabelReserved", l, err)
		}
	}
}
