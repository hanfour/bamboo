// SPDX-License-Identifier: Apache-2.0

import SwiftUI

/// BambooApp is the menu-bar entry point for the macOS client.
///
/// The app itself is intentionally thin: the heavy work (key management,
/// gRPC chatter, tunnel orchestration) lives in BambooCore (Swift wrapper
/// around the Go `clients/core` library, packaged as an XCFramework).
///
/// The actual WireGuard tunnel runs in the PacketTunnelProvider extension
/// target — see clients/macos/PacketTunnelProvider/.
@main
struct BambooApp: App {
    @StateObject private var connection = ConnectionViewModel()

    var body: some Scene {
        MenuBarExtra("bamboo", systemImage: connection.statusIcon) {
            ContentView(connection: connection)
        }
        .menuBarExtraStyle(.window)
    }
}

/// ConnectionViewModel exposes the connection state to SwiftUI.
///
/// All methods on this type are placeholders. The wiring to BambooCore
/// (and through it to the controller / tunnel) lands in a follow-up.
@MainActor
final class ConnectionViewModel: ObservableObject {
    enum Status: String {
        case disconnected = "Not connected"
        case connecting   = "Connecting…"
        case connected    = "Connected"
        case failing      = "Failing"
    }

    @Published var status: Status = .disconnected
    @Published var lastError: String?

    var statusIcon: String {
        switch status {
        case .disconnected: return "circle"
        case .connecting:   return "circle.dashed"
        case .connected:    return "circle.fill"
        case .failing:      return "exclamationmark.triangle"
        }
    }

    func connect() {
        // TODO: register with controller, launch PacketTunnelProvider via
        // NETunnelProviderManager. Tracked alongside Sprint 2 issue #16.
        status = .connecting
    }

    func disconnect() {
        // TODO: tear down the active session.
        status = .disconnected
    }
}
