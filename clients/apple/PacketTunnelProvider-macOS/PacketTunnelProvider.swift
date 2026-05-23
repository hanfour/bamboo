// SPDX-License-Identifier: Apache-2.0

import Foundation
import NetworkExtension
import OSLog
import WireGuardKit

/// PacketTunnelProvider (macOS) is the system-level WireGuard tunnel.
/// It runs in a separate process from the menu-bar app; the two
/// communicate via NETunnelProviderProtocol's providerConfiguration
/// (set by the app at start-up time) and IPC messages
/// (handleAppMessage).
final class PacketTunnelProvider: NEPacketTunnelProvider {
    private let log = Logger(subsystem: "dev.hanfour.bamboo.tunnel", category: "macos")

    private lazy var adapter: WireGuardAdapter = WireGuardAdapter(with: self) { [weak self] level, message in
        self?.log.log("[wg/\(level.rawValue, privacy: .public)] \(message, privacy: .public)")
    }

    override func startTunnel(options _: [String: NSObject]?,
                              completionHandler: @escaping (Error?) -> Void) {
        guard
            let proto = self.protocolConfiguration as? NETunnelProviderProtocol,
            let providerConfig = proto.providerConfiguration,
            let payload = providerConfig["bamboo"] as? Data
        else {
            log.error("startTunnel: missing providerConfiguration['bamboo']")
            completionHandler(BambooTunnelError.missingConfiguration)
            return
        }

        let appConfig: BambooTunnelConfig
        do {
            appConfig = try JSONDecoder().decode(BambooTunnelConfig.self, from: payload)
        } catch {
            log.error("startTunnel: decode config: \(String(describing: error), privacy: .public)")
            completionHandler(error)
            return
        }

        let tunnelConfig: TunnelConfiguration
        do {
            tunnelConfig = try TunnelConfigurationBuilder.build(from: appConfig)
        } catch {
            log.error("startTunnel: build config: \(String(describing: error), privacy: .public)")
            completionHandler(error)
            return
        }

        adapter.start(tunnelConfiguration: tunnelConfig) { [weak self] error in
            if let error = error {
                self?.log.error("adapter.start: \(String(describing: error), privacy: .public)")
                completionHandler(error)
                return
            }
            self?.log.log("tunnel up address=\(appConfig.address, privacy: .public) peers=\(appConfig.peers.count)")
            completionHandler(nil)
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason,
                             completionHandler: @escaping () -> Void) {
        log.log("stopTunnel reason=\(reason.rawValue, privacy: .public)")
        adapter.stop { [weak self] error in
            if let error = error {
                self?.log.error("adapter.stop: \(String(describing: error), privacy: .public)")
            }
            completionHandler()
        }
    }

    override func handleAppMessage(_ messageData: Data,
                                   completionHandler: ((Data?) -> Void)?) {
        let request: TunnelIPC.Request
        do {
            request = try TunnelIPC.decode(messageData)
        } catch {
            log.warning("handleAppMessage: decode: \(String(describing: error), privacy: .public)")
            completionHandler?(self.encodeError("decode failed"))
            return
        }

        switch request.kind {
        case .applyConfig:
            guard let appConfig = request.config else {
                completionHandler?(self.encodeError("applyConfig missing config"))
                return
            }
            let tunnelConfig: TunnelConfiguration
            do {
                tunnelConfig = try TunnelConfigurationBuilder.build(from: appConfig)
            } catch {
                log.error("applyConfig build: \(String(describing: error), privacy: .public)")
                completionHandler?(self.encodeError("build: \(error)"))
                return
            }
            adapter.update(tunnelConfiguration: tunnelConfig) { [weak self] error in
                if let error = error {
                    self?.log.error("adapter.update: \(String(describing: error), privacy: .public)")
                    completionHandler?(self?.encodeError("update: \(error)"))
                    return
                }
                // WireGuardKit's wgSetConfig (the C bridge call inside
                // adapter.update at WireGuardAdapter.swift:265) does
                // NOT propagate IPC failures back to Swift — when
                // wireguard-go logs e.g. "failed to create new peer:
                // device closed", the Swift callback still fires with
                // nil error. The tunnel then runs with the old (or
                // empty) peer set and handshakes silently fail. Verify
                // by reading back the runtime config; on mismatch
                // return an error so the host app's tunnel.applyConfig
                // returns false and falls back to a fresh startTunnel.
                self?.adapter.getRuntimeConfiguration { runtimeConfig in
                    let expectedPeers = appConfig.peers.count
                    let actualPeers = runtimeConfig?
                        .split(separator: "\n")
                        .filter { $0.hasPrefix("public_key=") }
                        .count ?? 0
                    if actualPeers < expectedPeers {
                        self?.log.error("applyConfig: peer count mismatch expected=\(expectedPeers, privacy: .public) actual=\(actualPeers, privacy: .public); update silently failed")
                        completionHandler?(self?.encodeError("update silent fail; peers=\(actualPeers)"))
                        return
                    }
                    self?.log.log("applyConfig ok peers=\(actualPeers, privacy: .public)")
                    completionHandler?(self?.encodeOK())
                }
            }

        case .status:
            // Minimal: connected if adapter has a current configuration.
            // Richer per-peer handshake info is a follow-up.
            let resp = TunnelIPC.Response(
                ok: true,
                status: .init(connected: true, peerCount: -1)
            )
            completionHandler?((try? TunnelIPC.encode(resp)) ?? Data())
        }
    }

    // MARK: - IPC reply helpers

    private func encodeOK() -> Data {
        let resp = TunnelIPC.Response(ok: true)
        return (try? TunnelIPC.encode(resp)) ?? Data()
    }

    private func encodeError(_ msg: String) -> Data {
        let resp = TunnelIPC.Response(ok: false, error: msg)
        return (try? TunnelIPC.encode(resp)) ?? Data()
    }
}
