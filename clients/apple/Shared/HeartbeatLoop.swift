// SPDX-License-Identifier: Apache-2.0

import Foundation
import OSLog

/// HeartbeatLoop pings POST /api/v1/peers/heartbeat at a fixed cadence
/// while the tunnel is up. Rerunning STUN discovery on each tick lets
/// the controller learn about NAT-mapping changes (Wi-Fi swap, sleep
/// resume, mobile-data hand-off) within ~30s. Errors are logged and
/// tolerated; the loop continues so a transient outage doesn't kill
/// the session.
public actor HeartbeatLoop {
    public static let interval: TimeInterval = 30

    private let log = Logger(subsystem: "dev.hanfour.bamboo.app", category: "heartbeat")
    private var task: Task<Void, Never>?

    public init() {}

    /// Start (or replace) the heartbeat loop. Calling start() again
    /// while already running first cancels the previous task.
    ///
    /// knownRevision is a closure rather than a value because the
    /// caller's policy revision moves over time as the controller
    /// pushes ACL updates (issue #132). The heartbeat reports
    /// whichever revision the ViewModel currently considers
    /// authoritative; the controller sets policyChanged=true when
    /// its revision differs, which the onPolicyChanged callback
    /// then resolves via a re-register.
    public func start(client: BambooClient,
                      peerID: String,
                      knownRevision: @Sendable @escaping () async -> Int64 = { 0 },
                      bandwidthStats: @Sendable @escaping () async -> TunnelIPC.BandwidthStats? = { nil },
                      onPolicyChanged: @Sendable @escaping (Int64) -> Void = { _ in },
                      onPeersChanged: @Sendable @escaping () -> Void = {}) {
        task?.cancel()
        let log = self.log
        task = Task {
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: UInt64(HeartbeatLoop.interval * 1_000_000_000))
                if Task.isCancelled { return }

                // Refresh STUN. Failure is non-fatal — we still
                // heartbeat with a nil endpoints list.
                var endpoints: [String]? = nil
                do {
                    endpoints = [try await STUNClient.discover()]
                } catch {
                    log.debug("heartbeat stun refresh failed: \(String(describing: error), privacy: .public)")
                }

                // Pull cumulative wg byte counters from the
                // extension via TunnelIPC.bandwidthStats. nil ⇒
                // tunnel not up yet / IPC failed / older
                // extension build; we still heartbeat, just
                // without the bandwidth side-channel so the
                // controller skips its bandwidth_sample write.
                let bw = await bandwidthStats()

                let rev = await knownRevision()
                do {
                    let resp = try await client.heartbeat(.init(
                        peerId: peerID,
                        knownPolicyRevision: rev,
                        endpoints: endpoints,
                        bytesSent: bw?.bytesSent,
                        bytesReceived: bw?.bytesReceived
                    ))
                    if resp.policyChanged {
                        onPolicyChanged(resp.currentPolicyRevision)
                    }
                    if resp.peersChanged {
                        onPeersChanged()
                    }
                } catch {
                    log.warning("heartbeat: \(String(describing: error), privacy: .public)")
                }
            }
        }
    }

    public func stop() {
        task?.cancel()
        task = nil
    }
}
