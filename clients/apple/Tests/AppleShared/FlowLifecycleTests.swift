// SPDX-License-Identifier: Apache-2.0

import XCTest

/// Tests for FlowLifecycle — the pure bookkeeping that fixes the
/// DNSProxyProvider write-after-close race. A DNS proxy flow must keep
/// its write half open until every in-flight async response has been
/// written, then close exactly once. Before this, readLoop's read-EOF
/// closed the write half immediately, so the deferred 2-stage DNS64
/// synthesis write lost the race and failed with "flow is not
/// connected" (the single-round-trip A path usually won, which is why
/// A resolved but AAAA synthesis silently never did).
final class FlowLifecycleTests: XCTestCase {

    /// Read closes with nothing in flight → close the write half now.
    func testCloseImmediatelyWhenNothingInFlight() {
        let lc = FlowLifecycle()
        XCTAssertTrue(lc.markReadClosed(), "no in-flight op → close now")
    }

    /// Read closes while a response is still in flight → defer; the
    /// draining op triggers the close.
    func testDeferUntilInFlightDrains() {
        let lc = FlowLifecycle()
        lc.begin()
        XCTAssertFalse(lc.markReadClosed(), "in-flight op present → must NOT close yet")
        XCTAssertTrue(lc.end(), "last op drained after read closed → close now")
    }

    /// The close signal must fire EXACTLY once — a second terminal event
    /// after the close must not re-signal (guards double closeWrite/release).
    func testCloseSignalsExactlyOnce() {
        let lc = FlowLifecycle()
        lc.begin()
        XCTAssertFalse(lc.markReadClosed())
        XCTAssertTrue(lc.end(), "first drain closes")
        XCTAssertFalse(lc.markReadClosed(), "already closed → no second signal")
    }

    /// Multiple concurrent responses: only the LAST drain (after read
    /// closed) signals the close.
    func testMultipleInFlightOnlyLastDrainCloses() {
        let lc = FlowLifecycle()
        lc.begin()
        lc.begin()
        XCTAssertFalse(lc.markReadClosed(), "2 in flight → defer")
        XCTAssertFalse(lc.end(), "1 still in flight → defer")
        XCTAssertTrue(lc.end(), "last drained → close now")
    }

    /// While the read half is still open, a completing op must NOT close
    /// the write half — more queries may yet arrive on the same flow.
    func testDoNotCloseWhileReadStillOpen() {
        let lc = FlowLifecycle()
        lc.begin()
        XCTAssertFalse(lc.end(), "read still open → keep write half open for more queries")
    }

    /// Ops can begin+drain before the read EOF; the eventual read-close
    /// (with nothing in flight) is what closes the write half.
    func testReadCloseAfterOpsDrainedClosesOnce() {
        let lc = FlowLifecycle()
        lc.begin()
        XCTAssertFalse(lc.end(), "read open → defer")
        XCTAssertTrue(lc.markReadClosed(), "read closes, nothing in flight → close now")
    }
}
