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

    /// Carries peer traffic through the relay from inside the system
    /// extension, so it survives the app quitting. See RelayMesh.
    private let mesh = RelayMesh()

    private func prepare(_ appConfig: BambooTunnelConfig) async throws -> TunnelConfiguration {
        let effective = await mesh.apply(appConfig, auth: appConfig.presence)
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
        Task { [mesh] in await mesh.stop() }
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
            // Minimal: connected if adapter has a current configuration.
            // Richer per-peer handshake info is a follow-up.
            let resp = TunnelIPC.Response(
                ok: true,
                status: .init(connected: true, peerCount: -1)
            )
            completionHandler?((try? TunnelIPC.encode(resp)) ?? Data())

        case .bandwidthStats:
            // adapter.getRuntimeConfiguration returns the wg-uapi
            // config string asynchronously. WGStatsParser sums
            // tx_bytes / rx_bytes across every peer block. nil ⇒
            // adapter has no active backend (tunnel not up yet);
            // we report (0, 0) so the controller's heartbeat
            // bandwidth-sample side-channel skips the write.
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
