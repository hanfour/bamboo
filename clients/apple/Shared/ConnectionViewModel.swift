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
    private let magicDNS = MagicDNSManager()
    private let peerStore = MagicDNSPeerStore()
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
    // ACL enforcement state (issue #132). policyRevision > 0 ⇒ the
    // controller is enforcing an authored ACL; allowedIPs in
    // cachedAllowedIPs are authoritative, peers absent from the map
    // are denied. policyRevision == 0 ⇒ no policy is authored and
    // every peer falls back to its tunnel /32, matching the pre-
    // enforcement default. Watch-event-derived peer rebuilds READ
    // from cachedAllowedIPs (Register-only-populated) so a
    // PeerUpdated for an endpoint change does not silently drop a
    // peer because its event payload lacks AllowedIps.
    private var policyRevision: Int64 = 0
    private var cachedAllowedIPs: [String: [String]] = [:]
    // Peer-session bearer minted by Register, held in memory so the
    // policy-refresh flow (issue #132) can authenticate without
    // re-running the bootstrap-credential dance. Nil when the
    // controller predates the peer-session-token train; the refresh
    // falls back to the user-session JWT in the keychain.
    private var peerSessionBearer: String?

    public init(keychain: KeychainStore = KeychainStore()) {
        self.keychain = keychain
        statusTask = Task { @MainActor [weak self] in
            guard let self = self else { return }
            await self.tunnel.refresh()
            // Cold-start handoff: if the tunnel System Extension
            // survived the host app exit, the kernel tunnel is still
            // carrying packets but our in-process state (peerCache,
            // peerSessionBearer, heartbeat / watch / relay tasks) is
            // empty. The user then sees a "Connected" UI but the
            // device never reports a heartbeat, never receives
            // peer_updated events, and never refreshes ACL policy —
            // mesh debug 2026-05-22.
            //
            // Rerun connectAsync() to register again, restart the
            // background loops, and reapply the tunnel config.
            // connectAsync is idempotent: register is a read for an
            // existing peer (FindByPubKey), the keychain still has
            // the WG private key, and tunnel.startTunnel for an
            // already-running SystemExt is a save-and-start that the
            // VPN subsystem applies in place. The brief UI flicker
            // (Connected → Connecting → Connected) is preferable to
            // sitting in a stale half-alive state forever.
            //
            // Race note: while connectAsync runs, tunnel.$status
            // transitions are NOT observed (the for-await below
            // hasn't subscribed yet). connectAsync sets
            // self.status itself (.connecting at entry, .failing on
            // throw via the catch), so the UI sees the high-level
            // result; intermediate tunnel-level flaps are
            // intentionally dropped here to avoid duplicating work
            // applyTunnelStatus does once we settle.
            if case .connected = self.tunnel.status {
                self.log.log("cold-start: tunnel already up; rerunning connect cycle to restart heartbeat/watch/relay")
                await self.connectAsync()
            }
            for await _ in self.tunnel.$status.values {
                self.applyTunnelStatus()
            }
        }
        // Ask the OS to install the MagicDNS proxy at launch. The
        // first install fires a system permission dialog the user
        // has to accept once; subsequent launches are no-ops. We
        // run this on a background Task so the launch sequence
        // isn't blocked on system preferences I/O.
        Task { @MainActor [weak self] in
            guard let self = self else { return }
            do {
                try await self.magicDNS.enable()
            } catch {
                self.log.warning("MagicDNS enable failed: \(String(describing: error), privacy: .public)")
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
        // Mesh-debug 2026-05-22: h4 reportedly never runs the full
        // register/relay/heartbeat after Connect press. Log
        // unconditionally so we can tell whether the button handler
        // fires at all and what tunnel.status looked like at the
        // moment. Cheap; safe to leave in.
        log.log("Connect button pressed; viewModel.status=\(self.status.rawValue, privacy: .public) tunnel.status=\(String(describing: self.tunnel.status), privacy: .public)")
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

        persistSettingIfNonEmpty(controllerURL, forKey: "controllerURL")
        persistSettingIfNonEmpty(tenantSlug, forKey: "tenantSlug")

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
        // Symmetric breadcrumb for the disconnect path so a button
        // mis-binding (UI shows "Disconnect" because status was
        // misread as .connected at cold-start) is obvious in the log.
        log.log("Disconnect button pressed; viewModel.status=\(self.status.rawValue, privacy: .public) tunnel.status=\(String(describing: self.tunnel.status), privacy: .public)")
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

    /// Persist a user-typed setting back to UserDefaults so the next
    /// launch's @Published init picks it up. Empty @Published values
    /// (typically because the un-sandboxed prefs store after PR #153
    /// started empty and the user never re-typed this field) are
    /// NOT written, so they don't clobber whatever a future migration
    /// or other code path might restore. A user who wants to *clear*
    /// a stored value should use an explicit Settings action that
    /// calls removeObject(forKey:) directly — silently erasing on
    /// every connect was the destructive-overwrite bug this fixes.
    /// As a side effect, trims surrounding whitespace; macOS 26
    /// URL(string:) rejects trailing newline / space, and pasted
    /// settings often carry one.
    private func persistSettingIfNonEmpty(_ value: String, forKey key: String) {
        let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        UserDefaults.bambooStandard.set(trimmed, forKey: key)
    }

    private func connectAsync() async {
        status = .connecting
        lastError = nil

        persistSettingIfNonEmpty(controllerURL, forKey: "controllerURL")
        persistSettingIfNonEmpty(tenantSlug, forKey: "tenantSlug")
        persistSettingIfNonEmpty(relayURL, forKey: "relayURL")

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
            //
            // Each non-relay-enabled exit path logs explicitly: the
            // earlier silent skip masked a destructive UserDefaults
            // overwrite (the @Published relayURL was unexpectedly
            // empty post un-sandbox migration, and the relay block
            // skipped without saying so), so this fix splits the
            // three failure shapes into named log lines.
            var peerEndpoints: [String: String] = [:] // peer.id -> endpoint
            let trimmedRelayURL = relayURL.trimmingCharacters(in: .whitespacesAndNewlines)
            if trimmedRelayURL.isEmpty {
                log.log("relay: skipped (relayURL is empty; type one in Settings to enable)")
            } else if let url = URL(string: trimmedRelayURL),
                      let scheme = url.scheme?.lowercased(),
                      scheme == "ws" || scheme == "wss" {
                // URL(string:) on macOS 26 rejects strings with
                // trailing whitespace or newlines (verified via
                // `swift -e`), so we trim first. The scheme guard
                // catches strings like "abc" — URL(string:) returns
                // a non-nil URL with scheme==nil that would otherwise
                // pass through to RelayClient.dial and hang there.
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
            } else {
                log.warning("relay: not a valid ws/wss URL — relayURL=\(trimmedRelayURL, privacy: .public); fix in Settings")
            }

            // ACL enforcement (issue #132): when the controller is
            // enforcing (PolicyRevision > 0), a peer with an empty
            // allowedIps list is policy-denied and must be excluded
            // from the WG config entirely. We compactMap so denied
            // peers fall out before BambooPeerConfig is constructed,
            // mirroring clients/core/wg/config.go's enforcing branch.
            let enforcing = resp.policyRevision > 0
            let peers = resp.peers.compactMap { p -> BambooPeerConfig? in
                guard let allowed = aclAllowedIPs(for: p, enforcing: enforcing) else {
                    return nil
                }
                return BambooPeerConfig(
                    id: p.id,
                    publicKey: p.wireguardPublicKey,
                    allowedIPs: allowed,
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
            // in-place when peer endpoints change. Self is stored
            // in peerCache too so the MagicDNS map covers
            // `<self-hostname>.bamboo` (useful for sanity-pinging
            // your own machine through the proxy).
            self.lastConfig = config
            var seed: [String: BambooClient.PeerJSON] = [resp.self_.id: resp.self_]
            for p in resp.peers { seed[p.id] = p }
            self.peerCache = seed
            self.selfPeerID = resp.self_.id
            self.selfIPv4 = resp.self_.ip
            // Cache the controller-computed AllowedIps separately
            // from peerCache so a later WatchPeers event (which
            // carries no AllowedIps) can't accidentally reset a peer
            // to the deny-by-default empty list during a rebuild.
            self.policyRevision = resp.policyRevision
            self.cachedAllowedIPs = Self.allowedIPMap(from: resp.peers)
            self.peerSessionBearer = resp.peerSessionToken
            // Stash relay-proxy endpoints so watch-driven rebuilds
            // don't swap them for STUN. Empty when relay is disabled
            // — rebuildAndReapply then uses the STUN path as before.
            self.peerRelayEndpoints = peerEndpoints
            // Push the current peer map across the App Group so the
            // DNSProxy extension can answer *.bamboo queries.
            self.syncMagicDNSMap()

            // Background loops: heartbeat keeps last_seen fresh +
            // re-reports our endpoint; watcher streams peer changes
            // and triggers a tunnel rebuild on endpoint updates.
            await heartbeat.start(
                client: client,
                peerID: resp.self_.id,
                // The heartbeat closure runs off the main actor; we
                // can't read the @MainActor-isolated stored property
                // directly. Capture a sendable atomic-ish view by
                // wrapping it in a function that hops back to main.
                knownRevision: { [weak self] in
                    // Hop to MainActor to read the @MainActor-isolated
                    // stored property safely. The heartbeat task runs
                    // off the main actor; assumeIsolated would crash.
                    await MainActor.run { self?.policyRevision ?? 0 }
                },
                onPolicyChanged: { [weak self] _ in
                    // Heartbeat detected a policy bump (issue #132).
                    // Watch handles this too; running it from both
                    // paths is intentional — the watch stream can
                    // briefly disconnect on a relay flap, and we
                    // don't want a policy delta to wait the full
                    // reconnect backoff.
                    Task { @MainActor [weak self] in
                        await self?.refreshPolicyFromController()
                    }
                }
            )
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
        // Under ACL enforcement (policyRevision > 0), every peer
        // membership delta also needs fresh per-recipient AllowedIps
        // from the controller — the WatchPeers event payload itself
        // carries no AllowedIps (the bus is tenant-wide, doesn't
        // know the recipient). Without this re-register, an admin
        // who approves a new peer in the queue wouldn't appear in
        // OUR WG config until we manually reconnect. Issue #132.
        let needsPolicyRefresh = policyRevision > 0
        switch event {
        case .peerAdded(let p):
            peerCache[p.id] = p
            log.log("watch: peer_added \(p.hostname, privacy: .public)")
            if needsPolicyRefresh {
                Task { @MainActor [weak self] in
                    await self?.refreshPolicyFromController()
                }
            } else {
                rebuildAndReapply()
            }
            syncMagicDNSMap()
        case .peerUpdated(let p):
            peerCache[p.id] = p
            log.log("watch: peer_updated \(p.hostname, privacy: .public)")
            // Endpoint-only updates (the common PeerUpdated case)
            // don't need a full policy refresh — the cached
            // AllowedIPs for this peer remain valid. rebuildAndReapply
            // re-reads cachedAllowedIPs so the endpoint swap lands
            // without dropping the route.
            rebuildAndReapply()
            syncMagicDNSMap()
        case .peerRemoved(let id):
            peerCache.removeValue(forKey: id)
            cachedAllowedIPs.removeValue(forKey: id)
            log.log("watch: peer_removed \(id, privacy: .public)")
            rebuildAndReapply()
            syncMagicDNSMap()
        case .policyChanged(let rev):
            log.log("watch: policy_changed rev=\(rev, privacy: .public)")
            // ACL enforcement refresh (issue #132). The watch event
            // signals the controller has a new policy revision; the
            // cached per-peer AllowedIps is now stale. Re-register
            // to pull fresh values, then rebuild the WG config.
            // Errors are logged + tolerated — the tunnel stays up on
            // the prior config so traffic isn't interrupted by a
            // transient controller blip.
            Task { @MainActor [weak self] in
                await self?.refreshPolicyFromController()
            }
        }
    }

    /// aclAllowedIPs returns the AllowedIPs slice to use for a peer
    /// when assembling the WireGuard config. Nil signals "skip this
    /// peer entirely" — the controller's policy engine has denied us
    /// traffic to them, and listing the peer with no routes would
    /// still expose its public key for no benefit.
    ///
    /// Decision tree:
    ///   - enforcing == false (policyRevision == 0)           → ["<ip>/32"]
    ///     Pre-enforcement default; preserves the legacy full-mesh
    ///     behavior for tenants without an authored policy.
    ///   - enforcing && cachedAllowedIPs[p.id] is non-empty   → use cache
    ///   - enforcing && cached entry is empty / missing       → nil (deny)
    private func aclAllowedIPs(for p: BambooClient.PeerJSON, enforcing: Bool) -> [String]? {
        if !enforcing {
            return ["\(p.ip)/32"]
        }
        guard let cached = cachedAllowedIPs[p.id], !cached.isEmpty else {
            return nil
        }
        return cached
    }

    /// allowedIPMap reduces a Register-response peer array into the
    /// (id → AllowedIPs) form rebuildAndReapply reads. Peers with
    /// allowedIps == nil (legacy controller or watch-event payload
    /// that omits the field) are absent from the map; the lookup
    /// site then falls back to deny under enforcement, or to the
    /// /32 default under non-enforcement.
    private static func allowedIPMap(from peers: [BambooClient.PeerJSON]) -> [String: [String]] {
        var out: [String: [String]] = [:]
        for p in peers {
            if let allowed = p.allowedIps {
                out[p.id] = allowed
            }
        }
        return out
    }

    /// refreshPolicyFromController re-runs the Register flow and uses
    /// the freshly-computed AllowedIps to rebuild the WG config. Same
    /// idempotency guarantee as the connect path — FindByPubKey makes
    /// Register a read for an existing peer + a side-effect-free mint
    /// of a new peer-session token.
    ///
    /// Errors are intentionally swallowed: the tunnel stays on the
    /// prior config so traffic isn't interrupted by a transient
    /// controller blip. A subsequent heartbeat / watch tick will fire
    /// this again when the controller is reachable.
    private func refreshPolicyFromController() async {
        guard let url = URL(string: controllerURL.trimmingCharacters(in: .whitespacesAndNewlines)) else {
            log.warning("policy refresh: invalid controller URL; staying on prior config")
            return
        }
        let privateKey: PrivateKey
        do {
            privateKey = try loadOrCreatePrivateKey()
        } catch {
            log.warning("policy refresh: load private key failed: \(String(describing: error), privacy: .public)")
            return
        }
        // Prefer the peer-session bearer the controller minted at
        // connect time; fall back to the user-session bearer in
        // the keychain. The controller's authenticate() accepts
        // either.
        let bearer = peerSessionBearer
            ?? keychain.getString(for: BambooKeychainKey.sessionToken)
        let client = BambooClient(
            baseURL: url,
            bearerToken: bearer,
            tenantSlug: tenantSlug
        )
        do {
            let resp = try await client.register(.init(
                hostname: hostname,
                wireguardPublicKey: privateKey.publicKey.base64Key,
                os: currentOSName(),
                clientVersion: "0.0.1",
                preAuthKeySecret: nil,
                tenantSlug: tenantSlug,
                endpoints: nil
            ))
            self.policyRevision = resp.policyRevision
            self.cachedAllowedIPs = Self.allowedIPMap(from: resp.peers)
            self.peerSessionBearer = resp.peerSessionToken
            // Replace peer cache so removed peers leave the rebuild;
            // self stays put.
            var seed: [String: BambooClient.PeerJSON] = [resp.self_.id: resp.self_]
            for p in resp.peers { seed[p.id] = p }
            self.peerCache = seed
            log.log("policy refresh: revision=\(resp.policyRevision, privacy: .public) peers=\(resp.peers.count, privacy: .public)")
            rebuildAndReapply()
            syncMagicDNSMap()
        } catch {
            log.warning("policy refresh failed: \(String(describing: error), privacy: .public); staying on prior config")
        }
    }

    /// Build the dns-name → IP map from the current peer cache and
    /// write it to the App Group shared store so the DNSProxy
    /// extension can answer *.bamboo queries. Cheap (a few dozen
    /// peers max + a JSON encode); called on every register and
    /// every watch event.
    private func syncMagicDNSMap() {
        var map: [String: MagicDNSPeerStore.PeerEntry] = [:]
        for (_, p) in peerCache {
            guard let dnsName = p.peerDnsName, !dnsName.isEmpty else {
                continue
            }
            map[dnsName.lowercased()] = MagicDNSPeerStore.PeerEntry(
                ipv4: p.ip,
                ipv6: nil,
                hostname: p.hostname
            )
        }
        peerStore?.setPeers(map)
        log.log("magicdns: synced \(map.count, privacy: .public) peer name(s)")
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

        let enforcing = policyRevision > 0
        let peers = peerCache.values
            .filter { $0.id != selfID }
            .compactMap { p -> BambooPeerConfig? in
                // Prefer the relay-proxy endpoint (127.0.0.1:<port>)
                // captured at connect time. Without this, a watch-
                // driven rebuild silently swaps the working relay
                // path for whatever STUN candidate the peer reported
                // and the mesh stops carrying packets — same-NAT
                // peers in particular have no hairpin fallback.
                //
                // AllowedIPs are read from cachedAllowedIPs rather
                // than p.allowedIps — the watch event payload that
                // populates peerCache after PeerUpdated carries no
                // AllowedIps and we don't want a stale-cache rebuild
                // to silently drop a peer (or expose one the policy
                // newly denies). The cache is refreshed via a
                // re-register on .policyChanged.
                guard let allowed = aclAllowedIPs(for: p, enforcing: enforcing) else {
                    return nil
                }
                return BambooPeerConfig(
                    id: p.id,
                    publicKey: p.wireguardPublicKey,
                    allowedIPs: allowed,
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

        // Heartbeat-driven watch events fire every ~30s and re-trigger
        // rebuildAndReapply even when nothing relevant changed (the
        // peer's last_seen ticked, the peer's STUN endpoint didn't,
        // and we use relay-proxy endpoints anyway). Each WG apply has
        // a real cost: wireguard-go's UAPI sometimes silently drops
        // the new peer set with "device closed" (mesh-debug 2026-05-23,
        // h4 ended up with 0 peers after a watch tick), and even a
        // successful apply burns CPU. Skip the apply when the
        // computed config equals what we already pushed.
        if config == self.lastConfig {
            return
        }
        self.lastConfig = config

        Task {
            // Prefer IPC: the running extension's WireGuardAdapter
            // can apply the new config without dropping the tunnel.
            // Fall back to the heavy saveToPreferences + startVPNTunnel
            // path only when IPC fails (typically: extension hasn't
            // started yet, the IPC channel is wedged, or
            // PacketTunnelProvider's post-update peer-count check
            // detected wireguard-go silently dropped the peer set).
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
