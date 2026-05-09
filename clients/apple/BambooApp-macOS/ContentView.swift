// SPDX-License-Identifier: Apache-2.0

import SwiftUI

/// ContentView is the popover body shown by the menu bar entry.
struct ContentView: View {
    @ObservedObject var connection: ConnectionViewModel

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("bamboo")
                .font(.headline)

            HStack {
                Image(systemName: connection.statusIcon)
                Text(connection.status.rawValue)
            }
            .foregroundStyle(.secondary)

            if let err = connection.lastError {
                Text(err)
                    .font(.caption)
                    .foregroundStyle(.red)
            }

            Divider()

            switch connection.status {
            case .disconnected, .failing:
                Button("Connect", action: connection.connect)
            case .connected, .connecting:
                Button("Disconnect", action: connection.disconnect)
            }

            Button("Quit") {
                NSApplication.shared.terminate(nil)
            }
        }
        .padding()
        .frame(width: 240)
    }
}
