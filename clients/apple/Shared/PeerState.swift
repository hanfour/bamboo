// SPDX-License-Identifier: Apache-2.0

import Foundation

/// PeerState is the mesh view a client builds from the controller: who the
/// peers are, what each may reach, which policy revision that reflects, and
/// the relay endpoint to use for each. It turns that into the
/// BambooTunnelConfig the extension applies.
///
/// It is a plain value with no I/O so the same logic can run in the app today
/// and in the tunnel extension (where it has to live for the tunnel to stay
/// correct while the app is suspended), and so it can be unit tested. The
/// ConnectionViewModel used to hold these fields and the rules below inline.
public struct PeerState: Equatable {
    public var selfID: String?
    public var selfIPv4: String?
    public var selfIPv6: String?
    /// Every known peer by id, including self (the MagicDNS map covers
    /// `<self-hostname>.bamboo` too).
    public var peers: [String: BambooClient.PeerJSON] = [:]
    /// Controller-computed AllowedIps per peer, kept apart from `peers`: a
    /// WatchPeers event carries no AllowedIps, so replacing a peer from an
    /// event must not reset it to deny.
    public var allowedIPs: [String: [String]] = [:]
    public var policyRevision: Int64 = 0
    /// peer id -> relay proxy endpoint (127.0.0.1:<port>). When present it
    /// wins over the peer's own endpoint, so a watch-driven rebuild does not
    /// swap a working relay path for a STUN candidate.
    public var relayEndpoints: [String: String] = [:]

    public init() {}

    /// State right after a successful register.
    public init(register resp: BambooClient.RegisterResponse, relayEndpoints: [String: String] = [:]) {
        selfID = resp.self_.id
        selfIPv4 = resp.self_.ip
        selfIPv6 = resp.self_.ip6
        self.relayEndpoints = relayEndpoints
        adopt(register: resp)
    }

    /// Replace the peer set, AllowedIps and policy revision from a register
    /// response. Self identity and relay endpoints are left alone: a policy
    /// refresh must not move the device or drop its relay proxies.
    public mutating func adopt(register resp: BambooClient.RegisterResponse) {
        policyRevision = resp.policyRevision
        allowedIPs = Self.allowedIPMap(from: resp.peers)
        var seed: [String: BambooClient.PeerJSON] = [resp.self_.id: resp.self_]
        for p in resp.peers { seed[p.id] = p }
        peers = seed
    }

    /// True once the controller enforces a policy.
    public var enforcing: Bool { policyRevision > 0 }

    /// The AllowedIPs for one peer, or nil when policy denies it and it must
    /// be left out of the WireGuard config entirely:
    ///   - a non-empty controller-computed set is always used;
    ///   - enforcing with none        -> nil (deny);
    ///   - not enforcing with none    -> ["<ip>/32"], the pre-policy full
    ///     mesh, so a current client still works against a legacy controller.
    public func allowedIPs(for p: BambooClient.PeerJSON) -> [String]? {
        if let cached = allowedIPs[p.id], !cached.isEmpty {
            return cached
        }
        if enforcing {
            return nil
        }
        return ["\(p.ip)/32"]
    }

    /// (id -> AllowedIPs) from a register response. Peers whose allowedIps is
    /// nil (a legacy controller) are absent, so lookup falls back as above.
    public static func allowedIPMap(from peers: [BambooClient.PeerJSON]) -> [String: [String]] {
        var out: [String: [String]] = [:]
        for p in peers {
            if let allowed = p.allowedIps {
                out[p.id] = allowed
            }
        }
        return out
    }

    /// WireGuard peers for the current state: everyone but self, minus those
    /// policy denies, sorted by id. The order is fixed so two equal states
    /// yield equal configs and the caller's no-op-skip works.
    public func peerConfigs() -> [BambooPeerConfig] {
        peers.values
            .filter { $0.id != selfID }
            .compactMap { p -> BambooPeerConfig? in
                guard let allowed = allowedIPs(for: p) else { return nil }
                return BambooPeerConfig(
                    id: p.id,
                    publicKey: p.wireguardPublicKey,
                    allowedIPs: allowed,
                    endpoint: relayEndpoints[p.id] ?? p.endpoints?.first,
                    persistentKeepalive: 25
                )
            }
            .sorted { $0.id < $1.id }
    }

    /// The config the extension applies, or nil before this device has an
    /// address (not registered yet).
    public func tunnelConfig(privateKey: String,
                             dnsServers: [String],
                             mtu: UInt16?,
                             wgListenPort: UInt16,
                             presence: PresenceConfig?,
                             relay: RelayConfig? = nil) -> BambooTunnelConfig? {
        guard let selfIPv4 else { return nil }
        return BambooTunnelConfig(
            privateKey: privateKey,
            address: "\(selfIPv4)/32",
            address6: selfIPv6.map { "\($0)/128" },
            dnsServers: dnsServers,
            mtu: mtu,
            wgListenPort: wgListenPort,
            peers: peerConfigs(),
            presence: presence,
            relay: relay
        )
    }
}
