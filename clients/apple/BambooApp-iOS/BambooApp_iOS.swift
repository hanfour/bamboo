// SPDX-License-Identifier: Apache-2.0

import SwiftUI

/// BambooApp_iOS is the iOS entry point. Unlike macOS it is a full
/// app rather than a menu-bar extra; the tunnel still lives in a
/// separate PacketTunnelProvider extension target.
@main
struct BambooApp_iOS: App {
    @StateObject private var connection = ConnectionViewModel()

    var body: some Scene {
        WindowGroup {
            ContentView(connection: connection)
        }
    }
}
