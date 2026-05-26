// SPDX-License-Identifier: Apache-2.0

import XCTest

// Note: Shared/BundleVersion.swift is compiled directly into the
// AppleSharedTests target (per project.yml), so `BundleVersion` is
// visible at the module level without an @testable import. Same
// pattern as MagicDNSPeerStoreTests / AdvertiseRoutesParserTests.

/// BundleVersionTests pins the contract used by the register +
/// refresh paths in ConnectionViewModel — namely that the value
/// sent as `clientVersion` is non-empty even when the host bundle
/// has no `CFBundleShortVersionString` key.
///
/// An empty `clientVersion` to the controller looks like "I don't
/// know what version I am", which the controller treats as a
/// different signal than "I'm prehistoric" and would suppress the
/// upgrade-indicator badge entirely. The "0.0.0" fallback keeps
/// the signal correctly classified.
final class BundleVersionTests: XCTestCase {

    func testFallbackIsNonEmpty() {
        // Empty info-dict exercises the fallback path; the real
        // Bundle.main is unavailable to plain xctest unit tests.
        XCTAssertEqual(BundleVersion.read(from: [:]), "0.0.0")
    }

    func testReadsCFBundleShortVersionString() {
        let info: [String: Any] = ["CFBundleShortVersionString": "1.2.3"]
        XCTAssertEqual(BundleVersion.read(from: info), "1.2.3")
    }

    func testIgnoresWrongType() {
        // CFBundleShortVersionString is documented String; treat
        // any other type as missing rather than coerce.
        let info: [String: Any] = ["CFBundleShortVersionString": 1.23]
        XCTAssertEqual(BundleVersion.read(from: info), "0.0.0")
    }

    func testEmptyStringFallsBackToSentinel() {
        let info: [String: Any] = ["CFBundleShortVersionString": ""]
        XCTAssertEqual(BundleVersion.read(from: info), "0.0.0")
    }
}
