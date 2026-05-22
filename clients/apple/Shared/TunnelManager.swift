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

    #if os(macOS)
    // macOS PacketTunnelProvider is a System Extension. Activated
    // lazily on the first startTunnel() call so the user only sees
    // the Privacy & Security prompt when they actually try to
    // connect — not at app launch.
    private let installer = SystemExtensionInstaller(
        bundleIdentifier: extensionBundleID
    )
    private var systemExtensionActivated = false
    #endif

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
        #if os(macOS)
        if !systemExtensionActivated {
            try await installer.activate()
            systemExtensionActivated = true
        }
        #endif

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

    /// applyConfig pushes a fresh BambooTunnelConfig to the running
    /// extension via IPC. The extension reapplies the WireGuard
    /// configuration without dropping the tunnel
    /// (`WireGuardAdapter.update`). Returns true on success, false on
    /// any error — caller should fall back to startTunnel(with:) when
    /// false (e.g. the extension isn't running yet).
    @discardableResult
    public func applyConfig(_ config: BambooTunnelConfig) async -> Bool {
        guard let session = manager?.connection as? NETunnelProviderSession else {
            return false
        }
        // Only useful when the tunnel is up; otherwise startTunnel
        // already does the right thing.
        guard session.status == .connected || session.status == .connecting else {
            return false
        }

        let request = TunnelIPC.Request(kind: .applyConfig, config: config)
        let payload: Data
        do {
            payload = try TunnelIPC.encode(request)
        } catch {
            log.error("applyConfig encode: \(String(describing: error), privacy: .public)")
            return false
        }

        return await withCheckedContinuation { (cont: CheckedContinuation<Bool, Never>) in
            do {
                try session.sendProviderMessage(payload) { response in
                    guard let response = response else {
                        cont.resume(returning: false)
                        return
                    }
                    if let decoded = try? TunnelIPC.decodeResponse(response), decoded.ok {
                        cont.resume(returning: true)
                    } else {
                        cont.resume(returning: false)
                    }
                }
            } catch {
                self.log.warning("sendProviderMessage: \(String(describing: error), privacy: .public)")
                cont.resume(returning: false)
            }
        }
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
