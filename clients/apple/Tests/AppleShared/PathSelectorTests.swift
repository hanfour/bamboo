// SPDX-License-Identifier: Apache-2.0

import XCTest

final class PathSelectorTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_000_000)
    private let direct = "203.0.113.5:51820"
    private let lan = "192.168.1.9:51820"

    private func at(_ s: TimeInterval) -> Date { t0.addingTimeInterval(s) }

    /// A selector with the shared grid off and the old retry ladder, so the
    /// tests below can use plain offsets.
    private func plainSelector() -> PathSelector {
        var t = PathSelector.Tuning()
        t.grid = 0
        t.probeTimeout = 8
        t.backoff = [30, 120, 600, 1800]
        return PathSelector(tuning: t)
    }

    private func obs(_ endpoint: String?, handshake: TimeInterval? = nil, rx: UInt64 = 0) -> WGPeerObservation {
        WGPeerObservation(endpoint: endpoint, lastHandshake: handshake.map(at), rxBytes: rx)
    }

    func testStartsOnRelayThenProbes() {
        var sel = plainSelector()
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: nil, now: at(0)), .relay)
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: nil, now: at(2)), .relay)
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: nil, now: at(3)), .direct(direct))
    }

    func testNoCandidatesStaysOnRelay() {
        var sel = plainSelector()
        for s in stride(from: 0.0, to: 60, by: 5) {
            XCTAssertEqual(sel.path(peer: "a", candidates: [], observation: nil, now: at(s)), .relay)
        }
    }

    func testProbeSucceedsOnHandshakeAtCandidate() {
        var sel = plainSelector()
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(0))
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(3))   // probing from 3
        // Endpoint matches but the handshake is older than the switch.
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 1), now: at(5)), .direct(direct))
        // Fresh handshake at the candidate: promoted, then stays.
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 4, rx: 100), now: at(6)), .direct(direct))
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 4, rx: 200), now: at(20)), .direct(direct))
    }

    func testProbeFailsWhenWireGuardRoamsBackToRelay() {
        var sel = plainSelector()
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(0))
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(3))
        let proxy = "127.0.0.1:60000"
        // The handshake finished, but through the relay proxy.
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs(proxy, handshake: 5), now: at(6)), .direct(direct))
        // Timeout (8s after the probe began at 3).
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs(proxy, handshake: 5), now: at(12)), .relay)
    }

    func testBackoffGrowsAndCapsAfterRepeatedFailures() {
        var sel = plainSelector()
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
        var sel = plainSelector()
        _ = sel.path(peer: "a", candidates: [lan, direct], observation: nil, now: at(0))
        XCTAssertEqual(sel.path(peer: "a", candidates: [lan, direct], observation: nil, now: at(3)), .direct(lan))
        XCTAssertEqual(sel.path(peer: "a", candidates: [lan, direct], observation: nil, now: at(12)), .direct(direct))
        XCTAssertEqual(sel.path(peer: "a", candidates: [lan, direct], observation: obs(direct, handshake: 13, rx: 1), now: at(14)), .direct(direct))
    }

    func testDirectFallsBackWhenEndpointRoamsAway() {
        var sel = plainSelector()
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(0))
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(3))
        _ = sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 4, rx: 10), now: at(5))
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs("127.0.0.1:60000", handshake: 4, rx: 20), now: at(30)), .relay)
    }

    func testDirectFallsBackWhenInboundTrafficStops() {
        var sel = plainSelector()
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(0))
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(3))
        _ = sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 4, rx: 10), now: at(5))
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 4, rx: 10), now: at(40)), .direct(direct))
        XCTAssertEqual(sel.path(peer: "a", candidates: [direct], observation: obs(direct, handshake: 4, rx: 10), now: at(51)), .relay)
    }

    func testStableDirectResetsBackoffAfterLateFailure() {
        var sel = plainSelector()
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
        var sel = plainSelector()
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(0))
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(3))
        _ = sel.path(peer: "a", candidates: [direct], observation: nil, now: at(12))   // failed, backing off 30s
        let moved = "198.51.100.7:51820"
        XCTAssertEqual(sel.path(peer: "a", candidates: [moved], observation: nil, now: at(13)), .relay)
        XCTAssertEqual(sel.path(peer: "a", candidates: [moved], observation: nil, now: at(16)), .direct(moved))
    }

    func testPeersAreIndependentAndRetainDropsGoneOnes() {
        var sel = plainSelector()
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

extension PathSelectorTests {
    func testProbesStartOnTheSharedGridSoBothEndsOverlap() {
        // Two peers whose clocks differ by 2s and who began at different
        // moments still start probing in the same 30s slot.
        var a = PathSelector()
        var b = PathSelector()
        let base = Date(timeIntervalSince1970: 3_000_000)   // a multiple of 30
        let aStart = base.addingTimeInterval(4)
        let bStart = base.addingTimeInterval(17)
        _ = a.path(peer: "p", candidates: ["203.0.113.5:1"], observation: nil, now: aStart)
        _ = b.path(peer: "p", candidates: ["203.0.113.5:1"], observation: nil, now: bStart)
        let slot = base.addingTimeInterval(30)
        XCTAssertEqual(a.path(peer: "p", candidates: ["203.0.113.5:1"], observation: nil, now: slot.addingTimeInterval(-1)), .relay)
        XCTAssertEqual(b.path(peer: "p", candidates: ["203.0.113.5:1"], observation: nil, now: slot.addingTimeInterval(-1)), .relay)
        XCTAssertEqual(a.path(peer: "p", candidates: ["203.0.113.5:1"], observation: nil, now: slot), .direct("203.0.113.5:1"))
        XCTAssertEqual(b.path(peer: "p", candidates: ["203.0.113.5:1"], observation: nil, now: slot.addingTimeInterval(1)), .direct("203.0.113.5:1"))
    }

    func testThreeRetriesOnTheNextSlotBeforeBackingOff() {
        var sel = PathSelector()
        let c = ["203.0.113.5:1"]
        var now = Date(timeIntervalSince1970: 3_000_000)
        _ = sel.path(peer: "p", candidates: c, observation: nil, now: now)
        var starts: [TimeInterval] = []
        for _ in 0..<5 {
            while sel.path(peer: "p", candidates: c, observation: nil, now: now) == .relay { now = now.addingTimeInterval(1) }
            starts.append(now.timeIntervalSince1970)
            now = now.addingTimeInterval(13)   // past the 12s probe timeout
            XCTAssertEqual(sel.path(peer: "p", candidates: c, observation: nil, now: now), .relay)
        }
        let gaps = zip(starts.dropFirst(), starts).map { $0 - $1 }
        XCTAssertEqual(Array(gaps.prefix(3)), [30, 30, 30])
        // Then it moves to the 2-minute grid: the 4th start is on it, and
        // arrives between 2 and 3 minutes after the 3rd.
        XCTAssertEqual(starts[4].truncatingRemainder(dividingBy: 120), 0)
        XCTAssertTrue((120...180).contains(gaps[3]), "gap \(gaps[3])")
    }
}

extension PathSelectorTests {
    func testExistingSessionIsPromotedOnInboundBytesWithoutANewHandshake() {
        var sel = plainSelector()
        let c = ["203.0.113.5:51820"]
        _ = sel.path(peer: "a", candidates: c, observation: obs("127.0.0.1:1", handshake: -50, rx: 100), now: at(0))
        // The probe starts; the old handshake is far older than the switch.
        XCTAssertEqual(sel.path(peer: "a", candidates: c, observation: obs("127.0.0.1:1", handshake: -50, rx: 100), now: at(3)), .direct(c[0]))
        // Endpoint is ours but nothing has arrived yet.
        XCTAssertEqual(sel.path(peer: "a", candidates: c, observation: obs(c[0], handshake: -50, rx: 100), now: at(5)), .direct(c[0]))
        // Bytes arrived with the endpoint still at the candidate: promoted.
        XCTAssertEqual(sel.path(peer: "a", candidates: c, observation: obs(c[0], handshake: -50, rx: 180), now: at(6)), .direct(c[0]))
        XCTAssertEqual(sel.path(peer: "a", candidates: c, observation: obs(c[0], handshake: -50, rx: 260), now: at(40)), .direct(c[0]))
    }

    func testInboundBytesViaRelayDoNotPromote() {
        var sel = plainSelector()
        let c = ["203.0.113.5:51820"]
        _ = sel.path(peer: "a", candidates: c, observation: obs("127.0.0.1:1", rx: 100), now: at(0))
        _ = sel.path(peer: "a", candidates: c, observation: obs("127.0.0.1:1", rx: 100), now: at(3))
        // WireGuard roamed back to the relay proxy while bytes kept arriving.
        XCTAssertEqual(sel.path(peer: "a", candidates: c, observation: obs("127.0.0.1:1", rx: 400), now: at(6)), .direct(c[0]))
        XCTAssertEqual(sel.path(peer: "a", candidates: c, observation: obs("127.0.0.1:1", rx: 500), now: at(12)), .relay)
    }
}

extension PathSelectorTests {
    func testEndsWithDifferentFailureCountsMeetOnTheTwoMinuteGrid() {
        let c = ["203.0.113.5:1"]
        func nextProbe(failures: Int) -> TimeInterval {
            var sel = PathSelector()
            var now = Date(timeIntervalSince1970: 3_000_000)
            _ = sel.path(peer: "p", candidates: c, observation: nil, now: now)
            var t: [TimeInterval] = []
            for _ in 0...failures {
                while sel.path(peer: "p", candidates: c, observation: nil, now: now) == .relay { now = now.addingTimeInterval(1) }
                t.append(now.timeIntervalSince1970)
                now = now.addingTimeInterval(13)
                _ = sel.path(peer: "p", candidates: c, observation: nil, now: now)
            }
            // Where its next probe lands.
            while sel.path(peer: "p", candidates: c, observation: nil, now: now) == .relay { now = now.addingTimeInterval(1) }
            return now.timeIntervalSince1970
        }
        // After 3 and after 4 failures the waits are 120s and 600s: both land on
        // a multiple of 120s, so the slow end's next attempt is one the fast end hits too.
        XCTAssertEqual(nextProbe(failures: 3).truncatingRemainder(dividingBy: 120), 0)
        XCTAssertEqual(nextProbe(failures: 4).truncatingRemainder(dividingBy: 120), 0)
        XCTAssertEqual(nextProbe(failures: 5).truncatingRemainder(dividingBy: 120), 0)
    }
}
