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

    public init() {}

    public func apply(_ config: BambooTunnelConfig, auth newAuth: PresenceConfig?) async -> BambooTunnelConfig {
        guard let relayCfg = config.relay, let url = URL(string: relayCfg.url), let newAuth else {
            await stop()
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

        var peers: [BambooPeerConfig] = []
        var live = Set<String>()
        for p in config.peers {
            guard let key = Data(base64Encoded: p.publicKey), key.count == RelayClient.pubKeyLen else {
                peers.append(p)
                continue
            }
            live.insert(p.publicKey)
            do {
                let endpoint = try await client.addPeer(key)
                proxied[p.publicKey] = endpoint
                peers.append(BambooPeerConfig(id: p.id, publicKey: p.publicKey, allowedIPs: p.allowedIPs,
                                              endpoint: endpoint, persistentKeepalive: p.persistentKeepalive))
            } catch {
                log.warning("relay-mesh: addPeer \(p.id, privacy: .public): \(String(describing: error), privacy: .public)")
                peers.append(p)
            }
        }
        for gone in proxied.keys where !live.contains(gone) {
            if let key = Data(base64Encoded: gone) { await client.removePeer(key) }
            proxied.removeValue(forKey: gone)
        }
        return config.replacingPeers(peers)
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
