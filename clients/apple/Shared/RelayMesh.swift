// SPDX-License-Identifier: Apache-2.0

import Foundation
import OSLog

/// RelayMesh runs inside the packet-tunnel extension and carries every
/// peer's WireGuard traffic through a relay server.
///
/// It used to live in the app. iOS suspends the app when the screen locks,
/// which killed the WebSocket and the localhost proxies WireGuard was
/// pointed at, so all traffic stopped within seconds of locking. The
/// extension keeps running for as long as the tunnel is up.
///
/// apply() takes the config the app sent and returns the same config with
/// each peer's endpoint replaced by that peer's local proxy address. When
/// the relay is not configured or cannot be reached it returns the config
/// unchanged, so peers fall back to their direct endpoints.
public actor RelayMesh {
    private let log = Logger(subsystem: "dev.hanfour.bamboo.app.tunnel", category: "relay-mesh")

    private var client: RelayClient?
    private var relay: RelayConfig?
    private var wgPort: UInt16 = 0
    private var auth: PresenceConfig?
    private var proxied: [String: String] = [:]   // peer public key (base64) -> proxy endpoint
    private var redialTask: Task<Void, Never>?

    // Direct-path upgrade. See PathSelector.
    private var selector = PathSelector()
    private var observations: [String: WGPeerObservation] = [:]
    private var lastPeers: [BambooPeerConfig] = []
    private var appliedPaths: [String: PeerPath] = [:]
    private var pathStats: (@Sendable () async -> String?)?
    private var onPathChange: (@Sendable () async -> Void)?
    private var pathTask: Task<Void, Never>?
    private let lanAddresses: @Sendable () -> [String]

    public init(lanAddresses: @escaping @Sendable () -> [String] = { EndpointDiscovery.lanIPv4Addresses() }) {
        self.lanAddresses = lanAddresses
    }

    /// Lets the mesh watch WireGuard and move peers between the relay and
    /// a direct address. `stats` returns the UAPI dump; `changed` asks the
    /// owner to apply the config again once a peer's path moved.
    public func enableDirectPaths(stats: @escaping @Sendable () async -> String?,
                                  changed: @escaping @Sendable () async -> Void) {
        pathStats = stats
        onPathChange = changed
        startPathTask()
    }

    public func apply(_ config: BambooTunnelConfig, auth newAuth: PresenceConfig?) async -> BambooTunnelConfig {
        guard let relayCfg = config.relay, let url = URL(string: relayCfg.url), let newAuth else {
            await closeRelay()
            return config
        }
        auth = newAuth
        // A different relay, key, or WireGuard port needs a new client,
        // because the proxies are bound to the WireGuard listen port.
        if client != nil, relay != relayCfg || wgPort != config.wgListenPort {
            await closeClient()
        }
        if client == nil {
            do {
                client = try await dial(relayCfg, url: url, wgPort: config.wgListenPort, auth: newAuth)
                relay = relayCfg
                wgPort = config.wgListenPort
                log.log("relay-mesh: connected url=\(relayCfg.url, privacy: .public)")
            } catch {
                log.warning("relay-mesh: dial failed, peers stay direct: \(String(describing: error), privacy: .public)")
                return config
            }
        }
        guard let client else { return config }

        lastPeers = config.peers
        let paths = resolvePaths(now: Date())
        appliedPaths = paths

        var peers: [BambooPeerConfig] = []
        var live = Set<String>()
        for p in config.peers {
            guard let key = Data(base64Encoded: p.publicKey), key.count == RelayClient.pubKeyLen else {
                peers.append(p.withoutCandidates())
                continue
            }
            live.insert(p.publicKey)
            do {
                var endpoint = try await client.addPeer(key)
                proxied[p.publicKey] = endpoint
                if case .direct(let d) = paths[p.publicKey] { endpoint = d }
                peers.append(BambooPeerConfig(id: p.id, publicKey: p.publicKey, allowedIPs: p.allowedIPs,
                                              endpoint: endpoint, persistentKeepalive: p.persistentKeepalive))
            } catch {
                log.warning("relay-mesh: addPeer \(p.id, privacy: .public): \(String(describing: error), privacy: .public)")
                peers.append(p.withoutCandidates())
            }
        }
        for gone in proxied.keys where !live.contains(gone) {
            if let key = Data(base64Encoded: gone) { await client.removePeer(key) }
            proxied.removeValue(forKey: gone)
        }
        selector.retain(live)
        return config.replacingPeers(peers)
    }

    // MARK: - direct paths

    /// The path each relayed peer should use now. Peers with no usable
    /// direct address, and every peer while this device has no LAN or Wi-Fi
    /// address (cellular), stay on the relay.
    private func resolvePaths(now: Date) -> [String: PeerPath] {
        let lan = lanAddresses()
        var out: [String: PeerPath] = [:]
        for p in lastPeers {
            let usable = lan.isEmpty || pathStats == nil ? [] : (p.candidates ?? []).filter {
                EndpointDiscovery.isReachableLAN($0, from: lan)
            }
            out[p.publicKey] = selector.path(peer: p.publicKey, candidates: usable,
                                             observation: observations[p.publicKey], now: now)
        }
        return out
    }

    private func startPathTask() {
        pathTask?.cancel()
        pathTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 5_000_000_000)
                guard let self else { return }
                await self.pathTick()
            }
        }
    }

    private func pathTick() async {
        guard client != nil, let pathStats, let onPathChange,
              lastPeers.contains(where: { !($0.candidates ?? []).isEmpty }),
              let dump = await pathStats() else { return }
        observations = WGStatsParser.observations(dump)
        let next = resolvePaths(now: Date())
        guard next != appliedPaths else { return }
        for (key, path) in next where appliedPaths[key] != path {
            log.log("relay-mesh: path \(String(key.prefix(6)), privacy: .public) -> \(String(describing: path), privacy: .public)")
        }
        await onPathChange()
    }

    /// The session token was renewed; later relay tokens must use it.
    public func updateAuth(_ newAuth: PresenceConfig) {
        auth = newAuth
    }

    /// The network changed under us (Wi-Fi to cellular, resume from sleep).
    /// A WebSocket on the old path can sit dead for minutes before the read
    /// fails, so replace it now.
    public func forceRedial() {
        guard client != nil else { return }
        scheduleRedial()
    }

    public func stop() async {
        pathTask?.cancel()
        pathTask = nil
        pathStats = nil
        onPathChange = nil
        await closeRelay()
    }

    /// Drops the relay connection and everything learned about paths. The
    /// path hooks stay, so a later config with a relay picks up again.
    private func closeRelay() async {
        selector = PathSelector()
        observations.removeAll()
        appliedPaths.removeAll()
        lastPeers.removeAll()
        redialTask?.cancel()
        redialTask = nil
        await closeClient()
        relay = nil
        auth = nil
    }

    // MARK: - private

    private func closeClient() async {
        if let c = client {
            await c.setOnDisconnect(nil)
            await c.close()
        }
        client = nil
        proxied.removeAll()
    }

    private func mintToken(_ relayCfg: RelayConfig, auth: PresenceConfig) async throws -> String {
        guard let base = URL(string: auth.controllerURL) else { throw BambooClientError.invalidResponse }
        let api = BambooClient(baseURL: base,
                               peerSessionToken: auth.sessionToken,
                               tenantSlug: auth.tenantSlug.isEmpty ? nil : auth.tenantSlug)
        let resp = try await api.relayToken(.init(peerId: auth.peerID, wireguardPublicKey: relayCfg.selfPublicKey))
        return resp.token
    }

    private func dial(_ relayCfg: RelayConfig, url: URL, wgPort: UInt16, auth: PresenceConfig) async throws -> RelayClient {
        guard let selfKey = Data(base64Encoded: relayCfg.selfPublicKey), selfKey.count == RelayClient.pubKeyLen else {
            throw BambooClientError.invalidResponse
        }
        let token = try await mintToken(relayCfg, auth: auth)
        let c = RelayClient(selfKey: selfKey, token: token, wgListenHost: "127.0.0.1", wgListenPort: wgPort)
        try await c.dial(relayURL: url)
        await c.setOnDisconnect { [weak self] in
            Task { await self?.scheduleRedial() }
        }
        return c
    }

    /// The WebSocket died (relay restart, network change). Redial with a
    /// fresh token and backoff. The proxies keep their ports, so WireGuard
    /// needs no new config.
    private func scheduleRedial() {
        guard redialTask == nil else { return }
        redialTask = Task { [weak self] in
            var delay: UInt64 = 1
            while !Task.isCancelled {
                guard let self else { return }
                if await self.tryRedial() { break }
                try? await Task.sleep(nanoseconds: delay * 1_000_000_000)
                delay = min(delay * 2, 30)
            }
            await self?.redialDone()
        }
    }

    private func redialDone() { redialTask = nil }

    private func tryRedial() async -> Bool {
        guard let client, let relay, let auth else { return true }
        do {
            let token = try await mintToken(relay, auth: auth)
            try await client.redial(token: token)
            log.log("relay-mesh: redialed")
            return true
        } catch {
            log.warning("relay-mesh: redial failed: \(String(describing: error), privacy: .public)")
            return false
        }
    }
}
