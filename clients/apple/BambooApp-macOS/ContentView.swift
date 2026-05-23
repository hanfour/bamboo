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

            if let email = connection.signedInEmail {
                HStack(spacing: 6) {
                    Image(systemName: "person.crop.circle.badge.checkmark")
                    Text(email)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
                .font(.caption)
                .foregroundStyle(.secondary)
            }

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

                    // Auth section: prefer OIDC, fall back to a pre-
                    // auth key for headless / CI cases. The visible
                    // CTA flips based on whether we already hold a
                    // Keychain session.
                    if connection.signedInEmail != nil {
                        HStack {
                            Text("Signed in")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            Spacer()
                            Button("Sign out", action: connection.signOut)
                                .buttonStyle(.borderless)
                                .controlSize(.small)
                        }
                    } else {
                        Button(action: connection.signIn) {
                            HStack {
                                if connection.isSigningIn {
                                    ProgressView()
                                        .controlSize(.small)
                                }
                                Text(connection.isSigningIn ? "Signing in…" : "Sign in with Google")
                            }
                            .frame(maxWidth: .infinity)
                        }
                        .buttonStyle(.borderedProminent)
                        .controlSize(.small)
                        .disabled(connection.isSigningIn)
                    }

                    LabelledField(label: "Pre-auth key (fallback)", text: $connection.preAuthKey)
                    LabelledField(label: "Relay URL (optional)", text: $connection.relayURL)
                    LabelledField(label: "Advertise routes (CIDRs, comma-separated; admin must approve)",
                                  text: $connection.advertiseRoutes)
                    Toggle("Offer this device as an exit node (admin approval required)",
                           isOn: $connection.advertiseExitNode)
                        .controlSize(.small)
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
