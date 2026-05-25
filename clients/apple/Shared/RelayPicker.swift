// SPDX-License-Identifier: Apache-2.0

import Foundation
import Network

/// RelayPicker selects the lowest-RTT relay from a controller-
/// supplied list (§4 P2 multi-relay stage 2 — Apple parity to
/// PR #193's CLI implementation).
///
/// Selection mechanics mirror the CLI: parallel TCP-connect
/// probes via NWConnection, pick the lowest-RTT eligible row.
/// Picking on connect time (not HTTP /healthz round-trip)
/// matches the 5-tuple a real relay session uses, so a stuck
/// firewall on the relay port surfaces here rather than later
/// as a handshake failure.
///
/// The user-typed Settings `relayURL` is honored as an override
/// upstream in ConnectionViewModel — RelayPicker only runs when
/// that field is empty.
internal enum RelayPicker {
    /// probeTimeout caps each per-relay probe. Tighter than the
    /// controller's 3s /healthz cadence because the Apple client
    /// is racing to bring the tunnel up; every second spent
    /// probing is a second the user sees a spinner. 1s fingerprints
    /// inter-region latency cleanly (typical 50-300ms cross-region,
    /// <30ms intra-region); anything slower would be too slow to
    /// prefer anyway.
    static let probeTimeout: TimeInterval = 1.0

    /// ProbeFn is the test seam — production binds it to
    /// probeRelayRTT (real TCP connect via NWConnection); tests
    /// inject a deterministic latency table. Latency-driven
    /// ranking can't be tested with localhost probes (the kernel
    /// completes the SYN-ACK before any user-space callback runs),
    /// so isolating the seam is the only way to lock the ASC sort
    /// direction without flakiness.
    typealias ProbeFn = (_ hostname: String, _ port: Int) async -> Result<TimeInterval, Error>

    static var probeFn: ProbeFn = probeRelayRTT

    /// regionToleranceSeconds is the §4 P2 stage 5 affinity window.
    /// A same-region relay wins over a lower-RTT out-of-region one
    /// when the RTT gap is within this many seconds — same magnitude
    /// as typical intra-AZ jitter, so the picker isn't preferring
    /// slowness for the sake of geography. Beyond the tolerance the
    /// cross-region option is genuinely faster and wins.
    ///
    /// 50ms (0.05s) matches the CLI's relayRegionToleranceMs so both
    /// clients pick the same relay against the same hint.
    static let regionToleranceSeconds: TimeInterval = 0.05

    /// pickLowestRTT returns the relay with the smallest measured
    /// RTT, or nil when:
    ///   * the candidate list is empty
    ///   * every probe failed (caller logs the cause and degrades
    ///     to direct-only; not an error since the network may
    ///     have direct-only peers anyway)
    ///
    /// preferredRegion is the §4 P2 stage 5 affinity hint. Empty ⇒
    /// pure RTT ranking (back-compat with pre-stage-5 behaviour).
    /// When set, a same-region relay wins over a lower-RTT out-of-
    /// region one if the gap is within `regionToleranceSeconds`.
    ///
    /// Parallel by design: serial would stretch a 5-relay probe to
    /// 5s in the worst case, doubling tunnel bring-up latency the
    /// user can see.
    static func pickLowestRTT(
        from candidates: [BambooClient.RelayServer],
        preferredRegion: String = ""
    ) async -> BambooClient.RelayServer? {
        guard !candidates.isEmpty else { return nil }

        // TaskGroup fans out probes in parallel and collects
        // (index, RTT-or-error) pairs. We retain the original
        // index so the rank-sort can still resolve back to the
        // RelayServer struct without copying it into the group.
        let results: [(Int, TimeInterval)] = await withTaskGroup(
            of: (Int, TimeInterval?).self
        ) { group in
            for (idx, rs) in candidates.enumerated() {
                group.addTask {
                    let outcome = await probeFn(rs.hostname, rs.port)
                    switch outcome {
                    case .success(let rtt):
                        return (idx, rtt)
                    case .failure:
                        return (idx, nil)
                    }
                }
            }
            var ok: [(Int, TimeInterval)] = []
            for await (idx, rtt) in group {
                if let rtt = rtt {
                    ok.append((idx, rtt))
                }
            }
            return ok
        }
        guard !results.isEmpty else { return nil }
        let sorted = results.sorted { $0.1 < $1.1 }
        let winnerIdx = applyRegionAffinity(
            candidates: candidates,
            ranked: sorted,
            preferredRegion: preferredRegion
        )
        return candidates[winnerIdx]
    }

    /// applyRegionAffinity returns the index of the winning relay
    /// after the §4 P2 stage 5 same-region preference. Empty
    /// preferredRegion ⇒ RTT winner. If the RTT winner is already
    /// in the preferred region, no-op. Otherwise the first same-
    /// region row within `regionToleranceSeconds` of the RTT
    /// winner wins; if none qualifies, the RTT winner stays. The
    /// hint can therefore never make the picker pathologically
    /// slow — only same or better than pure RTT.
    private static func applyRegionAffinity(
        candidates: [BambooClient.RelayServer],
        ranked: [(Int, TimeInterval)],
        preferredRegion: String
    ) -> Int {
        let rttWinnerIdx = ranked[0].0
        if preferredRegion.isEmpty { return rttWinnerIdx }
        if candidates[rttWinnerIdx].region == preferredRegion { return rttWinnerIdx }
        let winnerRTT = ranked[0].1
        for (idx, rtt) in ranked {
            guard candidates[idx].region == preferredRegion else { continue }
            if rtt - winnerRTT <= regionToleranceSeconds {
                return idx
            }
            // ranked is ASC by RTT; once a same-region row exceeds
            // the tolerance, all later same-region rows do too.
            break
        }
        return rttWinnerIdx
    }

    /// relayWSSURL builds the wss URL for a chosen relay. Wraps
    /// the (hostname, port) → URL conversion in one place so a
    /// future scheme change (h3, quic) lives here rather than
    /// being threaded through every dial site.
    static func relayWSSURL(_ rs: BambooClient.RelayServer) -> URL? {
        return URL(string: "wss://\(rs.hostname):\(rs.port)/relay")
    }
}

/// probeRelayRTT measures TCP-connect latency to one (hostname,
/// port). Uses NWConnection — Apple's modern Network framework
/// API — which works on macOS 10.14+ / iOS 12+ without falling
/// back to BSD sockets.
///
/// Returns .success(rtt) when the connection reaches .ready
/// state, .failure(timeoutOrErr) when the probe deadline trips
/// or the underlying connection errors.
///
/// The serial dispatch queue is what makes single-resume safe:
/// stateUpdateHandler + asyncAfter(timeout) both run on it, so
/// the resumed flag is accessed without any cross-thread races.
internal func probeRelayRTT(hostname: String, port: Int) async -> Result<TimeInterval, Error> {
    let host = NWEndpoint.Host(hostname)
    // UInt16(exactly:) returns nil on overflow rather than trapping
    // — a future caller passing a parsed-from-JSON port that
    // exceeds 65535 must surface as a probe failure, not a crash.
    guard let port16 = UInt16(exactly: port),
          let portNum = NWEndpoint.Port(rawValue: port16) else {
        return .failure(RelayPickerError.invalidPort(port))
    }
    let conn = NWConnection(host: host, port: portNum, using: .tcp)
    // Per-probe serial queue. Hand-rolled label aids os_log
    // attribution if a future deadlock investigation needs to
    // grep for which probe queue is stuck.
    let queue = DispatchQueue(label: "dev.hanfour.bamboo.relay-probe.\(hostname)")
    let start = Date()

    // ProbeGate wraps the single-resume flag in a reference type
    // so closures captured by the serial queue can share state
    // without tripping Swift 6's sendable-capture rules. All
    // mutation happens on `queue`, which is serial — the
    // @unchecked Sendable conformance reflects that.
    final class ProbeGate: @unchecked Sendable {
        var resumed = false
    }
    let gate = ProbeGate()

    return await withCheckedContinuation { cont in
        queue.asyncAfter(deadline: .now() + RelayPicker.probeTimeout) {
            if !gate.resumed {
                gate.resumed = true
                conn.cancel()
                cont.resume(returning: .failure(RelayPickerError.timeout))
            }
        }
        conn.stateUpdateHandler = { state in
            switch state {
            case .ready:
                if !gate.resumed {
                    gate.resumed = true
                    let rtt = Date().timeIntervalSince(start)
                    conn.cancel()
                    cont.resume(returning: .success(rtt))
                }
            case .failed(let err):
                if !gate.resumed {
                    gate.resumed = true
                    conn.cancel()
                    cont.resume(returning: .failure(err))
                }
            case .waiting(let err):
                // NWConnection enters .waiting when the path is
                // temporarily unavailable (cellular off, transient
                // DNS fail). For a fast-bring-up probe we treat
                // this as a failure rather than waiting indefinitely
                // — the timeout would catch it anyway, but failing
                // fast lets a healthy relay win sooner.
                if !gate.resumed {
                    gate.resumed = true
                    conn.cancel()
                    cont.resume(returning: .failure(err))
                }
            default:
                break
            }
        }
        conn.start(queue: queue)
    }
}

internal enum RelayPickerError: Error, CustomStringConvertible {
    case timeout
    case invalidPort(Int)

    public var description: String {
        switch self {
        case .timeout:                return "RelayPicker: probe timed out"
        case .invalidPort(let p):     return "RelayPicker: invalid port \(p)"
        }
    }
}
