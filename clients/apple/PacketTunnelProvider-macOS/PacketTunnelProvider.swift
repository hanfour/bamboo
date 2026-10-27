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

    private func prepare(_ appConfig: BambooTunnelConfig) async throws -> (effective: BambooTunnelConfig, tunnel: TunnelConfiguration) {
        let effective = await mesh.apply(appConfig, auth: appConfig.presence)
        return (effective, try TunnelConfigurationBuilder.build(from: effective))
    }

    /// The config the app last sent, applied again when a peer moves
    /// between the relay and a direct address.
    private var lastAppConfig: BambooTunnelConfig?

    private func runtimeDump() async -> String? {
        await withCheckedContinuation { cont in
            adapter.getRuntimeConfiguration { cont.resume(returning: $0) }
        }
    }

    /// The config last handed to WireGuard, after relay and path swaps.
    private var lastApplied: BambooTunnelConfig?

    /// Gives WireGuard what changed since `lastApplied` and nothing more:
    /// nothing when only tokens moved, an in-place endpoint update when only
    /// endpoints moved, a full update otherwise. A full update rebuilds every
    /// peer and the network settings, which is slow and stirs up the route
    /// table.
    private func applyPrepared(_ prepared: (effective: BambooTunnelConfig, tunnel: TunnelConfiguration)) async -> Error? {
        switch prepared.effective.wgDelta(from: lastApplied) {
        case .none:
            lastApplied = prepared.effective
            return nil
        case .endpoints(let moved):
            var byKey: [PublicKey: String] = [:]
            for (b64, endpoint) in moved {
                if let k = PublicKey(base64Key: b64) { byKey[k] = endpoint }
            }
            if byKey.count == moved.count {
                let err: Error? = await withCheckedContinuation { cont in
                    adapter.updateEndpoints(byKey) { cont.resume(returning: $0) }
                }
                if err == nil { lastApplied = prepared.effective }
                return err
            }
        case .full:
            break
        }
        let err: Error? = await withCheckedContinuation { cont in
            adapter.update(tunnelConfiguration: prepared.tunnel) { cont.resume(returning: $0) }
        }
        if err == nil { lastApplied = prepared.effective }
        return err
    }

    private func reapplyForPathChange() async {
        guard !recovering, let appConfig = lastAppConfig else { return }
        do {
            let prepared = try await prepare(appConfig)
            if let err = await applyPrepared(prepared) {
                log.error("path change apply: \(String(describing: err), privacy: .public)")
                await recoverBackend(reason: "path change apply failed")
            }
        } catch {
            log.error("path change build: \(String(describing: error), privacy: .public)")
        }
    }

    private var stopping = false
    private var recovering = false

    /// While a direct address is being tried, make WireGuard send to it now
    /// (a keepalive) so this end's NAT opens toward the peer.
    private func kickPaths(_ endpoints: [String: String]) async {
        guard !recovering else { return }
        var byKey: [PublicKey: String] = [:]
        for (b64, endpoint) in endpoints {
            if let k = PublicKey(base64Key: b64) { byKey[k] = endpoint }
        }
        guard !byKey.isEmpty else { return }
        let _: Error? = await withCheckedContinuation { cont in
            adapter.updateEndpoints(byKey, keepalive: 25) { cont.resume(returning: $0) }
        }
    }

    /// WireGuard closes its device for good when reading the routing socket
    /// overflows ("no buffer space available"), which busy route tables
    /// (another VPN, frequent updates) can cause. Every later update then
    /// fails with "device closed" and the tunnel carries nothing while still
    /// showing as connected. Stop the backend and start it again from the
    /// last config.
    private func recoverBackend(reason: String) async {
        guard !stopping, !recovering, let appConfig = lastAppConfig else { return }
        recovering = true
        defer { recovering = false }
        log.error("backend recover: \(reason, privacy: .public)")
        await withCheckedContinuation { (cont: CheckedContinuation<Void, Never>) in
            adapter.stop { _ in cont.resume() }
        }
        do {
            let prepared = try await prepare(appConfig)
            let err: Error? = await withCheckedContinuation { cont in
                adapter.start(tunnelConfiguration: prepared.tunnel) { cont.resume(returning: $0) }
            }
            if let err {
                log.error("backend recover: start failed: \(String(describing: err), privacy: .public)")
                cancelTunnelWithError(err)
                return
            }
            lastApplied = prepared.effective
            log.log("backend recover: ok peers=\(prepared.effective.peers.count, privacy: .public)")
        } catch {
            log.error("backend recover: build failed: \(String(describing: error), privacy: .public)")
            cancelTunnelWithError(error)
        }
    }

    private lazy var adapter: WireGuardAdapter = WireGuardAdapter(with: self) { [weak self] level, message in
        guard let self else { return }
        self.log.log("[wg/\(level.rawValue, privacy: .public)] \(message, privacy: .public)")
        if message.contains("Failed to read packet from TUN device") {
            Task { await self.recoverBackend(reason: message) }
        }
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
            let prepared: (effective: BambooTunnelConfig, tunnel: TunnelConfiguration)
            do {
                prepared = try await self.prepare(appConfig)
            } catch {
                self.log.error("startTunnel: build config: \(String(describing: error), privacy: .public)")
                completionHandler(error)
                return
            }
            self.adapter.start(tunnelConfiguration: prepared.tunnel) { [weak self] error in
                if let error = error {
                    self?.log.error("adapter.start: \(String(describing: error), privacy: .public)")
                    completionHandler(error)
                    return
                }
                self?.log.log("tunnel up address=\(appConfig.address, privacy: .public) peers=\(appConfig.peers.count)")
                self?.lastAppConfig = appConfig
                self?.lastApplied = prepared.effective
                completionHandler(nil)
                Task { [weak self] in
                    guard let self else { return }
                    await self.mesh.enableDirectPaths(
                        stats: { [weak self] in await self?.runtimeDump() },
                        changed: { [weak self] in await self?.reapplyForPathChange() },
                        kick: { [weak self] endpoints in await self?.kickPaths(endpoints) }
                    )
                }
            }
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason,
                             completionHandler: @escaping () -> Void) {
        stopping = true
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
                self.lastAppConfig = appConfig
                let prepared: (effective: BambooTunnelConfig, tunnel: TunnelConfiguration)
                do {
                    prepared = try await self.prepare(appConfig)
                } catch {
                    self.log.error("applyConfig build: \(String(describing: error), privacy: .public)")
                    completionHandler?(self.encodeError("build: \(error)"))
                    return
                }
                if let error = await self.applyPrepared(prepared) {
                    self.log.error("adapter.update: \(String(describing: error), privacy: .public)")
                    completionHandler?(self.encodeError("update: \(error)"))
                    await self.recoverBackend(reason: "applyConfig failed")
                    return
                }
                self.log.log("applyConfig ok peers=\(appConfig.peers.count, privacy: .public)")
                completionHandler?(self.encodeOK())
            }

        case .status:
            // Minimal: connected if adapter has a current configuration.
            // Richer per-peer handshake info is a follow-up.
            let resp = TunnelIPC.Response(
                ok: true,
                status: .init(connected: true, peerCount: -1)
            )
            completionHandler?((try? TunnelIPC.encode(resp)) ?? Data())

        case .peerSnapshot:
            // The control plane runs in the app on macOS, not here.
            completionHandler?(self.encodeError("control plane not running"))

        case .sessionToken:
            // The control plane runs in the app on macOS; it already has the token.
            completionHandler?(self.encodeError("control plane not running"))

        case .bandwidthStats:
            // adapter.getRuntimeConfiguration returns the wg-uapi
            // config string asynchronously. WGStatsParser sums
            // tx_bytes / rx_bytes across every peer block. nil ⇒
            // adapter has no active backend (tunnel not up yet);
            // we report (0, 0) so the controller's heartbeat
            // bandwidth-sample side-channel skips the write.
            adapter.getRuntimeConfiguration { [weak self] config in
                let (sent, received) = config.map(WGStatsParser.sumBytes) ?? (0, 0)
                Task {
                    let path = await self?.mesh.connectionPath()
                    let resp = TunnelIPC.Response(
                        ok: true,
                        bandwidth: .init(bytesSent: sent, bytesReceived: received, connectionPath: path)
                    )
                    completionHandler?((try? TunnelIPC.encode(resp)) ?? Data())
                }
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
