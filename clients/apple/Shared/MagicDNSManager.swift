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
/// Platform split (both share the same `dev.hanfour.bamboo.app.dnsproxy`
/// bundle identifier and the same DNSProxyProvider class):
///   - iOS: NEDNSProxyProvider runs as an *App Extension* under
///     Contents/PlugIns/. `enable()` wires `NEDNSProxyManager` →
///     extension bundle id and saves; the OS discovers the .appex
///     and spawns it on first DNS flow.
///   - macOS: NEDNSProxyProvider runs as a *System Extension* under
///     Contents/Library/SystemExtensions/. `enable()` first calls
///     `OSSystemExtensionRequest.activationRequest(...)` via
///     `SystemExtensionInstaller`, which prompts the user
///     (Privacy & Security → Allow → admin password) on first
///     install, then idempotently re-runs on later launches. Once
///     activated, the `NEDNSProxyManager` save is identical to
///     the iOS path.
///
/// Idempotent on both sides: enable() during an already-enabled
/// state re-saves config; disable() during disabled is a no-op.
@MainActor
public final class MagicDNSManager {

    private let log = Logger(subsystem: "dev.hanfour.bamboo.app",
                             category: "MagicDNSManager")

    /// The DNSProxy extension's bundle identifier. Shared between
    /// the iOS App Extension target and the macOS System Extension
    /// target — both register the same App ID with Apple. The
    /// NEDNSProxyProviderProtocol.providerBundleIdentifier we set
    /// at save time uses this constant.
    private static let providerBundle = "dev.hanfour.bamboo.app.dnsproxy"

    #if os(macOS)
    private let installer = SystemExtensionInstaller(
        bundleIdentifier: providerBundle
    )
    #endif

    public init() {}

    /// Configure the system's DNS proxy hook. Implementation depends
    /// on platform — see the class comment.
    public func enable() async throws {
        #if os(iOS)
        try await enableIOS()
        #else
        try await enableMacOS()
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
        try await configureManager()
        log.log("MagicDNS (iOS App Extension) enabled: \(Self.providerBundle, privacy: .public)")
    }
    #endif

    #if os(macOS)
    /// macOS path: activate the system extension first (this is
    /// where Privacy & Security + admin-password prompts happen on
    /// first install), then configure NEDNSProxyManager exactly
    /// like the iOS path. Splitting the install step out keeps the
    /// activation visibility honest — if the user denies, the
    /// NEDNSProxyManager save never runs and System Settings is
    /// left clean.
    private func enableMacOS() async throws {
        try await installer.activate()
        try await configureManager()
        log.log("MagicDNS (macOS System Extension) enabled: \(Self.providerBundle, privacy: .public)")
    }
    #endif

    /// Shared NEDNSProxyManager save. Same shape on both
    /// platforms — the only difference is whether a System
    /// Extension activation ran first.
    private func configureManager() async throws {
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
    }

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
