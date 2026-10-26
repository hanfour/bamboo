// SPDX-License-Identifier: Apache-2.0

import Foundation

/// What WireGuard reports about one peer. Parsed from the UAPI dump.
public struct WGPeerObservation: Equatable {
    public var endpoint: String?
    public var lastHandshake: Date?
    public var rxBytes: UInt64

    public init(endpoint: String?, lastHandshake: Date?, rxBytes: UInt64) {
        self.endpoint = endpoint
        self.lastHandshake = lastHandshake
        self.rxBytes = rxBytes
    }
}

/// Which way a peer's packets should go.
public enum PeerPath: Equatable {
    case relay
    /// Send to this host:port directly. Also returned while a candidate is
    /// still being tried.
    case direct(String)
}

/// PathSelector decides, per peer, whether to keep using the relay or try
/// a direct address, and falls back when the direct one stops working.
///
/// Every peer starts on the relay, which always works. After a short wait
/// the selector tries each candidate address in turn by pointing the
/// peer's WireGuard endpoint at it. WireGuard roams: when an authenticated
/// packet arrives from a new source it adopts that source as the endpoint.
/// So a candidate has worked only when a handshake finished after the
/// switch and the endpoint is still that candidate. If the peer keeps
/// answering through the relay, WireGuard roams back to the relay proxy on
/// its own, and the endpoint no longer matches.
///
/// A failed candidate costs the traffic sent to it until WireGuard roams
/// back, so failures back off: 30s, 2m, 10m, 30m.
public struct PathSelector {
    public struct Tuning {
        public var firstProbeDelay: TimeInterval = 3
        public var probeTimeout: TimeInterval = 8
        /// A direct path must show inbound bytes this often (peers send a
        /// keepalive every 25s).
        public var rxWindow: TimeInterval = 45
        public var backoff: [TimeInterval] = [30, 120, 600, 1800]
        /// Time on a direct path after which a later failure starts the
        /// backoff over.
        public var stableAfter: TimeInterval = 300
        public init() {}
    }

    private enum Phase {
        case relay(nextProbeAt: Date)
        case probing(index: Int, startedAt: Date)
        case direct(index: Int, since: Date, rx: UInt64, rxAt: Date)
    }

    private struct State {
        var phase: Phase
        var failures: Int
        var candidates: [String]
    }

    public var tuning: Tuning
    private var peers: [String: State] = [:]

    public init(tuning: Tuning = Tuning()) {
        self.tuning = tuning
    }

    /// The path to use for `peer` right now, and the next state. Call it on
    /// a timer and again whenever the config changes. `candidates` are the
    /// peer's direct addresses in the order to try them.
    public mutating func path(peer: String,
                              candidates: [String],
                              observation: WGPeerObservation?,
                              now: Date) -> PeerPath {
        guard !candidates.isEmpty else {
            peers.removeValue(forKey: peer)
            return .relay
        }
        var st = peers[peer] ?? State(phase: .relay(nextProbeAt: now.addingTimeInterval(tuning.firstProbeDelay)),
                                      failures: 0, candidates: candidates)
        // A new candidate list restarts the attempt, so a peer that moved
        // networks is retried at once instead of after a long backoff.
        if st.candidates != candidates {
            st = State(phase: .relay(nextProbeAt: now.addingTimeInterval(tuning.firstProbeDelay)),
                       failures: 0, candidates: candidates)
        }
        defer { peers[peer] = st }

        switch st.phase {
        case .relay(let nextProbeAt):
            guard now >= nextProbeAt else { return .relay }
            st.phase = .probing(index: 0, startedAt: now)
            return .direct(candidates[0])

        case .probing(let index, let startedAt):
            let candidate = candidates[index]
            if let obs = observation, obs.endpoint == candidate,
               let hs = obs.lastHandshake, hs >= startedAt {
                st.phase = .direct(index: index, since: now, rx: obs.rxBytes, rxAt: now)
                return .direct(candidate)
            }
            guard now.timeIntervalSince(startedAt) > tuning.probeTimeout else { return .direct(candidate) }
            if index + 1 < candidates.count {
                st.phase = .probing(index: index + 1, startedAt: now)
                return .direct(candidates[index + 1])
            }
            return fail(&st, now: now)

        case .direct(let index, let since, let rx, let rxAt):
            let candidate = candidates[index]
            guard let obs = observation, obs.endpoint == candidate else {
                if now.timeIntervalSince(since) > tuning.stableAfter { st.failures = 0 }
                return fail(&st, now: now)
            }
            if obs.rxBytes > rx {
                st.phase = .direct(index: index, since: since, rx: obs.rxBytes, rxAt: now)
                return .direct(candidate)
            }
            if now.timeIntervalSince(rxAt) > tuning.rxWindow {
                if now.timeIntervalSince(since) > tuning.stableAfter { st.failures = 0 }
                return fail(&st, now: now)
            }
            return .direct(candidate)
        }
    }

    /// Forget peers that left the config.
    public mutating func retain(_ keys: Set<String>) {
        for k in peers.keys where !keys.contains(k) { peers.removeValue(forKey: k) }
    }

    private func fail(_ st: inout State, now: Date) -> PeerPath {
        let wait = tuning.backoff[min(st.failures, tuning.backoff.count - 1)]
        st.failures += 1
        st.phase = .relay(nextProbeAt: now.addingTimeInterval(wait))
        return .relay
    }
}
