// SPDX-License-Identifier: Apache-2.0

import SwiftUI

/// ContentView (iOS) renders the bamboo connection state full-screen,
/// with a Settings sheet for the controller URL, tenant slug, and
/// optional pre-auth key.
struct ContentView: View {
    @ObservedObject var connection: ConnectionViewModel
    @State private var showingSettings = false

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
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button(action: { showingSettings = true }) {
                        Image(systemName: "gear")
                    }
                }
            }
            .sheet(isPresented: $showingSettings) {
                SettingsView(connection: connection)
            }
        }
    }
}

private struct SettingsView: View {
    @ObservedObject var connection: ConnectionViewModel
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            Form {
                Section("Controller") {
                    TextField("Controller URL", text: $connection.controllerURL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                    TextField("Tenant slug", text: $connection.tenantSlug)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                }
                Section("Authentication") {
                    SecureField("Pre-auth key (optional)", text: $connection.preAuthKey)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                }
                Section("Relay (optional)") {
                    TextField("wss://relay.example/relay", text: $connection.relayURL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                }
                Section("Device") {
                    TextField("Hostname", text: $connection.hostname)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                }
            }
            .navigationTitle("Settings")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }
}
