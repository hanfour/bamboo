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
                self?.log.log("applyConfig ok peers=\(appConfig.peers.count, privacy: .public)")
                completionHandler?(self?.encodeOK())
            }

        case .status:
            let resp = TunnelIPC.Response(
                ok: true,
                status: .init(connected: true, peerCount: -1)
            )
            completionHandler?((try? TunnelIPC.encode(resp)) ?? Data())
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
