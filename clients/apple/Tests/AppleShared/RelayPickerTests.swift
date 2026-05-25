// SPDX-License-Identifier: Apache-2.0

import XCTest

/// Tests for the Apple `RelayPicker` — §4 P2 multi-relay stage 2
/// (Apple parity to PR #193's CLI implementation). Same test
/// pattern as `clients/cli/cmd/bamboo/relay_picker_test.go`:
/// substitute the network-probe seam with a deterministic latency
/// table so the ranking logic can be pinned without timing
/// flakiness.
final class RelayPickerTests: XCTestCase {

    override func tearDown() {
        // Restore the production probe between tests so a leaked
        // seam from one case can't poison another.
        RelayPicker.probeFn = probeRelayRTT
        super.tearDown()
    }

    func testEmptyListReturnsNil() async {
        let got = await RelayPicker.pickLowestRTT(from: [])
        XCTAssertNil(got, "empty list should return nil so caller degrades to direct-only")
    }

    /// Pin the ASC sort direction. A refactor flipping to DESC
    /// would silently pick the slowest relay every time while
    /// every other test in the suite stays green — exactly the
    /// failure mode an explicit latency-ranked test catches.
    func testSelectsLowestRTT() async {
        let latencies: [String: TimeInterval] = [
            "fast.example.com": 0.005,
            "medium.example.com": 0.050,
            "slow.example.com": 0.200,
        ]
        RelayPicker.probeFn = { hostname, _ in
            guard let rtt = latencies[hostname] else {
                return .failure(RelayPickerError.timeout)
            }
            return .success(rtt)
        }

        let candidates = [
            relayServer(region: "slow", hostname: "slow.example.com"),
            relayServer(region: "fast", hostname: "fast.example.com"),
            relayServer(region: "medium", hostname: "medium.example.com"),
        ]
        let got = await RelayPicker.pickLowestRTT(from: candidates)
        XCTAssertEqual(got?.region, "fast",
            "should pick the lowest-RTT relay regardless of input order")
    }

    /// All-probes-fail must return nil (caller falls back to
    /// direct-only). Not an error type — the controller list may
    /// transiently fail-all from a flaky network and we don't
    /// want that to abort tunnel bring-up.
    func testAllFailReturnsNil() async {
        RelayPicker.probeFn = { _, _ in .failure(RelayPickerError.timeout) }
        let candidates = [
            relayServer(region: "a", hostname: "a.example.com"),
            relayServer(region: "b", hostname: "b.example.com"),
        ]
        let got = await RelayPicker.pickLowestRTT(from: candidates)
        XCTAssertNil(got, "all probes failing should return nil")
    }

    /// Partial-failure path: one bad + one good ⇒ the good wins,
    /// regardless of order. Production-relevant because controller
    /// stage 1's reaper has already filtered unhealthy rows, but
    /// a single client-to-relay path can still fail transiently.
    func testHealthyAmongFailedWins() async {
        RelayPicker.probeFn = { hostname, _ in
            if hostname == "good.example.com" { return .success(0.020) }
            return .failure(RelayPickerError.timeout)
        }
        // Bad relay listed first; picker must still find the good one.
        let candidates = [
            relayServer(region: "bad", hostname: "bad.example.com"),
            relayServer(region: "good", hostname: "good.example.com"),
        ]
        let got = await RelayPicker.pickLowestRTT(from: candidates)
        XCTAssertEqual(got?.region, "good")
    }

    func testRelayWSSURL() {
        let rs = relayServer(region: "us-east-1", hostname: "relay-east.example.com", port: 8443)
        let got = RelayPicker.relayWSSURL(rs)
        XCTAssertEqual(got?.absoluteString, "wss://relay-east.example.com:8443/relay")
    }

    /// Production probe rejects malformed ports (out of range)
    /// rather than crashing on UInt16 conversion. We pass an
    /// obviously-invalid port and assert failure rather than a
    /// hang or a crash.
    func testProbeRejectsInvalidPort() async {
        let outcome = await probeRelayRTT(hostname: "127.0.0.1", port: 1_000_000)
        switch outcome {
        case .success:
            XCTFail("invalid port should produce a failure result")
        case .failure(let err):
            XCTAssertTrue(err is RelayPickerError,
                "expected RelayPickerError, got \(err)")
        }
    }

    /// Production probe respects the timeout. We point it at the
    /// RFC 5737 documentation prefix (192.0.2.0/24) which never
    /// routes anywhere — SYN goes into the void — and assert the
    /// probe returns within a small ceiling above probeTimeout.
    /// Without this, a stuck relay could indefinitely hold the
    /// picker goroutine and stall tunnel bring-up.
    func testProbeRespectsTimeout() async {
        let start = Date()
        let outcome = await probeRelayRTT(hostname: "192.0.2.1", port: 9999)
        let elapsed = Date().timeIntervalSince(start)
        switch outcome {
        case .success:
            XCTFail("non-routable host should not return success")
        case .failure:
            break
        }
        // probeTimeout is 1.0s; allow generous headroom for
        // task-group scheduling on a busy CI runner.
        XCTAssertLessThan(elapsed, 3.0,
            "probe took \(elapsed)s, should be <3s with 1s timeout")
    }

    // MARK: - helpers

    private func relayServer(
        region: String, hostname: String, port: Int = 8443
    ) -> BambooClient.RelayServer {
        // The Decodable init requires JSON; we route through it
        // so the test exercises the same code path the production
        // wire decode uses (catches a Codable change that would
        // silently break field mapping).
        let json = """
        {
            "id": "00000000-0000-0000-0000-000000000000",
            "region": "\(region)",
            "hostname": "\(hostname)",
            "port": \(port),
            "publicKey": "stub"
        }
        """.data(using: .utf8)!
        return try! JSONDecoder().decode(BambooClient.RelayServer.self, from: json)
    }
}
