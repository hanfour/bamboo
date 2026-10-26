// SPDX-License-Identifier: Apache-2.0

import XCTest

final class PathSelectorTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_000_000)
    private let direct = "203.0.113.5:51820"
    private let lan = "192.168.1.9:51820"

    private func at(_ s: TimeInterval) -> Date { t0.addingTimeInterval(s) }

    private func obs(_ endpoint: String?, handshake: TimeInterval? = nil, rx: UInt64 = 0) -> WGPeerObservation {
        WGPeerObservation(endpoint: endpoint, lastHandshake: handshake.map(at), rxBytes: rx)
    }

    func testStartsOnRelayThenProbes() {
        var sel = PathSelector()
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: nil, now: at(0)), .relay)
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: nil, now: at(2)), .relay)
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: nil, now: at(3)), .direct(direct))
    }

    func testNoCandidatesStaysOnRelay() {
        var sel = PathSelector()
        for s in stride(from: 0.0, to: 60, by: 5) {
            XCTAssertEqual(sel.path(peer: "a", candidates: [], observation: nil, now: at(s)), .relay)
        }
    }

    func testProbeSucceedsOnHandshakeAtCandidate() {
        var sel = PathSelector()
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(0))
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(3))   // probing from 3
        // Endpoint matches but the handshake is older than the switch.
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 1), now: at(5)), .direct(direct))
        // Fresh handshake at the candidate: promoted, then stays.
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 4, rx: 100), now: at(6)), .direct(direct))
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 4, rx: 200), now: at(20)), .direct(direct))
    }

    func testProbeFailsWhenWireGuardRoamsBackToRelay() {
        var sel = PathSelector()
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(0))
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(3))
        let proxy = "127.0.0.1:60000"
        // The handshake finished, but through the relay proxy.
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs(proxy, handshake: 5), now: at(6)), .direct(direct))
        // Timeout (8s after the probe began at 3).
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs(proxy, handshake: 5), now: at(12)), .relay)
    }

    func testBackoffGrowsAndCapsAfterRepeatedFailures() {
        var sel = PathSelector()
        var now: TimeInterval = 0
        var waits: [TimeInterval] = []
        for _ in 0..<6 {
            // Wait until the probe starts.
            var p = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(now))
            let waitStart = now
            while p == .relay {
                now += 1
                p = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(now))
            }
            waits.append(now - waitStart)
            // Let the probe time out.
            now += 9
            XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: nil, now: at(now)), .relay)
        }
        XCTAssertEqual(waits[0], 3)
        XCTAssertEqual(Array(waits[1...4]), [30, 120, 600, 1800])
        XCTAssertEqual(waits[5], 1800)
    }

    func testTriesNextCandidateBeforeGivingUp() {
        var sel = PathSelector()
        _ = sel.path(peer: "a", candidates: [lan, direct], observation: nil, now: at(0))
        XCTAssertEqual(sel.path(peer: "a", candidates: [lan, direct], observation: nil, now: at(3)), .direct(lan))
        XCTAssertEqual(sel.path(peer: "a", candidates: [lan, direct], observation: nil, now: at(12)), .direct(direct))
        XCTAssertEqual(sel.path(peer: "a", candidates: [lan, direct], observation: obs(direct, handshake: 13, rx: 1), now: at(14)), .direct(direct))
    }

    func testDirectFallsBackWhenEndpointRoamsAway() {
        var sel = PathSelector()
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(0))
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(3))
        _ = sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 4, rx: 10), now: at(5))
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs("127.0.0.1:60000", handshake: 4, rx: 20), now: at(30)), .relay)
    }

    func testDirectFallsBackWhenInboundTrafficStops() {
        var sel = PathSelector()
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(0))
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(3))
        _ = sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 4, rx: 10), now: at(5))
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 4, rx: 10), now: at(40)), .direct(direct))
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 4, rx: 10), now: at(51)), .relay)
    }

    func testStableDirectResetsBackoffAfterLateFailure() {
        var sel = PathSelector()
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(0))
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(3))
        _ = sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 4, rx: 10), now: at(5))
        // Healthy for over 5 minutes, then the endpoint is lost.
        _ = sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 4, rx: 20), now: at(400))
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs("127.0.0.1:1", handshake: 4, rx: 20), now: at(401)), .relay)
        // First backoff step (30s), not a later one.
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: nil, now: at(430)), .relay)
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: nil, now: at(432)), .direct(direct))
    }

    func testChangedCandidatesRestartTheAttempt() {
        var sel = PathSelector()
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(0))
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(3))
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(12))   // failed, backing off 30s
        let moved = "198.51.100.7:51820"
        XCTAssertEqual(sel.path(peer: "a", candidates: [moved], observation: nil, now: at(13)), .relay)
        XCTAssertEqual(sel.path(peer: "a", candidates: [moved], observation: nil, now: at(16)), .direct(moved))
    }

    func testPeersAreIndependentAndRetainDropsGoneOnes() {
        var sel = PathSelector()
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(0))
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: nil, now: at(3)), .direct(direct))
        XCTAssertEqual(sel.path(peer: "b", candidates: [direct], observation: nil, now: at(3)), .relay)
        sel.retain(["b"])
        // "a" lost its state: it starts over on the relay.
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: nil, now: at(4)), .relay)
    }

    func testObservationsParseHexKeysEndpointHandshakeAndRx() {
        let key = Data(repeating: 0xAB, count: 32)
        let hex = String(repeating: "ab", count: 32)
        let dump = """
        private_key=00
        listen_port=51820
        public_key=\(hex)
        endpoint=203.0.113.5:51820
        last_handshake_time_sec=1700000000
        last_handshake_time_nsec=5
        tx_bytes=10
        rx_bytes=77
        public_key=\(String(repeating: "cd", count: 32))
        endpoint=127.0.0.1:60000
        last_handshake_time_sec=0
        rx_bytes=0
        """
        let o = WGStatsParser.observations(dump)
        XCTAssertEqual(o.count, 2)
        let a = o[key.base64EncodedString()]
        XCTAssertEqual(a?.endpoint, "203.0.113.5:51820")
        XCTAssertEqual(a?.lastHandshake, Date(timeIntervalSince1970: 1_700_000_000))
        XCTAssertEqual(a?.rxBytes, 77)
        let b = o[Data(repeating: 0xCD, count: 32).base64EncodedString()]
        XCTAssertNil(b?.lastHandshake)
        XCTAssertEqual(b?.endpoint, "127.0.0.1:60000")
    }
}
