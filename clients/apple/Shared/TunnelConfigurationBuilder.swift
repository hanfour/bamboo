// SPDX-License-Identifier: Apache-2.0

import Foundation
import Network
import WireGuardKit

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
    public let dnsServers: [String]
    public let mtu: UInt16?
    /// UDP port WireGuard listens on locally. The same port is used
    /// for STUN discovery so the advertised endpoint matches what
    /// other peers can actually dial through this host's NAT.
    /// 0 means "let the kernel pick" — pre-Finding-#4 behaviour,
    /// kept for backwards compatibility with stored configs.
    public let wgListenPort: UInt16
    public let peers: [BambooPeerConfig]

    public init(privateKey: String,
                address: String,
                dnsServers: [String] = [],
                mtu: UInt16? = nil,
                wgListenPort: UInt16 = 0,
                peers: [BambooPeerConfig] = []) {
        self.privateKey = privateKey
        self.address = address
        self.dnsServers = dnsServers
        self.mtu = mtu
        self.wgListenPort = wgListenPort
        self.peers = peers
    }
}

public enum TunnelConfigurationBuilderError: Error {
    case invalidPrivateKey
    case invalidAddress
    case invalidPeerPublicKey(String)
    case invalidAllowedIP(String)
    case invalidEndpoint(String)
}

/// TunnelConfigurationBuilder turns the app-side BambooTunnelConfig
/// into a WireGuardKit TunnelConfiguration the extension's adapter can
/// consume. Centralised here so both platforms share validation.
public enum TunnelConfigurationBuilder {
    public static func build(from config: BambooTunnelConfig) throws -> TunnelConfiguration {
        guard let privKey = PrivateKey(base64Key: config.privateKey) else {
            throw TunnelConfigurationBuilderError.invalidPrivateKey
        }

        var interface = InterfaceConfiguration(privateKey: privKey)
        guard let address = IPAddressRange(from: config.address) else {
            throw TunnelConfigurationBuilderError.invalidAddress
        }
        interface.addresses = [address]
        interface.dns = config.dnsServers.compactMap { DNSServer(from: $0) }
        if let mtu = config.mtu {
            interface.mtu = mtu
        }
        if config.wgListenPort != 0 {
            interface.listenPort = config.wgListenPort
        }

        var peers: [PeerConfiguration] = []
        peers.reserveCapacity(config.peers.count)
        for p in config.peers {
            guard let pubKey = PublicKey(base64Key: p.publicKey) else {
                throw TunnelConfigurationBuilderError.invalidPeerPublicKey(p.publicKey)
            }
            var peer = PeerConfiguration(publicKey: pubKey)
            peer.allowedIPs = try p.allowedIPs.map { raw in
                guard let r = IPAddressRange(from: raw) else {
                    throw TunnelConfigurationBuilderError.invalidAllowedIP(raw)
                }
                return r
            }
            if let ep = p.endpoint {
                guard let parsed = Endpoint(from: ep) else {
                    throw TunnelConfigurationBuilderError.invalidEndpoint(ep)
                }
                peer.endpoint = parsed
            }
            if let ka = p.persistentKeepalive, ka > 0 {
                peer.persistentKeepAlive = ka
            }
            peers.append(peer)
        }

        return TunnelConfiguration(name: "bamboo", interface: interface, peers: peers)
    }
}
