// SPDX-License-Identifier: Apache-2.0

import Foundation
import NetworkExtension
import OSLog
import WireGuardKit

/// PacketTunnelProvider_iOS is the system-level WireGuard tunnel for
/// iOS. The control flow is identical to the macOS provider; the
/// distinction is the bundle, entitlements and OSLog category.
final class PacketTunnelProvider_iOS: NEPacketTunnelProvider {
    private let log = Logger(subsystem: "dev.hanfour.bamboo.tunnel", category: "ios")

    /// Keeps the device online on the controller while the host app is
    /// suspended. See PresenceHeartbeat.
    private let presence = PresenceHeartbeat()

    /// Carries peer traffic through the relay from inside the extension, so
    /// it keeps working while the app is suspended. See RelayMesh.
    private let mesh = RelayMesh()

    /// Starts presence first (it holds the freshest session token), then
    /// swaps relay proxy endpoints into the config.
    private func prepare(_ appConfig: BambooTunnelConfig) async throws -> TunnelConfiguration {
        await presence.apply(appConfig.presence)
        let auth = await presence.currentConfig()
        let effective = await mesh.apply(appConfig, auth: auth)
        return try TunnelConfigurationBuilder.build(from: effective)
    }

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

        Task { [weak self] in
            guard let self else { return }
            let tunnelConfig: TunnelConfiguration
            do {
                tunnelConfig = try await self.prepare(appConfig)
            } catch {
                self.log.error("startTunnel: build config: \(String(describing: error), privacy: .public)")
                completionHandler(error)
                return
            }
            self.adapter.start(tunnelConfiguration: tunnelConfig) { [weak self] error in
                if let error = error {
                    self?.log.error("adapter.start: \(String(describing: error), privacy: .public)")
                    completionHandler(error)
                    return
                }
                self?.log.log("tunnel up address=\(appConfig.address, privacy: .public) peers=\(appConfig.peers.count)")
                completionHandler(nil)
            }
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason,
                             completionHandler: @escaping () -> Void) {
        log.log("stopTunnel reason=\(reason.rawValue, privacy: .public)")
        Task { [presence, mesh] in
            await presence.stop()
            await mesh.stop()
        }
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
            Task { [weak self] in
                guard let self else { return }
                let tunnelConfig: TunnelConfiguration
                do {
                    tunnelConfig = try await self.prepare(appConfig)
                } catch {
                    self.log.error("applyConfig build: \(String(describing: error), privacy: .public)")
                    completionHandler?(self.encodeError("build: \(error)"))
                    return
                }
                self.adapter.update(tunnelConfiguration: tunnelConfig) { [weak self] error in
                    if let error = error {
                        self?.log.error("adapter.update: \(String(describing: error), privacy: .public)")
                        completionHandler?(self?.encodeError("update: \(error)"))
                        return
                    }
                    self?.log.log("applyConfig ok peers=\(appConfig.peers.count, privacy: .public)")
                    completionHandler?(self?.encodeOK())
                }
            }

        case .status:
            let resp = TunnelIPC.Response(
                ok: true,
                status: .init(connected: true, peerCount: -1)
            )
            completionHandler?((try? TunnelIPC.encode(resp)) ?? Data())

        case .bandwidthStats:
            // Mirror of the macOS provider's path — see comments
            // there. Forwarded through TunnelManager.bandwidthStats
            // into HeartbeatLoop's per-tick reporter.
            adapter.getRuntimeConfiguration { [weak self] config in
                let (sent, received) = config.map(WGStatsParser.sumBytes) ?? (0, 0)
                let resp = TunnelIPC.Response(
                    ok: true,
                    bandwidth: .init(bytesSent: sent, bytesReceived: received)
                )
                completionHandler?((try? TunnelIPC.encode(resp)) ?? Data())
                _ = self
            }
        }
    }

    private func encodeOK() -> Data {
        let resp = TunnelIPC.Response(ok: true)
        return (try? TunnelIPC.encode(resp)) ?? Data()
    }

    private func encodeError(_ msg: String) -> Data {
        let resp = TunnelIPC.Response(ok: false, error: msg)
        return (try? TunnelIPC.encode(resp)) ?? Data()
    }
}
