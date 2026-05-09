// SPDX-License-Identifier: Apache-2.0

import SwiftUI

/// ContentView (iOS) renders the bamboo connection state full-screen.
/// The macOS variant is a popover; this version uses a standard
/// vertical layout suited to the phone.
struct ContentView: View {
    @ObservedObject var connection: ConnectionViewModel

    var body: some View {
        NavigationStack {
            VStack(spacing: 24) {
                Image(systemName: connection.statusIcon)
                    .font(.system(size: 72))
                    .foregroundStyle(.tint)

                Text(connection.status.rawValue)
                    .font(.title3)
                    .foregroundStyle(.secondary)

                if let err = connection.lastError {
                    Text(err)
                        .font(.callout)
                        .foregroundStyle(.red)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal)
                }

                Spacer()

                switch connection.status {
                case .disconnected, .failing:
                    Button("Connect", action: connection.connect)
                        .buttonStyle(.borderedProminent)
                        .controlSize(.large)
                case .connected, .connecting:
                    Button("Disconnect", action: connection.disconnect)
                        .buttonStyle(.bordered)
                        .controlSize(.large)
                }
            }
            .padding()
            .navigationTitle("bamboo")
        }
    }
}
