// SPDX-License-Identifier: Apache-2.0

import NetworkExtension

/// PacketTunnelProvider is the iOS entry point for the system-level
/// WireGuard tunnel. It runs in a separate process from the app.
///
/// Phase-2 scaffolding stub: acknowledges start/stop and echoes app
/// messages. The WireGuardKit integration that actually carries
/// packets lands in a follow-up PR.
final class PacketTunnelProvider_iOS: NEPacketTunnelProvider {
    override func startTunnel(options: [String: NSObject]?,
                              completionHandler: @escaping (Error?) -> Void) {
        completionHandler(nil)
    }

    override func stopTunnel(with reason: NEProviderStopReason,
                             completionHandler: @escaping () -> Void) {
        completionHandler()
    }

    override func handleAppMessage(_ messageData: Data,
                                   completionHandler: ((Data?) -> Void)?) {
        completionHandler?(messageData)
    }
}
