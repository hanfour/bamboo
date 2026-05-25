// SPDX-License-Identifier: Apache-2.0

import XCTest

/// Tests for `PeerWatcher.decode` — the SSE wire-shape mapping
/// between the controller's named events and the Apple-side
/// typed enum. Driving a real SSE stream from a unit test would
/// need a URLProtocol stub; hitting decode in isolation is
/// cheaper and pins the same contract that matters when the
/// controller's wire shape changes.
final class PeerWatcherTests: XCTestCase {

    func testDecodeRelaysChangedHappyPath() throws {
        // Wire shape mirrors the controller's writeSSE case for
        // RelaysChanged (apps/controller/internal/server/api_peers.go).
        // Two-relay payload exercises both fields + array ordering;
        // a single-entry test would let an array→single-element
        // bug pass silently.
        let payload = """
        {
          "relayServers": [
            {"id":"r1","region":"us-east-1","hostname":"a.example.com","port":8443,"publicKey":"PK_A"},
            {"id":"r2","region":"ap-northeast-1","hostname":"b.example.com","port":443,"publicKey":"PK_B"}
          ]
        }
        """
        let event = try PeerWatcher.decode(name: "relays_changed", payload: payload)
        guard case .relaysChanged(let servers) = event else {
            XCTFail("expected relaysChanged, got \(event)")
            return
        }
        XCTAssertEqual(servers.count, 2)
        XCTAssertEqual(servers[0].id, "r1")
        XCTAssertEqual(servers[0].region, "us-east-1")
        XCTAssertEqual(servers[0].hostname, "a.example.com")
        XCTAssertEqual(servers[0].port, 8443)
        XCTAssertEqual(servers[1].hostname, "b.example.com")
        XCTAssertEqual(servers[1].port, 443)
    }

    func testDecodeRelaysChangedEmptyList() throws {
        // The controller emits an empty list when the eligible set
        // collapses to zero (every relay flipped unhealthy). The
        // Apple consumer must decode this — and stage 4b's handler
        // treats it as "degrade to direct-only on next reconnect".
        // A throw here would silently lose the event.
        let event = try PeerWatcher.decode(
            name: "relays_changed",
            payload: "{\"relayServers\": []}"
        )
        guard case .relaysChanged(let servers) = event else {
            XCTFail("expected relaysChanged, got \(event)")
            return
        }
        XCTAssertEqual(servers.count, 0)
    }

    func testDecodeUnknownEventThrows() {
        // Defensive: an unknown event name from a newer controller
        // build must throw rather than crash or silently fall
        // through as one of the existing cases.
        XCTAssertThrowsError(try PeerWatcher.decode(
            name: "future_event_we_dont_know",
            payload: "{}"
        ))
    }

    func testDecodePolicyChangedStillWorks() throws {
        // Regression guard — adding the relays_changed case shouldn't
        // have broken the existing dispatch table.
        let event = try PeerWatcher.decode(
            name: "policy_changed",
            payload: "{\"policyRevision\": 42}"
        )
        guard case .policyChanged(let rev) = event else {
            XCTFail("expected policyChanged, got \(event)")
            return
        }
        XCTAssertEqual(rev, 42)
    }
}
