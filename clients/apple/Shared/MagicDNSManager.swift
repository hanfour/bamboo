// SPDX-License-Identifier: Apache-2.0

import Foundation
import NetworkExtension
import OSLog

/// MagicDNSManager owns the host-side `NEDNSProxyManager` lifecycle
/// for MagicDNS. The host app calls `enable()` at startup so that
/// `*.bamboo` resolution works whether or not the VPN tunnel is
/// currently up. The peer map is delivered out-of-band via
/// `MagicDNSPeerStore` (an App Group UserDefaults suite shared with
/// the extension).
///
/// Platform split:
///   - iOS: NEDNSProxyProvider is hosted as an App Extension. enable()
///     wires NEDNSProxyManager → bundle id of the extension target.
///   - macOS: App Extension form of NEDNSProxyProvider is rejected
///     by macOS 15+ (lsregister error -10811; pluginkit ignores the
///     bundle). The modern path is a System Extension via
///     OSSystemExtensionRequest, which Stage 3 will deliver. Until
///     then, enable() on macOS calls disable() instead — clearing
///     any stale config a previous build may have written so System
///     Settings → Network doesn't show a perpetually-failing DNS
///     proxy session.
///
/// Idempotent on both sides: enable() during an already-enabled
/// state re-saves config; disable() during disabled is a no-op.
@MainActor
public final class MagicDNSManager {

    private let log = Logger(subsystem: "dev.hanfour.bamboo.app",
                             category: "MagicDNSManager")

    /// The DNSProxy extension's bundle identifier. iOS only — the
    /// macOS extension target was removed (see project.yml comment
    /// + Stage 3 plan).
    private static let providerBundle = "dev.hanfour.bamboo.app.dnsproxy"

    public init() {}

    /// Configure the system's DNS proxy hook. Implementation depends
    /// on platform — see the class comment.
    public func enable() async throws {
        #if os(iOS)
        try await enableIOS()
        #else
        try await disable()
        log.log("MagicDNS macOS path deferred to Stage 3 (System Extension); cleared any stale App Extension config")
        #endif
    }

    /// Disable the DNS proxy. Leaves the configuration entry in
    /// System Settings; re-enabling is one save. The user can also
    /// remove the entry there manually for a complete clean-up.
    public func disable() async throws {
        let manager = NEDNSProxyManager.shared()
        try await loadIfNeeded(manager)
        guard manager.isEnabled else { return }
        manager.isEnabled = false
        try await save(manager)
        log.log("MagicDNS disabled")
    }

    // MARK: - private

    #if os(iOS)
    private func enableIOS() async throws {
        let manager = NEDNSProxyManager.shared()
        try await loadIfNeeded(manager)

        let proto = NEDNSProxyProviderProtocol()
        proto.providerBundleIdentifier = Self.providerBundle
        // Empty server address would be cleaner but the base class
        // requires non-nil; the OS does not connect to it for a
        // dns-proxy provider.
        proto.serverAddress = "MagicDNS"

        manager.providerProtocol = proto
        manager.localizedDescription = "bamboo MagicDNS"
        manager.isEnabled = true

        try await save(manager)
        log.log("MagicDNS enabled (provider=\(Self.providerBundle, privacy: .public))")
    }
    #endif

    private func loadIfNeeded(_ manager: NEDNSProxyManager) async throws {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            manager.loadFromPreferences { error in
                if let error = error {
                    cont.resume(throwing: error)
                } else {
                    cont.resume(returning: ())
                }
            }
        }
    }

    private func save(_ manager: NEDNSProxyManager) async throws {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            manager.saveToPreferences { error in
                if let error = error {
                    cont.resume(throwing: error)
                } else {
                    cont.resume(returning: ())
                }
            }
        }
    }
}
