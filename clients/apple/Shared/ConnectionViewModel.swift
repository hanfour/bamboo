// SPDX-License-Identifier: Apache-2.0

import Foundation

/// ConnectionViewModel exposes the bamboo tunnel connection state to
/// SwiftUI on macOS and iOS. The view-model is platform-agnostic; the
/// platform-specific entry points (BambooApp-macOS, BambooApp-iOS)
/// instantiate it and bind it to their UI.
///
/// All `connect` / `disconnect` paths are stubbed at this scaffolding
/// stage. Real wiring lands in the SwiftUI-app PR alongside the
/// NETunnelProviderManager + WireGuardKit integration.
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

    public init() {}

    public var statusIcon: String {
        switch status {
        case .disconnected: return "circle"
        case .connecting:   return "circle.dashed"
        case .connected:    return "circle.fill"
        case .failing:      return "exclamationmark.triangle"
        }
    }

    public func connect() {
        // Real implementation will register with the controller and
        // launch the PacketTunnelProvider via NETunnelProviderManager.
        status = .connecting
    }

    public func disconnect() {
        status = .disconnected
    }
}
