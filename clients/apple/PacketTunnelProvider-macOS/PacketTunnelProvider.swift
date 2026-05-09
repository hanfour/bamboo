// SPDX-License-Identifier: Apache-2.0

import NetworkExtension

/// PacketTunnelProvider is the macOS entry point for the system-level
/// WireGuard tunnel. It runs in a separate process from the menu-bar app.
///
/// The implementation here is a Phase-1 stub: it acknowledges the start
/// request but does not yet bring up a real tunnel. The full path is:
///
///   1. Read tunnel configuration from the protocolConfiguration.
///   2. Resolve any peer endpoints.
///   3. Hand the configuration to BambooCore (Go-backed XCFramework),
///      which drives wireguard-go to read/write packets via the
///      packetFlow.
///   4. Apply NetworkSettings (IP, DNS, routes) and call setTunnelNetworkSettings.
///
/// All four steps land alongside the BambooCore XCFramework integration.
final class PacketTunnelProvider: NEPacketTunnelProvider {
    override func startTunnel(options: [String: NSObject]?,
                              completionHandler: @escaping (Error?) -> Void) {
        // TODO: construct NEPacketTunnelNetworkSettings from controller config
        // TODO: hand packetFlow to wireguard-go bridge
        completionHandler(nil)
    }

    override func stopTunnel(with reason: NEProviderStopReason,
                             completionHandler: @escaping () -> Void) {
        // TODO: tear down wireguard-go and clear network settings.
        completionHandler()
    }

    override func handleAppMessage(_ messageData: Data,
                                   completionHandler: ((Data?) -> Void)?) {
        // The menu-bar app sends control messages through here (status,
        // re-auth, peer-set refresh). Phase 1: echo for sanity check.
        completionHandler?(messageData)
    }
}
