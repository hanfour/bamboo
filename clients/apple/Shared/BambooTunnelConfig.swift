// SPDX-License-Identifier: Apache-2.0

import Foundation

// The wire shapes the app hands the tunnel extension. They live apart from
// TunnelConfigurationBuilder so code that only needs the data (PeerState,
// tests) does not have to link WireGuardKit.

/// Wire shape of one peer entry stored in NETunnelProviderProtocol's
/// providerConfiguration. The app constructs these from the controller's
/// /api/v1/peers/register response and hands them to the extension.
public struct BambooPeerConfig: Codable, Equatable {
    public let id: String
    public let publicKey: String          // Curve25519, base64
    public let allowedIPs: [String]       // CIDRs the peer should be reachable on
    public let endpoint: String?          // host:port; nil while STUN/relay isn't wired
    public let persistentKeepalive: UInt16?

    public init(id: String,
                publicKey: String,
                allowedIPs: [String],
                endpoint: String? = nil,
                persistentKeepalive: UInt16? = nil) {
        self.id = id
        self.publicKey = publicKey
        self.allowedIPs = allowedIPs
        self.endpoint = endpoint
        self.persistentKeepalive = persistentKeepalive
    }
}

/// Wire shape that the app stores in providerConfiguration. The
/// extension decodes this on every startTunnel, builds a
/// WireGuardKit.TunnelConfiguration, and hands it to the adapter.
public struct BambooTunnelConfig: Codable, Equatable {
    public let privateKey: String         // Curve25519, base64
    public let address: String            // tunnel IPv4, e.g. "100.64.0.5/32"
    /// tunnel IPv6 ULA, e.g. "fdba:1100::6440:5/128" (NAT64 Phase A).
    /// nil for configs built against a pre-#232 controller or restored
    /// from a pre-dual-stack stored config.
    public let address6: String?
    public let dnsServers: [String]
    public let mtu: UInt16?
    /// UDP port WireGuard listens on locally. The same port is used
    /// for STUN discovery so the advertised endpoint matches what
    /// other peers can actually dial through this host's NAT.
    /// 0 means "let the kernel pick" — pre-Finding-#4 behaviour,
    /// kept for backwards compatibility with stored configs.
    public let wgListenPort: UInt16
    public let peers: [BambooPeerConfig]
    /// Lets the extension keep the device online on the controller while
    /// the app is suspended (iOS). nil from configs written before this
    /// field existed, in which case the extension does not heartbeat.
    public let presence: PresenceConfig?

    public init(privateKey: String,
                address: String,
                address6: String? = nil,
                dnsServers: [String] = [],
                mtu: UInt16? = nil,
                wgListenPort: UInt16 = 0,
                peers: [BambooPeerConfig] = [],
                presence: PresenceConfig? = nil) {
        self.privateKey = privateKey
        self.address = address
        self.address6 = address6
        self.dnsServers = dnsServers
        self.mtu = mtu
        self.wgListenPort = wgListenPort
        self.peers = peers
        self.presence = presence
    }
}
