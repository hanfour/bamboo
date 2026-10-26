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

/// Tells the extension to carry every peer's traffic through a relay
/// server. The extension, not the app, owns the WebSocket and the
/// per-peer local proxies, because iOS suspends the app (and everything
/// in it) when the screen locks. selfPublicKey is this device's WireGuard
/// public key, base64, which the relay uses to address inbound packets.
public struct RelayConfig: Codable, Equatable {
    public let url: String
    public let selfPublicKey: String

    public init(url: String, selfPublicKey: String) {
        self.url = url
        self.selfPublicKey = selfPublicKey
    }
}

/// What the extension needs to run the control plane by itself (register,
/// heartbeat, watch) while the host app is suspended. iOS only today; on
/// macOS the app still drives the control plane. Credentials are limited to
/// the peer session token (about 24h, renewable by the extension).
public struct ControlPlaneConfig: Codable, Equatable {
    public let controllerURL: String
    public let tenantSlug: String
    public let peerID: String
    public let sessionToken: String
    public let hostname: String
    public let wireguardPublicKey: String
    public let os: String
    public let clientVersion: String
    public let advertisedRoutes: [String]?
    public let advertiseExitNode: Bool?

    public init(controllerURL: String, tenantSlug: String, peerID: String, sessionToken: String,
                hostname: String, wireguardPublicKey: String, os: String, clientVersion: String,
                advertisedRoutes: [String]? = nil, advertiseExitNode: Bool? = nil) {
        self.controllerURL = controllerURL
        self.tenantSlug = tenantSlug
        self.peerID = peerID
        self.sessionToken = sessionToken
        self.hostname = hostname
        self.wireguardPublicKey = wireguardPublicKey
        self.os = os
        self.clientVersion = clientVersion
        self.advertisedRoutes = advertisedRoutes
        self.advertiseExitNode = advertiseExitNode
    }

    /// The same identity in the shape the relay and presence code use.
    public func asPresence(token: String? = nil) -> PresenceConfig {
        PresenceConfig(controllerURL: controllerURL, tenantSlug: tenantSlug, peerID: peerID,
                       sessionToken: token ?? sessionToken)
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
    /// nil means direct-only: peers keep the endpoints in `peers`.
    public let relay: RelayConfig?
    /// When set, the extension runs register, heartbeat and watch itself and
    /// the app stops doing so. nil keeps the app in charge.
    public let controlPlane: ControlPlaneConfig?

    public init(privateKey: String,
                address: String,
                address6: String? = nil,
                dnsServers: [String] = [],
                mtu: UInt16? = nil,
                wgListenPort: UInt16 = 0,
                peers: [BambooPeerConfig] = [],
                presence: PresenceConfig? = nil,
                relay: RelayConfig? = nil,
                controlPlane: ControlPlaneConfig? = nil) {
        self.privateKey = privateKey
        self.address = address
        self.address6 = address6
        self.dnsServers = dnsServers
        self.mtu = mtu
        self.wgListenPort = wgListenPort
        self.peers = peers
        self.presence = presence
        self.relay = relay
        self.controlPlane = controlPlane
    }

    /// Same config with the peer list replaced. The extension uses it to
    /// swap in relay proxy endpoints without touching anything else.
    public func replacingPeers(_ newPeers: [BambooPeerConfig]) -> BambooTunnelConfig {
        BambooTunnelConfig(privateKey: privateKey, address: address, address6: address6,
                           dnsServers: dnsServers, mtu: mtu, wgListenPort: wgListenPort,
                           peers: newPeers, presence: presence, relay: relay,
                           controlPlane: controlPlane)
    }
}
