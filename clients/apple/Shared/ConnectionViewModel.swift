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
    @Published public var relayURL: String =
        UserDefaults.bambooStandard.string(forKey: "relayURL") ?? ""
    /// Email of the OIDC-authenticated user, or nil if the device is
    /// driving the controller via pre-auth key only. Persisted to
    /// UserDefaults so the UI's "Signed in as ..." banner survives an
    /// app restart even though the JWT itself lives in the Keychain.
    @Published public var signedInEmail: String? =
        UserDefaults.bambooStandard.string(forKey: "signedInEmail")
    /// Drives the Settings sheet's "Sign in with Google" button into a
    /// spinner state while ASWebAuthenticationSession is presented.
    @Published public var isSigningIn: Bool = false

    private let log = Logger(subsystem: "dev.hanfour.bamboo.app", category: "viewmodel")
    private let keychain: KeychainStore
    private let authClient = AuthClient()
    private let tunnel = TunnelManager()
    private let heartbeat = HeartbeatLoop()
    private let watcher = PeerWatcher()
    private var relay: RelayClient?
    private var statusTask: Task<Void, Never>?

    // Latest known tunnel config; held so we can rebuild + reapply
    // when peer endpoints change without re-running the full register
    // round-trip.
    private var lastConfig: BambooTunnelConfig?
    private var peerCache: [String: BambooClient.PeerJSON] = [:]
    private var selfPeerID: String?
    private var selfIPv4: String?
    // peer.id -> 127.0.0.1:<port> of the local RelayClient proxy for
    // that peer. Populated at connect() time when relay is enabled,
    // then consulted on every watch-driven rebuild so a peer_updated
    // event doesn't accidentally swap the relay endpoint for a (often
    // unreachable) STUN one.
    private var peerRelayEndpoints: [String: String] = [:]

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
        let r = relay
        Task { [heartbeat, watcher] in
            await heartbeat.stop()
            await watcher.stop()
            await r?.close()
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

    /// Kick off the OIDC sign-in flow. Resolves into:
    ///   - on success: session JWT in Keychain (key sessionToken) and
    ///     signedInEmail populated for the UI banner.
    ///   - on cancel: silent return — the user closed the browser, no
    ///     error banner shown.
    ///   - on error: lastError populated.
    public func signIn() {
        Task { await self.signInAsync() }
    }

    private func signInAsync() async {
        isSigningIn = true
        defer { isSigningIn = false }
        lastError = nil

        UserDefaults.bambooStandard.set(controllerURL, forKey: "controllerURL")
        UserDefaults.bambooStandard.set(tenantSlug, forKey: "tenantSlug")

        guard let url = URL(string: controllerURL) else {
            self.lastError = "controller URL is not a valid URL"
            return
        }
        do {
            let result = try await authClient.signIn(controllerURL: url, tenantSlug: tenantSlug)
            try keychain.setString(result.token, for: BambooKeychainKey.sessionToken)
            if !result.tenant.isEmpty {
                // Controller may have overridden the slug (e.g.
                // invite flow); reflect that in the UI + UserDefaults
                // so the next Connect uses the right tenant.
                self.tenantSlug = result.tenant
                UserDefaults.bambooStandard.set(result.tenant, forKey: "tenantSlug")
            }
            if !result.email.isEmpty {
                self.signedInEmail = result.email
                UserDefaults.bambooStandard.set(result.email, forKey: "signedInEmail")
            }
            log.log("sign-in success email=\(result.email, privacy: .public)")
        } catch {
            if let aerr = error as? AuthError, case .userCancelled = aerr {
                // Cancellation is silent — user dismissed Safari, no
                // recovery needed.
                return
            }
            log.error("sign-in: \(String(describing: error), privacy: .public)")
            self.lastError = String(describing: error)
        }
    }

    /// Clear the OIDC session: drop the Keychain bearer + forget the
    /// signed-in email. The WireGuard private key is intentionally
    /// untouched so a subsequent re-sign-in keeps the same device
    /// identity.
    public func signOut() {
        keychain.remove(for: BambooKeychainKey.sessionToken)
        UserDefaults.bambooStandard.removeObject(forKey: "signedInEmail")
        self.signedInEmail = nil
        log.log("sign-out: cleared session token")
    }

    public func disconnect() {
        let r = relay
        relay = nil
        Task { [heartbeat, watcher] in
            await heartbeat.stop()
            await watcher.stop()
            await r?.close()
        }
        tunnel.stopTunnel()
        status = .disconnected
    }

    /// ensureRelay mints a relay-token, opens a RelayClient, waits
    /// for SERVER_HELLO. Caller registers peers via addPeer. wgPort
    /// must match the port WireGuard binds locally so the relay
    /// proxy delivers decrypted payloads to the right kernel socket.
    private func ensureRelay(client: BambooClient,
                             selfId: String,
                             selfPubKey: String,
                             url: URL,
                             wgPort: UInt16) async throws -> RelayClient {
        let resp = try await client.relayToken(.init(peerId: selfId,
                                                      wireguardPublicKey: selfPubKey))
        guard let selfKey = Data(base64Encoded: selfPubKey) else {
            throw BambooClientError.invalidResponse
        }
        let r = RelayClient(selfKey: selfKey, token: resp.token,
                            wgListenHost: "127.0.0.1", wgListenPort: wgPort)
        try await r.dial(relayURL: url)
        return r
    }

    /// pickFreeUDPPort opens an ephemeral UDP socket so the OS picks
    /// a free port, then closes it and returns the assigned number.
    /// The close/reopen window is a race; in practice nothing else on
    /// the host is racing for UDP ports while the connect flow runs,
    /// and WireGuard's bind in the extension surfaces a clear error
    /// if the port is taken — louder than silently picking a
    /// different port.
    private func pickFreeUDPPort() throws -> UInt16 {
        let sock = socket(AF_INET, SOCK_DGRAM, 0)
        guard sock >= 0 else {
            throw ConnectionError.portPickFailed("socket")
        }
        defer { close(sock) }
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_addr.s_addr = INADDR_ANY.bigEndian
        addr.sin_port = 0
        let bindRes = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.bind(sock, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        guard bindRes == 0 else {
            throw ConnectionError.portPickFailed("bind errno=\(errno)")
        }
        var bound = sockaddr_in()
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        let nameRes = withUnsafeMutablePointer(to: &bound) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                getsockname(sock, $0, &len)
            }
        }
        guard nameRes == 0 else {
            throw ConnectionError.portPickFailed("getsockname errno=\(errno)")
        }
        return UInt16(bigEndian: bound.sin_port)
    }

    private func connectAsync() async {
        status = .connecting
        lastError = nil

        UserDefaults.bambooStandard.set(controllerURL, forKey: "controllerURL")
        UserDefaults.bambooStandard.set(tenantSlug, forKey: "tenantSlug")
        UserDefaults.bambooStandard.set(relayURL, forKey: "relayURL")

        do {
            let privateKey = try loadOrCreatePrivateKey()
            let publicKey = privateKey.publicKey.base64Key

            guard let url = URL(string: controllerURL) else {
                throw ConnectionError.invalidControllerURL
            }

            // Pick the WireGuard listen port BEFORE STUN so STUN and
            // WG share the same internal source port. NATs allocate
            // external mappings keyed on (src_ip, src_port); using
            // different ports for STUN vs WG yields an advertised
            // endpoint other peers can't actually dial. See Finding
            // #4 in docs/development/project-understanding-2026-05-13.md.
            let wgPort = try pickFreeUDPPort()
            log.log("wireguard listen port chosen \(wgPort, privacy: .public)")

            // Best-effort STUN discovery so other peers in the tenant
            // learn how to reach us. Failure is logged but does not
            // block the tunnel — we still get a working self-tunnel
            // and other peers can dial us once their handshake retries.
            var discoveredEndpoints: [String] = []
            do {
                let endpoint = try await STUNClient.discover(localPort: wgPort)
                discoveredEndpoints = [endpoint]
                log.log("stun discovered \(endpoint, privacy: .public)")
            } catch {
                log.warning("stun discovery failed: \(String(describing: error), privacy: .public)")
            }

            let bootstrapClient = BambooClient(
                baseURL: url,
                bearerToken: keychain.getString(for: BambooKeychainKey.sessionToken),
                tenantSlug: tenantSlug
            )
            let resp = try await bootstrapClient.register(.init(
                hostname: hostname,
                wireguardPublicKey: publicKey,
                os: currentOSName(),
                clientVersion: "0.0.1",
                preAuthKeySecret: preAuthKey.isEmpty ? nil : preAuthKey,
                tenantSlug: tenantSlug,
                endpoints: discoveredEndpoints.isEmpty ? nil : discoveredEndpoints
            ))

            // Once Register returns, prefer the peer-session bearer
            // for every subsequent peer-bound call (heartbeat / watch
            // / relay-token). Older controllers that predate the auth
            // train omit the token; in that case `client` is just
            // `bootstrapClient` and the dev slug fallback continues to
            // work.
            let client = bootstrapClient.withPeerSessionToken(resp.peerSessionToken)
            if let tok = resp.peerSessionToken, !tok.isEmpty {
                log.log("peer session token issued (expires=\(resp.peerSessionExpiresAt ?? 0, privacy: .public))")
            }

            // Optional: when relayURL is set, route every peer
            // through the relay server. Useful on networks where
            // direct STUN endpoints fail (symmetric NAT, restrictive
            // egress firewalls).
            var peerEndpoints: [String: String] = [:] // peer.id -> endpoint
            if !relayURL.isEmpty, let url = URL(string: relayURL) {
                do {
                    let r = try await ensureRelay(client: client,
                                                   selfId: resp.self_.id,
                                                   selfPubKey: publicKey,
                                                   url: url,
                                                   wgPort: wgPort)
                    for p in resp.peers {
                        if let pkData = Data(base64Encoded: p.wireguardPublicKey) {
                            let proxyAddr = try await r.addPeer(pkData)
                            peerEndpoints[p.id] = proxyAddr
                        }
                    }
                    self.relay = r
                    log.log("relay enabled url=\(url.absoluteString, privacy: .public) peers=\(resp.peers.count, privacy: .public)")
                } catch {
                    log.warning("relay init failed; falling back to direct: \(String(describing: error), privacy: .public)")
                }
            }

            let peers = resp.peers.map { p in
                BambooPeerConfig(
                    id: p.id,
                    publicKey: p.wireguardPublicKey,
                    allowedIPs: ["\(p.ip)/32"],
                    endpoint: peerEndpoints[p.id] ?? p.endpoints?.first,
                    persistentKeepalive: 25
                )
            }
            let config = BambooTunnelConfig(
                privateKey: privateKey.base64Key,
                address: "\(resp.self_.ip)/32",
                dnsServers: [],
                mtu: 1280,
                wgListenPort: wgPort,
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
            // Stash relay-proxy endpoints so watch-driven rebuilds
            // don't swap them for STUN. Empty when relay is disabled
            // — rebuildAndReapply then uses the STUN path as before.
            self.peerRelayEndpoints = peerEndpoints

            // Background loops: heartbeat keeps last_seen fresh +
            // re-reports our endpoint; watcher streams peer changes
            // and triggers a tunnel rebuild on endpoint updates.
            await heartbeat.start(client: client, peerID: resp.self_.id)
            // Watch is peer-bound: prefer the peer-session token the
            // controller just minted. Fall back to the user-session
            // bearer for older controllers (and for admin-driven dev
            // sessions); the controller resolves whichever matches.
            let bearer = resp.peerSessionToken
                ?? keychain.getString(for: BambooKeychainKey.sessionToken)
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
                // Prefer the relay-proxy endpoint (127.0.0.1:<port>)
                // captured at connect time. Without this, a watch-
                // driven rebuild silently swaps the working relay
                // path for whatever STUN candidate the peer reported
                // and the mesh stops carrying packets — same-NAT
                // peers in particular have no hairpin fallback.
                BambooPeerConfig(
                    id: p.id,
                    publicKey: p.wireguardPublicKey,
                    allowedIPs: ["\(p.ip)/32"],
                    endpoint: peerRelayEndpoints[p.id] ?? p.endpoints?.first,
                    persistentKeepalive: 25
                )
            }
        let config = BambooTunnelConfig(
            privateKey: prevConfig.privateKey,
            address: "\(selfIPv4)/32",
            dnsServers: prevConfig.dnsServers,
            mtu: prevConfig.mtu,
            wgListenPort: prevConfig.wgListenPort,
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
    case portPickFailed(String)

    var description: String {
        switch self {
        case .invalidControllerURL: return "controller URL is not a valid URL"
        case .portPickFailed(let reason): return "could not pick a free UDP port: \(reason)"
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
