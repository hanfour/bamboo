// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package device

import (
	"context"
	"fmt"
	"net"

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
	opts Options
	wg   *wgctrl.Client
	link netlink.Link
}

// Apply implements Device.
func (d *linuxDevice) Apply(_ context.Context, cfg *wg.DeviceConfig) error {
	if err := d.ensureLink(); err != nil {
		return err
	}
	if err := d.applyAddress(cfg); err != nil {
		return err
	}
	if err := d.applyWireGuard(cfg); err != nil {
		return err
	}
	if err := netlink.LinkSetUp(d.link); err != nil {
		return fmt.Errorf("link up: %w", err)
	}
	return d.applyRoutes(cfg)
}

// Stats implements Device.
func (d *linuxDevice) Stats() ([]PeerStats, error) {
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
	addr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   cfg.Address.Addr().AsSlice(),
			Mask: net.CIDRMask(cfg.Address.Bits(), cfg.Address.Addr().BitLen()),
		},
	}
	// Idempotent: ignore EEXIST.
	if err := netlink.AddrAdd(d.link, addr); err != nil && !isExist(err) {
		return fmt.Errorf("addr add: %w", err)
	}
	return nil
}

func (d *linuxDevice) applyWireGuard(cfg *wg.DeviceConfig) error {
	priv, err := wgtypes.NewKey(cfg.PrivateKey[:])
	if err != nil {
		return fmt.Errorf("wgtypes private key: %w", err)
	}

	peers := make([]wgtypes.PeerConfig, 0, len(cfg.Peers))
	for _, p := range cfg.Peers {
		pub, err := wgtypes.NewKey(p.PublicKey[:])
		if err != nil {
			return fmt.Errorf("wgtypes peer pubkey: %w", err)
		}
		allowed := make([]net.IPNet, 0, len(p.AllowedIPs))
		for _, a := range p.AllowedIPs {
			allowed = append(allowed, net.IPNet{
				IP:   a.Addr().AsSlice(),
				Mask: net.CIDRMask(a.Bits(), a.Addr().BitLen()),
			})
		}
		pc := wgtypes.PeerConfig{
			PublicKey:         pub,
			ReplaceAllowedIPs: true,
			AllowedIPs:        allowed,
		}
		if p.Endpoint != "" {
			ep, err := net.ResolveUDPAddr("udp", p.Endpoint)
			if err == nil {
				pc.Endpoint = ep
			}
		}
		if p.PersistentKeepalive > 0 {
			ka := p.PersistentKeepalive
			pc.PersistentKeepaliveInterval = &ka
		}
		peers = append(peers, pc)
	}

	port := int(cfg.ListenPort)
	wgcfg := wgtypes.Config{
		PrivateKey:   &priv,
		ListenPort:   &port,
		ReplacePeers: true,
		Peers:        peers,
	}
	if cfg.ListenPort == 0 {
		wgcfg.ListenPort = nil
	}
	return d.wg.ConfigureDevice(d.opts.InterfaceName, wgcfg)
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
