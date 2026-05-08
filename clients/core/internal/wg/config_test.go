// SPDX-License-Identifier: AGPL-3.0-or-later

package wg_test

import (
	"strings"
	"testing"

	"github.com/hanfour/bamboo/clients/core/internal/wg"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

func TestBuildDeviceConfig_HappyPath(t *testing.T) {
	priv, err := wg.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}

	peerPriv, _ := wg.GeneratePrivateKey()
	peerPubB64 := peerPriv.PublicKey().Base64()

	resp := &bamboov1.RegisterResponse{
		Self: &bamboov1.Peer{
			Id: "self-id",
			Ip: "100.64.0.1",
		},
		Peers: []*bamboov1.Peer{
			{
				Id:                 "peer-id",
				Ip:                 "100.64.0.2",
				WireguardPublicKey: peerPubB64,
				Endpoints:          []string{"203.0.113.5:51820"},
			},
		},
	}

	cfg, err := wg.BuildDeviceConfig(priv, resp)
	if err != nil {
		t.Fatalf("BuildDeviceConfig: %v", err)
	}
	if cfg.Address.String() != "100.64.0.1/32" {
		t.Errorf("Address = %s, want 100.64.0.1/32", cfg.Address)
	}
	if len(cfg.Peers) != 1 {
		t.Fatalf("len(Peers) = %d, want 1", len(cfg.Peers))
	}
	p := cfg.Peers[0]
	if p.AllowedIPs[0].String() != "100.64.0.2/32" {
		t.Errorf("peer AllowedIPs[0] = %s, want 100.64.0.2/32", p.AllowedIPs[0])
	}
	if p.Endpoint != "203.0.113.5:51820" {
		t.Errorf("peer Endpoint = %q, want 203.0.113.5:51820", p.Endpoint)
	}
	if p.PublicKey != peerPriv.PublicKey() {
		t.Error("peer PublicKey did not match input")
	}
}

func TestBuildDeviceConfig_MissingSelf(t *testing.T) {
	_, err := wg.BuildDeviceConfig(wg.PrivateKey{}, &bamboov1.RegisterResponse{})
	if err == nil {
		t.Error("expected error when Self is nil")
	}
}

func TestBuildDeviceConfig_BadPeerKey(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	resp := &bamboov1.RegisterResponse{
		Self: &bamboov1.Peer{Ip: "100.64.0.1"},
		Peers: []*bamboov1.Peer{
			{Ip: "100.64.0.2", WireguardPublicKey: "definitely-not-base64"},
		},
	}
	if _, err := wg.BuildDeviceConfig(priv, resp); err == nil {
		t.Error("expected error for malformed peer key")
	}
}

func TestDeviceConfig_WGQuick_Format(t *testing.T) {
	priv, _ := wg.GeneratePrivateKey()
	peerPriv, _ := wg.GeneratePrivateKey()

	resp := &bamboov1.RegisterResponse{
		Self: &bamboov1.Peer{Ip: "100.64.0.1"},
		Peers: []*bamboov1.Peer{
			{
				Ip:                 "100.64.0.2",
				WireguardPublicKey: peerPriv.PublicKey().Base64(),
				Endpoints:          []string{"203.0.113.5:51820"},
			},
		},
	}
	cfg, err := wg.BuildDeviceConfig(priv, resp)
	if err != nil {
		t.Fatalf("BuildDeviceConfig: %v", err)
	}
	out := cfg.WGQuick()

	for _, want := range []string{
		"[Interface]", "[Peer]",
		"PrivateKey", "PublicKey",
		"100.64.0.1/32", "100.64.0.2/32",
		"203.0.113.5:51820",
		"PersistentKeepalive = 25",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("WGQuick output missing %q. got:\n%s", want, out)
		}
	}
}
