// SPDX-License-Identifier: Apache-2.0

import SwiftUI

/// ContentView is the popover body shown by the menu bar entry. Has
/// two modes: the compact status pane and an expanded settings pane
/// where the user supplies the controller URL + tenant + (optional)
/// pre-auth key. Settings persist via UserDefaults via the view model.
struct ContentView: View {
    @ObservedObject var connection: ConnectionViewModel
    @State private var showSettings = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text("bamboo")
                    .font(.headline)
                Spacer()
                Button(action: { showSettings.toggle() }) {
                    Image(systemName: "gear")
                }
                .buttonStyle(.plain)
            }

            HStack {
                Image(systemName: connection.statusIcon)
                Text(connection.status.rawValue)
            }
            .foregroundStyle(.secondary)

            if let err = connection.lastError {
                Text(err)
                    .font(.caption)
                    .foregroundStyle(.red)
                    .lineLimit(3)
            }

            if showSettings {
                Divider()
                Group {
                    LabelledField(label: "Controller URL", text: $connection.controllerURL)
                    LabelledField(label: "Tenant slug", text: $connection.tenantSlug)
                    LabelledField(label: "Pre-auth key (optional)", text: $connection.preAuthKey)
                }
                .textFieldStyle(.roundedBorder)
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
        .frame(width: 320)
    }
}

private struct LabelledField: View {
    let label: String
    @Binding var text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)
            TextField(label, text: $text)
        }
    }
}
