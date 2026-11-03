// SPDX-License-Identifier: AGPL-3.0-or-later

package wg_test

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/hanfour/bamboo/clients/core/wg"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

func prefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(s))
	for _, p := range s {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}

func TestOrderCandidates(t *testing.T) {
	cases := []struct {
		name  string
		cands []string
		local []netip.Prefix
		want  []string
	}{
		{
			name:  "vps tries public before remote LAN",
			cands: []string{"192.168.1.108:51820", "220.135.1.2:51820"},
			local: prefixes("203.0.113.0/24", "10.0.0.0/8"),
			want:  []string{"220.135.1.2:51820", "192.168.1.108:51820"},
		},
		{
			name:  "same LAN goes direct first",
			cands: []string{"220.135.1.2:51820", "192.168.1.108:51820"},
			local: prefixes("192.168.1.0/24"),
			want:  []string{"192.168.1.108:51820", "220.135.1.2:51820"},
		},
		{
			name:  "off-subnet private kept in controller order",
			cands: []string{"192.168.1.108:51820", "10.1.2.3:51820"},
			local: prefixes("172.16.0.0/16"),
			want:  []string{"192.168.1.108:51820", "10.1.2.3:51820"},
		},
		{
			name: "routed private (CGNAT, ULA) demoted behind public",
			cands: []string{
				"100.64.1.1:1", "[fd00::1]:3", "[2001:db8::1]:5", "198.51.100.7:6",
			},
			want: []string{"[2001:db8::1]:5", "198.51.100.7:6", "100.64.1.1:1", "[fd00::1]:3"},
		},
		{
			name:  "link-local never counts as same LAN",
			cands: []string{"169.254.1.1:2", "[fe80::1%eth0]:4", "198.51.100.7:6"},
			local: prefixes("169.254.0.0/16", "fe80::/64"),
			want:  []string{"198.51.100.7:6", "169.254.1.1:2", "[fe80::1%eth0]:4"},
		},
		{
			name:  "groups: same LAN, public and hostnames, then off-subnet private",
			cands: []string{"10.9.9.9:2", "198.51.100.7:1", "192.168.0.5:7", "relay.example.com:3", "203.0.113.9:4"},
			local: prefixes("192.168.0.0/16"),
			want:  []string{"192.168.0.5:7", "198.51.100.7:1", "relay.example.com:3", "203.0.113.9:4", "10.9.9.9:2"},
		},
		{
			name:  "empty entries ignored",
			cands: []string{"", "198.51.100.7:1"},
			want:  []string{"198.51.100.7:1"},
		},
		{
			name:  "no candidates",
			cands: nil,
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wg.OrderCandidates(tc.cands, tc.local)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestOrderCandidatesDoesNotMutateInput(t *testing.T) {
	in := []string{"220.135.1.2:51820", "192.168.1.108:51820"}
	_ = wg.OrderCandidates(in, prefixes("192.168.1.0/24"))
	if in[0] != "220.135.1.2:51820" {
		t.Fatal("input reordered")
	}
}

func TestBuildDeviceConfigCarriesCandidates(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	peerPriv, _ := wg.GeneratePrivateKey()
	resp := &bamboov1.RegisterResponse{
		Self: &bamboov1.Peer{Id: "self", Ip: "100.64.0.1"},
		Peers: []*bamboov1.Peer{{
			Id: "mac", Ip: "100.64.0.2", WireguardPublicKey: peerPriv.PublicKey().Base64(),
			Endpoints: []string{"192.168.1.108:51820", "", "220.135.1.2:51820"},
		}},
	}
	cfg, err := wg.BuildDeviceConfig(priv, resp)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.1.108:51820", "220.135.1.2:51820"}
	if !slices.Equal(cfg.Peers[0].Candidates, want) {
		t.Fatalf("candidates = %v", cfg.Peers[0].Candidates)
	}
	if cfg.Peers[0].Endpoint != "192.168.1.108:51820" {
		t.Fatalf("endpoint = %q, want controller first", cfg.Peers[0].Endpoint)
	}
}
