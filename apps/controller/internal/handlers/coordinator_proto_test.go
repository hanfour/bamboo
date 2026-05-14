// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

func TestToProtoPeer_EmptyEndpointsFallsBackToWGEndpoint(t *testing.T) {
	wg := "203.0.113.10:51820"
	p := &repo.Peer{
		ID:                 uuid.New(),
		TenantID:           uuid.New(),
		Hostname:           "legacy-wg-quick",
		WireGuardPublicKey: "AAAA",
		IP:                 "100.64.0.4",
		Endpoints:          nil,
		WGEndpoint:         &wg,
		CreatedAt:          time.Now(),
	}
	got := toProtoPeer(p)
	if len(got.GetEndpoints()) != 1 || got.GetEndpoints()[0] != wg {
		t.Fatalf("wg_endpoint fallback missing: got %v", got.GetEndpoints())
	}
}

func TestToProtoPeer_StunEndpointsPreferredOverWG(t *testing.T) {
	wg := "203.0.113.10:51820"
	p := &repo.Peer{
		ID:                 uuid.New(),
		TenantID:           uuid.New(),
		Hostname:           "fresh-bamboo",
		WireGuardPublicKey: "BBBB",
		IP:                 "100.64.0.5",
		Endpoints:          []string{"198.51.100.5:60000"},
		WGEndpoint:         &wg,
		CreatedAt:          time.Now(),
	}
	got := toProtoPeer(p)
	if len(got.GetEndpoints()) != 1 || got.GetEndpoints()[0] != "198.51.100.5:60000" {
		t.Fatalf("STUN endpoint should win; got %v", got.GetEndpoints())
	}
}

func TestToProtoPeer_BothEmptyStaysEmpty(t *testing.T) {
	p := &repo.Peer{
		ID:                 uuid.New(),
		TenantID:           uuid.New(),
		Hostname:           "never-handshaked",
		WireGuardPublicKey: "CCCC",
		IP:                 "100.64.0.6",
		Endpoints:          nil,
		WGEndpoint:         nil,
		CreatedAt:          time.Now(),
	}
	got := toProtoPeer(p)
	if len(got.GetEndpoints()) != 0 {
		t.Fatalf("no endpoints should remain empty; got %v", got.GetEndpoints())
	}
}
