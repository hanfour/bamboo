// SPDX-License-Identifier: Apache-2.0

import Foundation
import Network
import WireGuardKit

public enum TunnelConfigurationBuilderError: Error {
    case invalidPrivateKey
    case invalidAddress
    case invalidAddress6
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
        // Dual-stack (NAT64 Phase A): append the IPv6 ULA so WireGuardKit's
        // adapter derives NEIPv6Settings alongside the v4 NEIPv4Settings.
        if let raw6 = config.address6 {
            guard let address6 = IPAddressRange(from: raw6) else {
                throw TunnelConfigurationBuilderError.invalidAddress6
            }
            interface.addresses.append(address6)
        }
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
