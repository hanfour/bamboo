// SPDX-License-Identifier: Apache-2.0

import Foundation
#if canImport(UIKit)
import UIKit
#elseif canImport(AppKit)
import AppKit
#endif
import OSLog
import WireGuardKit

/// ConnectionViewModel exposes the bamboo tunnel connection state to
/// SwiftUI on macOS and iOS. It owns:
///
///   - the persistent WireGuard private key (Keychain)
///   - a BambooClient configured to talk to the user-supplied controller URL
///   - a TunnelManager that drives NETunnelProviderManager
///
/// connect() is the main flow:
///   1. Generate (or load) the WireGuard private key
///   2. Hit /api/v1/peers/register to learn (a) our tunnel IP and
///      (b) the rest of the peer set
///   3. Persist the private key
///   4. Hand a BambooTunnelConfig to TunnelManager.startTunnel
@MainActor
public final class ConnectionViewModel: ObservableObject {
    public enum Status: String {
        case disconnected = "Not connected"
        case connecting   = "Connecting…"
        case connected    = "Connected"
        case failing      = "Failing"
    }

    @Published public var status: Status = .disconnected
    @Published public var lastError: String?
    @Published public var controllerURL: String =
        UserDefaults.bambooStandard.string(forKey: "controllerURL") ?? "http://127.0.0.1:8081"
    @Published public var tenantSlug: String =
        UserDefaults.bambooStandard.string(forKey: "tenantSlug") ?? "default"
    @Published public var preAuthKey: String = ""
    @Published public var hostname: String = currentHostname()

    private let log = Logger(subsystem: "dev.hanfour.bamboo.app", category: "viewmodel")
    private let keychain: KeychainStore
    private let tunnel = TunnelManager()
    private let heartbeat = HeartbeatLoop()
    private let watcher = PeerWatcher()
    private var statusTask: Task<Void, Never>?

    // Latest known tunnel config; held so we can rebuild + reapply
    // when peer endpoints change without re-running the full register
    // round-trip.
    private var lastConfig: BambooTunnelConfig?
    private var peerCache: [String: BambooClient.PeerJSON] = [:]
    private var selfPeerID: String?
    private var selfIPv4: String?

    public init(keychain: KeychainStore = KeychainStore()) {
        self.keychain = keychain
        statusTask = Task { @MainActor [weak self] in
            guard let self = self else { return }
            await self.tunnel.refresh()
            for await _ in self.tunnel.$status.values {
                self.applyTunnelStatus()
            }
        }
    }

    deinit {
        statusTask?.cancel()
        Task { [heartbeat, watcher] in
            await heartbeat.stop()
            await watcher.stop()
        }
    }

    public var statusIcon: String {
        switch status {
        case .disconnected: return "circle"
        case .connecting:   return "circle.dashed"
        case .connected:    return "circle.fill"
        case .failing:      return "exclamationmark.triangle"
        }
    }

    public func connect() {
        Task { await self.connectAsync() }
    }

    public func disconnect() {
        Task { [heartbeat, watcher] in
            await heartbeat.stop()
            await watcher.stop()
        }
        tunnel.stopTunnel()
        status = .disconnected
    }

    private func connectAsync() async {
        status = .connecting
        lastError = nil

        UserDefaults.bambooStandard.set(controllerURL, forKey: "controllerURL")
        UserDefaults.bambooStandard.set(tenantSlug, forKey: "tenantSlug")

        do {
            let privateKey = try loadOrCreatePrivateKey()
            let publicKey = privateKey.publicKey.base64Key

            guard let url = URL(string: controllerURL) else {
                throw ConnectionError.invalidControllerURL
            }

            // Best-effort STUN discovery so other peers in the tenant
            // learn how to reach us. Failure is logged but does not
            // block the tunnel — we still get a working self-tunnel
            // and other peers can dial us once their handshake retries.
            var discoveredEndpoints: [String] = []
            do {
                let endpoint = try await STUNClient.discover()
                discoveredEndpoints = [endpoint]
                log.log("stun discovered \(endpoint, privacy: .public)")
            } catch {
                log.warning("stun discovery failed: \(String(describing: error), privacy: .public)")
            }

            let client = BambooClient(
                baseURL: url,
                bearerToken: keychain.getString(for: BambooKeychainKey.sessionToken),
                tenantSlug: tenantSlug
            )
            let resp = try await client.register(.init(
                hostname: hostname,
                wireguardPublicKey: publicKey,
                os: currentOSName(),
                clientVersion: "0.0.1",
                preAuthKeySecret: preAuthKey.isEmpty ? nil : preAuthKey,
                tenantSlug: tenantSlug,
                endpoints: discoveredEndpoints.isEmpty ? nil : discoveredEndpoints
            ))

            let peers = resp.peers.map { p in
                BambooPeerConfig(
                    id: p.id,
                    publicKey: p.wireguardPublicKey,
                    allowedIPs: ["\(p.ip)/32"],
                    endpoint: p.endpoints?.first,
                    persistentKeepalive: 25
                )
            }
            let config = BambooTunnelConfig(
                privateKey: privateKey.base64Key,
                address: "\(resp.self_.ip)/32",
                dnsServers: [],
                mtu: 1280,
                peers: peers
            )

            try await tunnel.startTunnel(with: config)
            log.log("connect: register ok ip=\(resp.self_.ip, privacy: .public) peers=\(peers.count)")

            // Cache state so the watch stream can rebuild the config
            // in-place when peer endpoints change.
            self.lastConfig = config
            self.peerCache = Dictionary(uniqueKeysWithValues: resp.peers.map { ($0.id, $0) })
            self.selfPeerID = resp.self_.id
            self.selfIPv4 = resp.self_.ip

            // Background loops: heartbeat keeps last_seen fresh +
            // re-reports our endpoint; watcher streams peer changes
            // and triggers a tunnel rebuild on endpoint updates.
            await heartbeat.start(client: client, peerID: resp.self_.id)
            let bearer = keychain.getString(for: BambooKeychainKey.sessionToken)
            let slug = self.tenantSlug
            await watcher.start(baseURL: url, peerID: resp.self_.id,
                                bearerToken: bearer, tenantSlug: slug) { [weak self] event in
                Task { @MainActor [weak self] in
                    self?.handleWatchEvent(event)
                }
            }
        } catch {
            log.error("connect: \(String(describing: error), privacy: .public)")
            self.lastError = String(describing: error)
            self.status = .failing
        }
    }

    private func handleWatchEvent(_ event: PeerWatcher.Event) {
        switch event {
        case .peerAdded(let p):
            peerCache[p.id] = p
            log.log("watch: peer_added \(p.hostname, privacy: .public)")
            rebuildAndReapply()
        case .peerUpdated(let p):
            peerCache[p.id] = p
            log.log("watch: peer_updated \(p.hostname, privacy: .public)")
            rebuildAndReapply()
        case .peerRemoved(let id):
            peerCache.removeValue(forKey: id)
            log.log("watch: peer_removed \(id, privacy: .public)")
            rebuildAndReapply()
        case .policyChanged(let rev):
            log.log("watch: policy_changed rev=\(rev, privacy: .public)")
        }
    }

    /// rebuildAndReapply rebuilds the tunnel config from the cached
    /// peer set and pushes it to the system VPN. WireGuard sees a
    /// brief restart (~100ms); future PR can replace this with
    /// handleAppMessage IPC so the extension reconfigures without
    /// dropping the tunnel.
    private func rebuildAndReapply() {
        guard
            let selfID = selfPeerID,
            let selfIPv4 = selfIPv4,
            let prevConfig = lastConfig
        else { return }

        let peers = peerCache.values
            .filter { $0.id != selfID }
            .map { p in
                BambooPeerConfig(
                    id: p.id,
                    publicKey: p.wireguardPublicKey,
                    allowedIPs: ["\(p.ip)/32"],
                    endpoint: p.endpoints?.first,
                    persistentKeepalive: 25
                )
            }
        let config = BambooTunnelConfig(
            privateKey: prevConfig.privateKey,
            address: "\(selfIPv4)/32",
            dnsServers: prevConfig.dnsServers,
            mtu: prevConfig.mtu,
            peers: peers
        )
        self.lastConfig = config

        Task {
            // Prefer IPC: the running extension's WireGuardAdapter
            // can apply the new config without dropping the tunnel.
            // Fall back to the heavy saveToPreferences + startVPNTunnel
            // path only when IPC fails (typically: extension hasn't
            // started yet, or the IPC channel is wedged).
            if await self.tunnel.applyConfig(config) {
                self.log.log("rebuild applied via IPC (no restart)")
                return
            }
            self.log.log("IPC apply failed; falling back to start path")
            do {
                try await self.tunnel.startTunnel(with: config)
            } catch {
                self.log.warning("rebuild apply: \(String(describing: error), privacy: .public)")
            }
        }
    }

    private func applyTunnelStatus() {
        switch tunnel.status {
        case .unknown, .disconnected:
            self.status = .disconnected
        case .connecting:
            self.status = .connecting
        case .connected:
            self.status = .connected
        case .disconnecting:
            self.status = .disconnected
        case .failing(let reason):
            self.lastError = reason
            self.status = .failing
        }
    }

    private func loadOrCreatePrivateKey() throws -> PrivateKey {
        if let saved = keychain.getString(for: BambooKeychainKey.wireguardPrivateKey),
           let key = PrivateKey(base64Key: saved) {
            return key
        }
        let fresh = PrivateKey()
        try keychain.setString(fresh.base64Key, for: BambooKeychainKey.wireguardPrivateKey)
        return fresh
    }
}

private enum ConnectionError: Error, CustomStringConvertible {
    case invalidControllerURL

    var description: String {
        switch self {
        case .invalidControllerURL: return "controller URL is not a valid URL"
        }
    }
}

// MARK: - platform helpers

private func currentHostname() -> String {
    #if canImport(UIKit)
    return UIDevice.current.name
    #elseif canImport(AppKit)
    return Host.current().localizedName ?? "mac"
    #else
    return "bamboo"
    #endif
}

private func currentOSName() -> String {
    #if os(iOS)
    return "ios"
    #elseif os(macOS)
    return "darwin"
    #else
    return "unknown"
    #endif
}

extension UserDefaults {
    /// `bambooStandard` is a hook so that a future App Group can swap
    /// in a shared suite. Using `.standard` for now keeps the diff
    /// small and works fine until extension-side reads land.
    static let bambooStandard: UserDefaults = .standard
}
