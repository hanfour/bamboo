// SPDX-License-Identifier: Apache-2.0

import XCTest

/// Tests for `parseAdvertiseRoutes` used by ConnectionViewModel's
/// connect path to turn the Settings string field into a `[String]`
/// payload for `BambooClient.RegisterRequest.advertisedRoutes`.
///
/// Mirrors the CLI's `validateAdvertisedRoutes` table in
/// `clients/cli/cmd/bamboo/advertise_test.go`, plus the extra UI
/// tokenisation tests (comma / whitespace / trim).
final class AdvertiseRoutesParserTests: XCTestCase {

    // MARK: - happy path

    func testEmptyReturnsEmpty() throws {
        XCTAssertEqual(try parseAdvertiseRoutes(""), [])
        XCTAssertEqual(try parseAdvertiseRoutes("   "), [])
        XCTAssertEqual(try parseAdvertiseRoutes("\n\t"), [])
    }

    func testSingleIPv4() throws {
        XCTAssertEqual(try parseAdvertiseRoutes("10.0.0.0/24"), ["10.0.0.0/24"])
    }

    func testCommaSeparated() throws {
        let got = try parseAdvertiseRoutes("10.0.0.0/24, 192.168.86.0/24")
        XCTAssertEqual(got, ["10.0.0.0/24", "192.168.86.0/24"])
    }

    func testWhitespaceSeparated() throws {
        // Allow whitespace-only separation too — paste from a list
        // shouldn't require manual comma insertion.
        let got = try parseAdvertiseRoutes("10.0.0.0/24 192.168.86.0/24")
        XCTAssertEqual(got, ["10.0.0.0/24", "192.168.86.0/24"])
    }

    func testMixedSeparators() throws {
        let got = try parseAdvertiseRoutes("10.0.0.0/24,, 192.168.86.0/24\n10.42.0.0/16")
        XCTAssertEqual(got, ["10.0.0.0/24", "192.168.86.0/24", "10.42.0.0/16"])
    }

    func testIPv6() throws {
        XCTAssertEqual(try parseAdvertiseRoutes("2001:db8::/32"), ["2001:db8::/32"])
        XCTAssertEqual(try parseAdvertiseRoutes("::/0"), ["::/0"])
    }

    func testDefaultRoutes() throws {
        // 0.0.0.0/0 + ::/0 are the "exit node"-style defaults; clients
        // wouldn't normally type these (the dedicated exit-node toggle
        // is the right knob), but if they do the parser shouldn't trip.
        XCTAssertEqual(try parseAdvertiseRoutes("0.0.0.0/0"), ["0.0.0.0/0"])
    }

    func testPreservesOrder() throws {
        let got = try parseAdvertiseRoutes("10.0.3.0/24, 10.0.1.0/24, 10.0.2.0/24")
        XCTAssertEqual(got, ["10.0.3.0/24", "10.0.1.0/24", "10.0.2.0/24"])
    }

    // MARK: - invalid

    func testInvalidNoPrefix() {
        XCTAssertThrowsError(try parseAdvertiseRoutes("10.0.0.0")) { err in
            XCTAssertTrue(String(describing: err).contains("10.0.0.0"))
        }
    }

    func testInvalidPrefixOutOfRange() {
        // 33 is past IPv4 max (32). IPv4Address parses the address part
        // but the prefix bound check rejects.
        XCTAssertThrowsError(try parseAdvertiseRoutes("10.0.0.0/33"))
    }

    func testInvalidIPv6PrefixOutOfRange() {
        XCTAssertThrowsError(try parseAdvertiseRoutes("2001:db8::/129"))
    }

    func testInvalidGarbage() {
        XCTAssertThrowsError(try parseAdvertiseRoutes("not-a-cidr"))
    }

    func testFirstBadTokenStops() {
        // Mixed input: the parser bails on the first malformed token
        // so the caller surfaces the offending value verbatim, not a
        // half-parsed partial list.
        XCTAssertThrowsError(try parseAdvertiseRoutes("10.0.0.0/24, bad-token, 192.168.0.0/24")) { err in
            XCTAssertTrue(String(describing: err).contains("bad-token"))
        }
    }
}
