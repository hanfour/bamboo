// SPDX-License-Identifier: Apache-2.0

import Foundation
import NetworkExtension
import OSLog
import WireGuardKit

#if os(macOS)
private let extensionBundleID = "dev.hanfour.bamboo.app.tunnel"
#else
private let extensionBundleID = "dev.hanfour.bamboo.app.tunnel"
#endif

/// TunnelManager owns the NETunnelProviderManager for the bamboo VPN
/// configuration. It is responsible for:
///
///   - Looking up (or creating) the single saved bamboo VPN profile
///   - Writing a fresh BambooTunnelConfig into providerConfiguration
///   - Calling startVPNTunnel / stopVPNTunnel
///
/// The view model owns one TunnelManager and observes its status via
/// the published `connectionStatus`.
@MainActor
public final class TunnelManager: ObservableObject {
    public enum Status: Equatable {
        case unknown
        case disconnected
        case connecting
        case connected
        case disconnecting
        case failing(String)
    }

    @Published public private(set) var status: Status = .unknown

    private let log = Logger(subsystem: "dev.hanfour.bamboo.app", category: "tunnel")
    private var manager: NETunnelProviderManager?
    private var statusObserver: NSObjectProtocol?

    public init() {}

    /// Reload the saved tunnel manager (or no-op if none exists).
    public func refresh() async {
        do {
            let managers = try await NETunnelProviderManager.loadAllFromPreferences()
            self.manager = managers.first
            updateStatus()
            attachObserver()
        } catch {
            log.error("loadAllFromPreferences: \(String(describing: error), privacy: .public)")
            self.status = .failing(String(describing: error))
        }
    }

    /// Install (or update) the bamboo profile and start the tunnel.
    public func startTunnel(with config: BambooTunnelConfig) async throws {
        let mgr = manager ?? NETunnelProviderManager()
        let proto = (mgr.protocolConfiguration as? NETunnelProviderProtocol) ?? NETunnelProviderProtocol()
        proto.providerBundleIdentifier = extensionBundleID
        // The serverAddress field is informational on iOS / macOS but
        // required to be non-empty by NETunnelProviderManager.
        proto.serverAddress = "bamboo"

        let payload = try JSONEncoder().encode(config)
        proto.providerConfiguration = ["bamboo": payload]
        mgr.protocolConfiguration = proto
        mgr.localizedDescription = "bamboo"
        mgr.isEnabled = true

        try await mgr.saveToPreferences()
        // Per Apple docs, we have to load again after save before
        // starting the tunnel — saveToPreferences invalidates the
        // in-memory configuration object.
        try await mgr.loadFromPreferences()

        self.manager = mgr
        attachObserver()

        try mgr.connection.startVPNTunnel()
        updateStatus()
    }

    public func stopTunnel() {
        guard let mgr = manager else { return }
        mgr.connection.stopVPNTunnel()
        updateStatus()
    }

    // MARK: - private

    private func attachObserver() {
        if let token = statusObserver {
            NotificationCenter.default.removeObserver(token)
            statusObserver = nil
        }
        guard let conn = manager?.connection else { return }
        statusObserver = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange,
            object: conn,
            queue: .main,
            using: { [weak self] _ in
                Task { @MainActor in self?.updateStatus() }
            }
        )
    }

    private func updateStatus() {
        guard let conn = manager?.connection else {
            self.status = .disconnected
            return
        }
        switch conn.status {
        case .invalid, .disconnected:    self.status = .disconnected
        case .connecting:                self.status = .connecting
        case .connected:                 self.status = .connected
        case .disconnecting:             self.status = .disconnecting
        case .reasserting:               self.status = .connecting
        @unknown default:                self.status = .unknown
        }
    }
}
