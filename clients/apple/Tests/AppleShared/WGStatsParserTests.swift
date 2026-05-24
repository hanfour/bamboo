// SPDX-License-Identifier: Apache-2.0

import XCTest

/// Tests for `WGStatsParser.sumBytes` which aggregates the cumulative
/// wg byte counters from the wg-uapi config string returned by
/// `WireGuardAdapter.getRuntimeConfiguration`. Mirrors the CLI-side
/// `device.SumBytes` test in `clients/core/device/stats_test.go`.
///
/// Return-tuple order (sent, received) matches the controller's
/// `(bytesSent, bytesReceived)` wire pair — a flip would silently
/// report send as receive on the dashboard, so we cover the
/// asymmetric "tx-only peer" case explicitly.
final class WGStatsParserTests: XCTestCase {

    func testEmptyInput() {
        let (s, r) = WGStatsParser.sumBytes("")
        XCTAssertEqual(s, 0)
        XCTAssertEqual(r, 0)
    }

    func testSinglePeer() {
        let config = """
        private_key=0000000000000000000000000000000000000000000000000000000000000000
        listen_port=51820
        public_key=PEER1PUBKEYBASE64BASE64BASE64BASE64BASE64BAS=
        endpoint=10.0.0.1:51820
        tx_bytes=1000
        rx_bytes=500
        last_handshake_time_sec=1735689600
        """
        let (s, r) = WGStatsParser.sumBytes(config)
        XCTAssertEqual(s, 1000)
        XCTAssertEqual(r, 500)
    }

    func testMultiplePeersSum() {
        let config = """
        private_key=0000000000000000000000000000000000000000000000000000000000000000
        listen_port=51820
        public_key=PEER1PUBKEYBASE64BASE64BASE64BASE64BASE64BAS=
        endpoint=10.0.0.1:51820
        tx_bytes=1000
        rx_bytes=100
        public_key=PEER2PUBKEYBASE64BASE64BASE64BASE64BASE64BAS=
        endpoint=10.0.0.2:51820
        tx_bytes=2000
        rx_bytes=200
        public_key=PEER3PUBKEYBASE64BASE64BASE64BASE64BASE64BAS=
        endpoint=10.0.0.3:51820
        tx_bytes=4000
        rx_bytes=400
        """
        let (s, r) = WGStatsParser.sumBytes(config)
        XCTAssertEqual(s, 7000)
        XCTAssertEqual(r, 700)
    }

    /// One-way traffic — covers the symmetry between tx and rx so a
    /// future refactor that flips the return order trips this
    /// assertion instead of going silently wrong.
    func testTxOnlyAndRxOnlyPeers() {
        let config = """
        public_key=A=
        tx_bytes=999
        rx_bytes=0
        public_key=B=
        tx_bytes=0
        rx_bytes=111
        """
        let (s, r) = WGStatsParser.sumBytes(config)
        XCTAssertEqual(s, 999)
        XCTAssertEqual(r, 111)
    }

    /// Lines without an `=` (e.g. a stray blank line, or a key-only
    /// future-extension field) must be ignored, not crash and not
    /// be summed as zero into the wrong key.
    func testMalformedLinesIgnored() {
        let config = """
        public_key=A=
        garbage-without-equals
        tx_bytes=100
        rx_bytes=50

        """
        let (s, r) = WGStatsParser.sumBytes(config)
        XCTAssertEqual(s, 100)
        XCTAssertEqual(r, 50)
    }

    /// Non-numeric value on a tx_bytes line shouldn't crash —
    /// belt-and-braces in case the wg-uapi format ever introduces
    /// a sentinel like `unknown` (it doesn't today, but the
    /// parser should be permissive).
    func testNonNumericValueSkipped() {
        let config = """
        public_key=A=
        tx_bytes=notanumber
        rx_bytes=42
        """
        let (s, r) = WGStatsParser.sumBytes(config)
        XCTAssertEqual(s, 0)
        XCTAssertEqual(r, 42)
    }

    /// CRLF line endings — wg-uapi uses LF on Apple platforms but
    /// the parser strips trailing CR defensively so a Windows-side
    /// recording or a hand-pasted test fixture doesn't break it.
    func testCRLFLineEndings() {
        let config = "public_key=A=\r\ntx_bytes=10\r\nrx_bytes=20\r\n"
        let (s, r) = WGStatsParser.sumBytes(config)
        XCTAssertEqual(s, 10)
        XCTAssertEqual(r, 20)
    }
}
