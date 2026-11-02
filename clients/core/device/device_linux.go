// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package device

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/hanfour/bamboo/clients/core/wg"
	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// New returns a Linux Device. The caller must hold CAP_NET_ADMIN; in
// practice that means running as root.
func New(opts Options) (Device, error) {
	if opts.InterfaceName == "" {
		return nil, fmt.Errorf("InterfaceName is required")
	}
	if opts.MTU == 0 {
		opts.MTU = 1420
	}

	wg, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("wgctrl: %w", err)
	}
	return &linuxDevice{opts: opts, wg: wg}, nil
}

type linuxDevice struct {
	opts      Options
	wg        wireGuardClient
	mu        sync.Mutex
	endpoints map[wgtypes.Key]string
	link      netlink.Link
}

type wireGuardClient interface {
	Device(string) (*wgtypes.Device, error)
	ConfigureDevice(string, wgtypes.Config) error
	Close() error
}

// Apply implements Device.
func (d *linuxDevice) Apply(_ context.Context, cfg *wg.DeviceConfig) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	peers, err := desiredPeers(cfg)
	if err != nil {
		return err
	}
	if err := d.ensureLink(); err != nil {
		return err
	}
	if err := d.applyAddress(cfg); err != nil {
		return err
	}
	return d.configureAndFinish(cfg, peers, func() error {
		if err := netlink.LinkSetUp(d.link); err != nil {
			return fmt.Errorf("link up: %w", err)
		}
		return d.applyRoutes(cfg)
	})
}

func (d *linuxDevice) configureAndFinish(cfg *wg.DeviceConfig, peers []wgtypes.PeerConfig, finish func() error) error {
	if err := d.applyPreparedWireGuard(cfg, peers); err != nil {
		return err
	}
	return finish()
}

// Stats implements Device.
func (d *linuxDevice) Stats() ([]PeerStats, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.link == nil {
		return nil, nil
	}
	dev, err := d.wg.Device(d.opts.InterfaceName)
	if err != nil {
		return nil, fmt.Errorf("wg device: %w", err)
	}
	out := make([]PeerStats, 0, len(dev.Peers))
	for _, p := range dev.Peers {
		var endpoint string
		if p.Endpoint != nil {
			endpoint = p.Endpoint.String()
		}
		out = append(out, PeerStats{
			PublicKey:         p.PublicKey.String(),
			Endpoint:          endpoint,
			LastHandshakeTime: p.LastHandshakeTime,
			RxBytes:           uint64(p.ReceiveBytes),
			TxBytes:           uint64(p.TransmitBytes),
		})
	}
	return out, nil
}

// Close implements Device.
func (d *linuxDevice) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	defer func() { _ = d.wg.Close() }()
	if d.link == nil {
		return nil
	}
	return netlink.LinkDel(d.link)
}

func (d *linuxDevice) ensureLink() error {
	link, err := netlink.LinkByName(d.opts.InterfaceName)
	if err == nil {
		d.link = link
		return nil
	}

	attrs := netlink.NewLinkAttrs()
	attrs.Name = d.opts.InterfaceName
	attrs.MTU = d.opts.MTU
	wgLink := &netlink.Wireguard{LinkAttrs: attrs}
	if err := netlink.LinkAdd(wgLink); err != nil {
		return fmt.Errorf("link add: %w", err)
	}
	d.link = wgLink
	return nil
}

func (d *linuxDevice) applyAddress(cfg *wg.DeviceConfig) error {
	if err := d.addAddr(cfg.Address); err != nil {
		return err
	}
	// Dual-stack (NAT64 Phase A): add the IPv6 ULA when the controller
	// supplied one. Zero/invalid on a pre-#232 controller — skip then.
	if cfg.Address6.IsValid() {
		if err := d.addAddr(cfg.Address6); err != nil {
			return err
		}
	}
	return nil
}

// addAddr assigns a single prefix to the link, idempotent on EEXIST.
func (d *linuxDevice) addAddr(p netip.Prefix) error {
	addr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   p.Addr().AsSlice(),
			Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen()),
		},
	}
	if err := netlink.AddrAdd(d.link, addr); err != nil && !isExist(err) {
		return fmt.Errorf("addr add %s: %w", p, err)
	}
	return nil
}

func desiredPeers(cfg *wg.DeviceConfig) ([]wgtypes.PeerConfig, error) {
	if cfg == nil {
		return nil, fmt.Errorf("missing device config")
	}
	peers := make([]wgtypes.PeerConfig, 0, len(cfg.Peers))
	seen := make(map[wgtypes.Key]bool)
	for _, p := range cfg.Peers {
		pub := wgtypes.Key(p.PublicKey)
		if seen[pub] {
			return nil, fmt.Errorf("duplicate peer key")
		}
		seen[pub] = true
		ka := p.PersistentKeepalive
		if ka < 0 {
			return nil, fmt.Errorf("negative keepalive")
		}
		pc := wgtypes.PeerConfig{PublicKey: pub, ReplaceAllowedIPs: true, PersistentKeepaliveInterval: &ka}
		for _, a := range p.AllowedIPs {
			if !a.IsValid() {
				return nil, fmt.Errorf("invalid allowed IP")
			}
			pc.AllowedIPs = append(pc.AllowedIPs, net.IPNet{IP: a.Addr().AsSlice(), Mask: net.CIDRMask(a.Bits(), a.Addr().BitLen())})
		}
		if p.Endpoint != "" {
			ep, err := net.ResolveUDPAddr("udp", p.Endpoint)
			if err != nil {
				return nil, fmt.Errorf("peer endpoint: %w", err)
			}
			pc.Endpoint = ep
		}
		peers = append(peers, pc)
	}
	return peers, nil
}

func (d *linuxDevice) applyPreparedWireGuard(cfg *wg.DeviceConfig, peers []wgtypes.PeerConfig) error {
	actual, err := d.wg.Device(d.opts.InterfaceName)
	if err != nil {
		return fmt.Errorf("wg device: %w", err)
	}
	existing := make(map[wgtypes.Key]bool)
	for _, p := range actual.Peers {
		existing[p.PublicKey] = true
	}
	next := make(map[wgtypes.Key]string)
	for i := range peers {
		key := peers[i].PublicKey
		intent := cfg.Peers[i].Endpoint
		previous, known := d.endpoints[key]
		if existing[key] && known && previous == intent {
			peers[i].Endpoint = nil
		}
		next[key] = intent
	}
	for key := range existing {
		if _, ok := next[key]; !ok {
			peers = append(peers, wgtypes.PeerConfig{PublicKey: key, Remove: true})
		}
	}
	priv := wgtypes.Key(cfg.PrivateKey)
	port := int(cfg.ListenPort)
	config := wgtypes.Config{PrivateKey: &priv, Peers: peers}
	if port != 0 {
		config.ListenPort = &port
	}
	if err := d.wg.ConfigureDevice(d.opts.InterfaceName, config); err != nil {
		d.endpoints = nil
		return err
	}
	d.endpoints = next
	return nil
}

func (d *linuxDevice) applyRoutes(cfg *wg.DeviceConfig) error {
	for _, p := range cfg.Peers {
		for _, a := range p.AllowedIPs {
			route := &netlink.Route{
				LinkIndex: d.link.Attrs().Index,
				Dst: &net.IPNet{
					IP:   a.Addr().AsSlice(),
					Mask: net.CIDRMask(a.Bits(), a.Addr().BitLen()),
				},
				Scope: netlink.SCOPE_LINK,
			}
			if err := netlink.RouteAdd(route); err != nil && !isExist(err) {
				return fmt.Errorf("route add %s: %w", a, err)
			}
		}
	}
	return nil
}

// isExist returns true for the "already exists" error netlink returns
// when adding an entry that's already there. Matching by string keeps
// us decoupled from the syscall package.
func isExist(err error) bool {
	return err != nil && err.Error() == "file exists"
}
