// SPDX-License-Identifier: Apache-2.0

import SwiftUI

/// BambooApp is the menu-bar entry point for the macOS client.
///
/// The app itself is intentionally thin: the heavy work (key management,
/// controller chatter, tunnel orchestration) lives in BambooCore (Swift
/// wrapper around the Go `clients/core` library, packaged as an
/// XCFramework) and the PacketTunnelProvider extension target.
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
