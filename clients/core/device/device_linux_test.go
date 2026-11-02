// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package device

import (
	"errors"
	"testing"
	"time"

	"github.com/hanfour/bamboo/clients/core/wg"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type kernelConfigFake struct {
	config wgtypes.Config
	peers  []wgtypes.Peer
	fail   bool
}

func (f *kernelConfigFake) Device(string) (*wgtypes.Device, error) {
	return &wgtypes.Device{Peers: f.peers}, nil
}
func (f *kernelConfigFake) Close() error { return nil }
func (f *kernelConfigFake) ConfigureDevice(_ string, c wgtypes.Config) error {
	f.config = c
	for i, p := range c.Peers {
		found := -1
		for j := range f.peers {
			if f.peers[j].PublicKey == p.PublicKey {
				found = j
				break
			}
		}
		if p.Remove {
			if found >= 0 {
				f.peers = append(f.peers[:found], f.peers[found+1:]...)
			}
		} else if found < 0 {
			f.peers = append(f.peers, wgtypes.Peer{PublicKey: p.PublicKey})
		}
		if f.fail && i == 0 {
			return errors.New("partial configure")
		}
	}

	return nil
}
func applyTestWireGuard(d *linuxDevice, cfg *wg.DeviceConfig) error {
	peers, err := desiredPeers(cfg)
	if err != nil {
		return err
	}
	return d.applyPreparedWireGuard(cfg, peers)
}

func TestIncrementalPeerIntent(t *testing.T) {
	f := &kernelConfigFake{}
	d := &linuxDevice{wg: f}
	cfg := &wg.DeviceConfig{Peers: []wg.PeerConfig{{PublicKey: wg.PublicKey{1}, Endpoint: "192.0.2.1:1234", PersistentKeepalive: 25 * time.Second}}}
	if err := applyTestWireGuard(d, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Peers[0].PersistentKeepalive = 0
	if err := applyTestWireGuard(d, cfg); err != nil {
		t.Fatal(err)
	}
	p := f.config.Peers[0]
	if f.config.ReplacePeers || p.Endpoint != nil || p.PersistentKeepaliveInterval == nil || *p.PersistentKeepaliveInterval != 0 || !p.ReplaceAllowedIPs {
		t.Fatal("incremental intent contract violated")
	}
	cfg.Peers[0].Endpoint = "192.0.2.2:1234"
	f.fail = true
	if err := applyTestWireGuard(d, cfg); err == nil {
		t.Fatal("expected partial failure")
	}
	f.fail = false
	if err := applyTestWireGuard(d, cfg); err != nil {
		t.Fatal(err)
	}
	if f.config.Peers[0].Endpoint == nil {
		t.Fatal("uncertain endpoint skipped on retry")
	}
	if err := applyTestWireGuard(d, &wg.DeviceConfig{}); err != nil {
		t.Fatal(err)
	}
	if len(f.config.Peers) != 1 || !f.config.Peers[0].Remove {
		t.Fatal("missing explicit removal")
	}
}

func TestPartialMembershipRetryAndRouteFailure(t *testing.T) {
	a, b, c := wg.PublicKey{1}, wg.PublicKey{2}, wg.PublicKey{3}
	f := &kernelConfigFake{peers: []wgtypes.Peer{{PublicKey: wgtypes.Key(a)}, {PublicKey: wgtypes.Key(b)}}, fail: true}
	d := &linuxDevice{wg: f}
	cfg := &wg.DeviceConfig{Peers: []wg.PeerConfig{{PublicKey: c, Endpoint: "192.0.2.3:51820"}}}
	if err := applyTestWireGuard(d, cfg); err == nil {
		t.Fatal("expected partial update")
	}
	if len(f.peers) != 3 {
		t.Fatal("fake did not retain partial new membership")
	}
	f.fail = false
	peers, err := desiredPeers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	routeError := errors.New("route failure")
	if err := d.configureAndFinish(cfg, peers, func() error { return routeError }); !errors.Is(err, routeError) {
		t.Fatal(err)
	}
	if len(f.peers) != 1 || f.peers[0].PublicKey != wgtypes.Key(c) {
		t.Fatal("retry failed to reconcile actual membership")
	}
	if err := applyTestWireGuard(d, cfg); err != nil {
		t.Fatal(err)
	}
	if f.config.Peers[0].Endpoint != nil {
		t.Fatal("route failure discarded successful WG intent")
	}
	cfg.Peers[0].Endpoint = "invalid endpoint"
	if err := applyTestWireGuard(d, cfg); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
	if len(f.peers) != 1 {
		t.Fatal("invalid endpoint mutated kernel")
	}
}
