// SPDX-License-Identifier: AGPL-3.0-or-later

package wg

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// DeviceConfig is the WireGuard configuration this peer would apply to
// its local interface. It is a faithful subset of `wg-quick` config,
// limited to what the controller drives.
type DeviceConfig struct {
	// PrivateKey is the local secret. Never log or transmit.
	PrivateKey PrivateKey
	// Address is the IP this peer holds inside the mesh, with prefix length.
	Address netip.Prefix
	// ListenPort is the UDP port WireGuard binds locally. Zero means
	// "let the kernel pick".
	ListenPort uint16
	// Peers is the full set of remote peers.
	Peers []PeerConfig
}

// PeerConfig describes one remote peer.
type PeerConfig struct {
	PublicKey PublicKey
	// AllowedIPs are CIDRs that should route through this peer.
	AllowedIPs []netip.Prefix
	// Endpoint is the candidate ip:port to connect to. Empty if we expect
	// the peer to initiate.
	Endpoint string
	// PersistentKeepalive forces a keepalive packet every N seconds. Zero
	// to disable.
	PersistentKeepalive time.Duration
}

// BuildDeviceConfig assembles a DeviceConfig from a controller register
// response and the local private key.
//
// Responsibilities:
//   - The local Address is taken from resp.Self.Ip (host route, /32).
//   - Endpoints are taken from peer.Endpoints (first non-empty wins
//     for now; ICE-driven path selection lands in a follow-up).
//   - AllowedIPs come from one of two paths, decided by PolicyRevision:
//
// When resp.PolicyRevision > 0 the controller is enforcing an authored
// ACL: peer.AllowedIps is authoritative. A peer with an empty
// AllowedIps slice is intentionally excluded — the controller's policy
// engine determined this peer is unreachable from us, and listing it
// in WireGuard's peer set with no routes would still expose the public
// key for no benefit.
//
// When resp.PolicyRevision == 0 the controller has no authored ACL.
// peer.AllowedIps is still used when present, because the controller's
// allowedIPsFor merges the peer's tunnel /32 with any admin-approved
// subnet routes (#136) and exit-node defaults (#137) regardless of
// ACL state — these are policy-orthogonal admin sign-offs. Only when
// AllowedIps is absent (legacy controller pre-#149) do we fall back
// to deriving a /32 from peer.IP.
func BuildDeviceConfig(privKey PrivateKey, resp *bamboov1.RegisterResponse) (*DeviceConfig, error) {
	if resp == nil || resp.GetSelf() == nil {
		return nil, fmt.Errorf("register response missing self")
	}

	selfAddr, err := netip.ParseAddr(resp.GetSelf().GetIp())
	if err != nil {
		return nil, fmt.Errorf("parse self ip %q: %w", resp.GetSelf().GetIp(), err)
	}
	prefixLen := 32
	if selfAddr.Is6() {
		prefixLen = 128
	}
	selfPrefix := netip.PrefixFrom(selfAddr, prefixLen)

	cfg := &DeviceConfig{
		PrivateKey: privKey,
		Address:    selfPrefix,
	}

	enforcing := resp.GetPolicyRevision() > 0

	for _, p := range resp.GetPeers() {
		pubKey, err := PublicKeyFromBase64(p.GetWireguardPublicKey())
		if err != nil {
			return nil, fmt.Errorf("peer %s pubkey: %w", p.GetId(), err)
		}

		var allowedIPs []netip.Prefix
		switch {
		case enforcing && len(p.GetAllowedIps()) == 0:
			// Policy denies traffic to this peer — drop it entirely.
			continue
		case len(p.GetAllowedIps()) > 0:
			// Use controller-supplied list. Carries the peer's /32
			// plus any admin-approved subnet routes (#136) and exit-
			// node defaults (#137). Same parse path under both
			// enforcing and non-enforcing modes — the policy gate
			// already filtered above for the deny case.
			for _, s := range p.GetAllowedIps() {
				pre, err := netip.ParsePrefix(s)
				if err != nil {
					return nil, fmt.Errorf("peer %s allowed_ips %q: %w", p.GetId(), s, err)
				}
				allowedIPs = append(allowedIPs, pre)
			}
		default:
			// Legacy controller pre-#149 omitted AllowedIps entirely
			// when policy_revision was zero; derive a /32 (or /128)
			// from the tunnel IP so this client keeps talking to those
			// peers via direct mesh.
			peerAddr, err := netip.ParseAddr(p.GetIp())
			if err != nil {
				return nil, fmt.Errorf("peer %s ip: %w", p.GetId(), err)
			}
			peerLen := 32
			if peerAddr.Is6() {
				peerLen = 128
			}
			allowedIPs = []netip.Prefix{netip.PrefixFrom(peerAddr, peerLen)}
		}

		pc := PeerConfig{
			PublicKey:           pubKey,
			AllowedIPs:          allowedIPs,
			PersistentKeepalive: 25 * time.Second,
		}
		if eps := p.GetEndpoints(); len(eps) > 0 {
			pc.Endpoint = eps[0]
		}
		cfg.Peers = append(cfg.Peers, pc)
	}
	return cfg, nil
}

// WGQuick renders cfg as a wg-quick(8) compatible config file. Useful for
// dev preview and for users who prefer to drive the interface with the
// stock tooling. Production code path should drive netlink / wgctrl
// directly.
func (c *DeviceConfig) WGQuick() string {
	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", c.PrivateKey.Base64())
	fmt.Fprintf(&b, "Address    = %s\n", c.Address.String())
	if c.ListenPort != 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", c.ListenPort)
	}

	for _, p := range c.Peers {
		b.WriteString("\n[Peer]\n")
		fmt.Fprintf(&b, "PublicKey  = %s\n", p.PublicKey.Base64())
		allowed := make([]string, 0, len(p.AllowedIPs))
		for _, a := range p.AllowedIPs {
			allowed = append(allowed, a.String())
		}
		fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.Join(allowed, ", "))
		if p.Endpoint != "" {
			fmt.Fprintf(&b, "Endpoint   = %s\n", p.Endpoint)
		}
		if p.PersistentKeepalive > 0 {
			fmt.Fprintf(&b, "PersistentKeepalive = %d\n", int(p.PersistentKeepalive.Seconds()))
		}
	}
	return b.String()
}
