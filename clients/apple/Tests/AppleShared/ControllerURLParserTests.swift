// SPDX-License-Identifier: Apache-2.0

import XCTest

/// Tests for the `parseControllerURL` helper used by signInAsync,
/// connectAsync, and refreshPolicyFromController to validate the
/// user-typed `controllerURL` Settings field.
///
/// Mirrors the contract documented at the helper's site:
///   - trims whitespace + newlines
///   - requires http or https scheme (case-insensitive)
///   - returns nil for empty, malformed, or wrong-scheme inputs
final class ControllerURLParserTests: XCTestCase {

    // MARK: - happy path

    func testValidHTTPS() {
        let url = parseControllerURL("https://controller.example.com")
        XCTAssertEqual(url?.absoluteString, "https://controller.example.com")
    }

    func testValidHTTP() {
        // http is also accepted — useful for dev / on-prem deployments
        // behind a tls-terminating proxy. Production deploys should
        // still use https.
        let url = parseControllerURL("http://localhost:8080")
        XCTAssertEqual(url?.absoluteString, "http://localhost:8080")
    }

    func testSchemeIsCaseInsensitive() {
        // RFC 3986 says scheme matching is case-insensitive. Verify
        // both common typo forms work.
        XCTAssertNotNil(parseControllerURL("HTTPS://controller.example.com"))
        XCTAssertNotNil(parseControllerURL("Http://localhost:8080"))
    }

    // MARK: - whitespace trimming

    func testTrimsLeadingTrailingWhitespace() {
        let url = parseControllerURL("  https://controller.example.com  ")
        XCTAssertEqual(url?.absoluteString, "https://controller.example.com")
    }

    func testTrimsTrailingNewline() {
        // macOS 26 URL(string:) rejects trailing \n outright, so this
        // exercises the trim before parse rather than the parser's
        // built-in tolerance.
        let url = parseControllerURL("https://controller.example.com\n")
        XCTAssertEqual(url?.absoluteString, "https://controller.example.com")
    }

    // MARK: - nil cases

    func testEmptyReturnsNil() {
        XCTAssertNil(parseControllerURL(""))
    }

    func testWhitespaceOnlyReturnsNil() {
        XCTAssertNil(parseControllerURL("   \n\t  "))
    }

    func testNoSchemeReturnsNil() {
        // "abc" parses to a URL with scheme==nil; the guard catches it
        // so it doesn't pass through to URLSession and fail with an
        // opaque error far from the typo.
        XCTAssertNil(parseControllerURL("controller.example.com"))
        XCTAssertNil(parseControllerURL("abc"))
    }

    func testNonHTTPSchemeReturnsNil() {
        // ws/wss are valid URL schemes but not what URLSession dials
        // for the controller; ftp/file similarly meaningless here.
        XCTAssertNil(parseControllerURL("ws://controller.example.com"))
        XCTAssertNil(parseControllerURL("wss://controller.example.com"))
        XCTAssertNil(parseControllerURL("ftp://controller.example.com"))
        XCTAssertNil(parseControllerURL("file:///etc/passwd"))
    }

    func testMalformedURLReturnsNil() {
        // Triggers URL(string:) returning nil even after the trim.
        // The space inside a string with no scheme is one such case.
        XCTAssertNil(parseControllerURL("not a url"))
    }
}
