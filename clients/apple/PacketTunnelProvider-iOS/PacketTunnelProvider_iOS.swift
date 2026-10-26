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

    /// Registers, heartbeats and watches for peer and policy changes while
    /// the app is suspended. Used when the app sends a controlPlane block;
    /// otherwise the app stays in charge and `presence` only keeps the
    /// device online. See ControlPlane.
    private let controlPlane = ControlPlane()

    /// Picks who authenticates as this peer (the control plane when it runs,
    /// else the presence heartbeat), then swaps relay proxy endpoints into
    /// the config.
    private func prepare(_ appConfig: BambooTunnelConfig) async throws -> (effective: BambooTunnelConfig, tunnel: TunnelConfiguration) {
        let auth: PresenceConfig?
        if let cp = appConfig.controlPlane {
            await presence.stop()
            auth = await controlPlane.auth() ?? cp.asPresence()
        } else {
            await presence.apply(appConfig.presence)
            auth = await presence.currentConfig()
        }
        let effective = await mesh.apply(appConfig, auth: auth)
        return (effective, try TunnelConfigurationBuilder.build(from: effective))
    }

    /// The config last handed to WireGuard, after relay endpoints were
    /// swapped in. A peer whose direct endpoint moves while its traffic goes
    /// through the relay leaves this unchanged, so there is nothing to apply.
    private var lastApplied: BambooTunnelConfig?

    /// The app-side config behind `lastApplied`, kept to rebuild the backend.
    private var lastBase: BambooTunnelConfig?

    private var stopping = false
    private var recovering = false

    /// WireGuard closes its device for good when reading the tun fails
    /// (seen as "no buffer space available"). Every later update then fails
    /// with "device closed" and the tunnel looks up but carries nothing, so
    /// stop the backend and start it again from the last config.
    private func recoverBackend(reason: String) async {
        guard !stopping, !recovering, let base = lastBase else { return }
        recovering = true
        defer { recovering = false }
        log.error("backend recover: \(reason, privacy: .public)")
        await withCheckedContinuation { (cont: CheckedContinuation<Void, Never>) in
            adapter.stop { _ in cont.resume() }
        }
        do {
            let prepared = try await prepare(base)
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

    /// Starts the control plane, or hands a running one the app's new base.
    private func runControlPlane(for appConfig: BambooTunnelConfig) async {
        guard let cp = appConfig.controlPlane else {
            await controlPlane.stop()
            return
        }
        if await controlPlane.isRunning, await controlPlane.auth()?.peerID == cp.peerID {
            await controlPlane.rebase(appConfig)
            return
        }
        await controlPlane.start(
            base: appConfig,
            cfg: cp,
            bandwidth: { [weak self] in await self?.readBandwidth() },
            apply: { [weak self] config in await self?.applyFromControlPlane(config) },
            tokenRenewed: { [weak self] auth in await self?.mesh.updateAuth(auth) },
            networkChanged: { [weak self] in await self?.mesh.forceRedial() }
        )
    }

    private func readBandwidth() async -> TunnelIPC.BandwidthStats? {
        await withCheckedContinuation { cont in
            adapter.getRuntimeConfiguration { config in
                guard let config else { cont.resume(returning: nil); return }
                let (sent, received) = WGStatsParser.sumBytes(config)
                cont.resume(returning: .init(bytesSent: sent, bytesReceived: received))
            }
        }
    }

    private func applyFromControlPlane(_ config: BambooTunnelConfig) async {
        guard !recovering else { return }
        lastBase = config
        do {
            let prepared = try await prepare(config)
            if prepared.effective == lastApplied { return }
            let err: Error? = await withCheckedContinuation { cont in
                adapter.update(tunnelConfiguration: prepared.tunnel) { cont.resume(returning: $0) }
            }
            if let err {
                log.error("control-plane apply: \(String(describing: err), privacy: .public)")
                await recoverBackend(reason: "apply failed")
            } else {
                lastApplied = prepared.effective
                log.log("control-plane apply ok peers=\(config.peers.count, privacy: .public)")
            }
        } catch {
            log.error("control-plane apply build: \(String(describing: error), privacy: .public)")
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
                self?.lastApplied = prepared.effective
                self?.lastBase = appConfig
                self?.log.log("tunnel up address=\(appConfig.address, privacy: .public) peers=\(appConfig.peers.count)")
                completionHandler(nil)
                Task { [weak self] in await self?.runControlPlane(for: appConfig) }
            }
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason,
                             completionHandler: @escaping () -> Void) {
        stopping = true
        log.log("stopTunnel reason=\(reason.rawValue, privacy: .public)")
        Task { [presence, mesh, controlPlane] in
            await controlPlane.stop()
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
                // With the control plane running here, its peer view is the
                // truth; the app only supplies the base (relay, key, port).
                if appConfig.controlPlane != nil, await self.controlPlane.isRunning {
                    await self.runControlPlane(for: appConfig)
                    completionHandler?(self.encodeOK())
                    return
                }
                let prepared: (effective: BambooTunnelConfig, tunnel: TunnelConfiguration)
                do {
                    prepared = try await self.prepare(appConfig)
                } catch {
                    self.log.error("applyConfig build: \(String(describing: error), privacy: .public)")
                    completionHandler?(self.encodeError("build: \(error)"))
                    return
                }
                self.adapter.update(tunnelConfiguration: prepared.tunnel) { [weak self] error in
                    if let error = error {
                        self?.log.error("adapter.update: \(String(describing: error), privacy: .public)")
                        completionHandler?(self?.encodeError("update: \(error)"))
                        return
                    }
                    self?.lastApplied = prepared.effective
                    self?.lastBase = appConfig
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

        case .peerSnapshot:
            Task { [weak self] in
                guard let self else { return }
                guard await self.controlPlane.isRunning else {
                    completionHandler?(self.encodeError("control plane not running"))
                    return
                }
                let snap = await self.controlPlane.snapshot()
                completionHandler?((try? TunnelIPC.encode(TunnelIPC.Response(ok: true, snapshot: snap))) ?? Data())
            }

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
