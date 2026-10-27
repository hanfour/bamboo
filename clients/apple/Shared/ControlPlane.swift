// SPDX-License-Identifier: Apache-2.0

import Foundation
import Network
import OSLog

/// ControlPlane runs inside the packet-tunnel extension and keeps this
/// device's view of the mesh current while the host app is suspended.
///
/// It registers with the controller, heartbeats every 30s, consumes the
/// peer watch stream, and re-emits a BambooTunnelConfig whenever the peer
/// set, AllowedIPs, or policy changes. On a network change it heartbeats
/// immediately, restarts the watch stream, and asks the relay to redial.
///
/// The app used to do all of this in ConnectionViewModel. iOS suspends the
/// app on lock, so new peers and policy changes never reached a locked
/// phone. The mesh rules themselves live in PeerState, which has no I/O.
public actor ControlPlane {
    public static let heartbeatInterval: TimeInterval = 30

    private let log = Logger(subsystem: "dev.hanfour.bamboo.app.tunnel", category: "control-plane")

    private var cfg: ControlPlaneConfig?
    private var base: BambooTunnelConfig?
    private var token: String = ""
    private var net = PeerState()
    private var nat64: (enabled: Bool, prefix: String) = (false, "")
    private var lastEmitted: BambooTunnelConfig?
    private var lastEndpoints: [String]?

    private var heartbeatTask: Task<Void, Never>?
    private let watcher = PeerWatcher()
    private var monitor: NWPathMonitor?
    private var lastPathSignature: String?
    private var watchStartedAt = Date.distantPast
    private var debounce: Task<Void, Never>?

    private var bandwidth: (@Sendable () async -> TunnelIPC.BandwidthStats?)?
    private var apply: (@Sendable (BambooTunnelConfig) async -> Void)?
    private var tokenRenewed: (@Sendable (PresenceConfig) async -> Void)?
    private var networkChanged: (@Sendable () async -> Void)?

    public init() {}

    public var isRunning: Bool { cfg != nil }

    /// Identity plus the newest session token, for components that
    /// authenticate as this peer (the relay).
    public func auth() -> PresenceConfig? {
        cfg?.asPresence(token: token)
    }

    public func snapshot() -> TunnelIPC.PeerSnapshot {
        let entries: [TunnelIPC.PeerSnapshot.Entry] = net.peers.values.compactMap { p in
            guard let name = p.peerDnsName, !name.isEmpty else { return nil }
            return .init(dnsName: name.lowercased(), hostname: p.hostname, ip: p.ip, ip6: p.ip6)
        }
        return .init(peers: entries.sorted { $0.dnsName < $1.dnsName },
                     dns64Enabled: nat64.enabled, nat64Prefix: nat64.prefix)
    }

    /// Start (or restart) the control plane. `base` carries the pieces the
    /// app owns: private key, DNS, MTU, WireGuard port, relay. `apply`
    /// receives each new config; the provider relays it to WireGuard.
    public func start(base: BambooTunnelConfig,
                      cfg: ControlPlaneConfig,
                      bandwidth: @escaping @Sendable () async -> TunnelIPC.BandwidthStats?,
                      apply: @escaping @Sendable (BambooTunnelConfig) async -> Void,
                      tokenRenewed: @escaping @Sendable (PresenceConfig) async -> Void,
                      networkChanged: @escaping @Sendable () async -> Void) {
        stopLoops()
        self.base = base
        self.bandwidth = bandwidth
        self.apply = apply
        self.tokenRenewed = tokenRenewed
        self.networkChanged = networkChanged
        // Same identity: keep the token we may already have renewed.
        if self.cfg?.peerID != cfg.peerID || token.isEmpty {
            token = cfg.sessionToken
        }
        self.cfg = cfg
        lastEmitted = base

        startMonitor()
        startWatch()
        heartbeatTask = Task { [weak self] in
            await self?.refresh(reason: "start")
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: UInt64(ControlPlane.heartbeatInterval * 1_000_000_000))
                if Task.isCancelled { return }
                await self?.beat()
            }
        }
    }

    /// The app pushed a new base (relay swap, settings). Keep the live
    /// mesh state and re-emit with the new base.
    public func rebase(_ newBase: BambooTunnelConfig) async {
        guard cfg != nil else { return }
        base = newBase
        await emit()
    }

    public func stop() {
        stopLoops()
        cfg = nil
        base = nil
        net = PeerState()
        lastEmitted = nil
    }

    // MARK: - loops

    private func stopLoops() {
        heartbeatTask?.cancel()
        heartbeatTask = nil
        debounce?.cancel()
        debounce = nil
        monitor?.cancel()
        monitor = nil
        lastPathSignature = nil
        Task { [watcher] in await watcher.stop() }
    }

    private func startWatch() {
        guard let cfg, let url = URL(string: cfg.controllerURL) else { return }
        let peerID = cfg.peerID, slug = cfg.tenantSlug, tok = token
        watchStartedAt = Date()
        Task { [watcher, weak self] in
            let owner = self
            await watcher.start(baseURL: url, peerID: peerID, bearerToken: tok, tenantSlug: slug) { event in
                Task { await owner?.handle(event) }
            }
        }
    }

    private func startMonitor() {
        let m = NWPathMonitor()
        m.pathUpdateHandler = { [weak self] path in
            let sig = ControlPlane.signature(of: path)
            Task { await self?.pathUpdated(sig) }
        }
        m.start(queue: DispatchQueue(label: "dev.hanfour.bamboo.controlplane.path"))
        monitor = m
    }

    /// Which physical networks are usable right now. The tunnel's own
    /// interface (type .other) and loopback are left out, because applying
    /// a WireGuard config makes the OS report a new path for them and that
    /// must not count as a network change. Empty when there is no network.
    nonisolated static func signature(of path: NWPath) -> String {
        guard path.status == .satisfied else { return "" }
        let names = path.availableInterfaces
            .filter { $0.type != .other && $0.type != .loopback }
            .map { "\($0.type):\($0.name)" }
            .sorted()
        return names.joined(separator: ",")
    }

    /// Debounced because the OS reports several path updates in a row. A
    /// change in the usable interfaces (Wi-Fi to cellular, coming back
    /// online) is a network change; the first report only records state.
    private func pathUpdated(_ signature: String) {
        defer { lastPathSignature = signature }
        guard let previous = lastPathSignature, previous != signature, !signature.isEmpty else { return }
        debounce?.cancel()
        debounce = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 1_000_000_000)
            if Task.isCancelled { return }
            // Not cancelled by a later report: a heartbeat in flight must finish.
            Task { await self?.networkDidChange() }
        }
    }

    private func networkDidChange() async {
        log.log("control-plane: network changed; heartbeat, rewatch, relay redial")
        startWatch()
        await networkChanged?()
        await beat()
    }

    // MARK: - controller I/O

    private func client() -> BambooClient? {
        guard let cfg, let url = URL(string: cfg.controllerURL) else { return nil }
        return BambooClient(baseURL: url, peerSessionToken: token,
                            tenantSlug: cfg.tenantSlug.isEmpty ? nil : cfg.tenantSlug)
    }

    /// Re-register to pull the full peer set and per-peer AllowedIPs. Errors
    /// are tolerated: the tunnel keeps its prior config until the next try.
    private func refresh(reason: String) async {
        guard let cfg, let client = client() else { return }
        do {
            let resp = try await client.register(.init(
                hostname: cfg.hostname,
                wireguardPublicKey: cfg.wireguardPublicKey,
                os: cfg.os,
                clientVersion: cfg.clientVersion,
                preAuthKeySecret: nil,
                tenantSlug: cfg.tenantSlug.isEmpty ? nil : cfg.tenantSlug,
                endpoints: lastEndpoints,
                advertisedRoutes: (cfg.advertisedRoutes?.isEmpty ?? true) ? nil : cfg.advertisedRoutes,
                advertiseExitNode: cfg.advertiseExitNode == true ? true : nil
            ))
            if net.selfID == nil {
                net = PeerState(register: resp)
            } else {
                net.adopt(register: resp)
            }
            nat64 = (resp.dns64Enabled ?? false, resp.nat64Prefix ?? "")
            if let tok = resp.peerSessionToken, !tok.isEmpty { await adopt(token: tok) }
            log.log("control-plane: refresh (\(reason, privacy: .public)) revision=\(resp.policyRevision, privacy: .public) peers=\(resp.peers.count, privacy: .public)")
            await emit()
        } catch {
            log.warning("control-plane: refresh (\(reason, privacy: .public)) failed: \(String(describing: error), privacy: .public)")
        }
    }

    private var beats = 0

    private func beat() async {
        guard let cfg, let client = client() else { return }
        beats += 1
        var endpoints: [String]? = nil
        if let port = base?.wgListenPort {
            let found = await EndpointDiscovery.candidates(wgPort: port)
            if !found.isEmpty {
                endpoints = found
                lastEndpoints = found
            }
        }
        let bw = await bandwidth?()
        do {
            let resp = try await client.heartbeat(.init(
                peerId: cfg.peerID,
                knownPolicyRevision: net.policyRevision,
                endpoints: endpoints,
                bytesSent: bw?.bytesSent,
                bytesReceived: bw?.bytesReceived,
                connectionPath: bw?.connectionPath
            ))
            if let tok = resp.peerSessionToken, !tok.isEmpty { await adopt(token: tok) }
            if resp.policyChanged || resp.peersChanged {
                await refresh(reason: resp.policyChanged ? "policy" : "peers")
            } else if beats % 4 == 0 {
                // The watch stream can miss an update; peers dial each
                // other's advertised addresses now, so keep them fresh.
                await refresh(reason: "periodic")
            }
        } catch {
            log.warning("control-plane: heartbeat failed: \(String(describing: error), privacy: .public)")
        }
    }

    /// The newest peer-session token, nil before the first register.
    public func currentToken() -> String? {
        token.isEmpty ? nil : token
    }

    private func adopt(token newToken: String) async {
        guard newToken != token, let cfg else { return }
        token = newToken
        log.log("control-plane: session token renewed")
        await tokenRenewed?(cfg.asPresence(token: newToken))
        // The stream authenticates once at connect; a reconnect reuses the
        // token it was started with. Reopen it now and then so that token
        // never ages out, but not on every refresh (each mints a new one).
        if Date().timeIntervalSince(watchStartedAt) > 3600 { startWatch() }
    }

    // MARK: - watch events

    private func handle(_ event: PeerWatcher.Event) async {
        switch event {
        case .peerAdded(let p):
            net.peers[p.id] = p
            log.log("control-plane: peer_added \(p.hostname, privacy: .public)")
            // The event carries no AllowedIps, so under enforcement ask again.
            if net.enforcing { await refresh(reason: "peer_added") } else { await emit() }
        case .peerUpdated(let p):
            net.peers[p.id] = p
            log.log("control-plane: peer_updated \(p.hostname, privacy: .public) dns=\(p.peerDnsName ?? "", privacy: .public)")
            await emit()
        case .peerRemoved(let id):
            net.peers.removeValue(forKey: id)
            net.allowedIPs.removeValue(forKey: id)
            log.log("control-plane: peer_removed \(id, privacy: .public)")
            await emit()
        case .policyChanged(let rev):
            log.log("control-plane: policy_changed rev=\(rev, privacy: .public)")
            await refresh(reason: "policy_changed")
        case .relaysChanged:
            break   // picking a relay stays with the app, which pushes a new base
        }
    }

    // MARK: - config

    /// Build the config from current state and hand it on when it changed.
    private func emit() async {
        guard let base, let cfg else { return }
        guard let config = net.tunnelConfig(privateKey: base.privateKey,
                                            dnsServers: base.dnsServers,
                                            mtu: base.mtu,
                                            wgListenPort: base.wgListenPort,
                                            presence: nil,
                                            relay: base.relay,
                                            controlPlane: cfg) else { return }
        if config == lastEmitted { return }
        lastEmitted = config
        await apply?(config)
    }
}
