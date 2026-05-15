// SPDX-License-Identifier: Apache-2.0

import Foundation
import NetworkExtension
import OSLog

/// MagicDNSManager owns the host-side `NEDNSProxyManager` lifecycle.
/// Enabling the manager hooks the DNSProxy extension into the OS's
/// DNS resolution path system-wide; disabling pulls it out. The host
/// app calls `enable()` at startup so that `*.bamboo` resolution
/// works whether or not the VPN tunnel is currently up. The peer
/// map is delivered out-of-band via `MagicDNSPeerStore` (an App
/// Group UserDefaults suite shared with the extension).
///
/// Idempotent on both sides: enable() during an already-enabled
/// state re-saves config (refreshes provider identity); disable()
/// during disabled is a no-op.
@MainActor
public final class MagicDNSManager {

    private let log = Logger(subsystem: "dev.hanfour.bamboo.app",
                             category: "MagicDNSManager")

    /// The DNSProxy extension's bundle identifier. Must match
    /// `PRODUCT_BUNDLE_IDENTIFIER` of the DNSProxy target in
    /// project.yml.
    private static let providerBundle = "dev.hanfour.bamboo.app.dnsproxy"

    public init() {}

    /// Enable the DNS proxy globally. On first call this writes a
    /// proxy configuration and asks the OS to load + activate; on
    /// subsequent calls (same proxy already enabled) we re-save so
    /// the system picks up any changed `providerBundleIdentifier`
    /// (effectively a no-op after the first install).
    public func enable() async throws {
        let manager = NEDNSProxyManager.shared()
        try await loadIfNeeded(manager)

        let proto = NEDNSProxyProviderProtocol()
        proto.providerBundleIdentifier = Self.providerBundle
        // Empty server address is fine for a dns-proxy provider —
        // we synthesize / forward inside the extension. The field
        // is required by NEVPNProtocol's base class; the OS does
        // not connect to it.
        proto.serverAddress = "MagicDNS"

        manager.providerProtocol = proto
        manager.localizedDescription = "bamboo MagicDNS"
        manager.isEnabled = true

        try await save(manager)
        log.log("MagicDNS enabled (provider=\(Self.providerBundle, privacy: .public))")
    }

    /// Disable the DNS proxy. Leaves the configuration in System
    /// Settings (Network → DNS Proxy) so re-enabling is one save;
    /// the user can also remove the entry there manually if they
    /// want a complete clean-up.
    public func disable() async throws {
        let manager = NEDNSProxyManager.shared()
        try await loadIfNeeded(manager)
        guard manager.isEnabled else { return }
        manager.isEnabled = false
        try await save(manager)
        log.log("MagicDNS disabled")
    }

    // MARK: - private

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
