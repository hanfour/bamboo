// SPDX-License-Identifier: Apache-2.0

import XCTest

/// Decode tests for the NAT64 tenant config on the REST register response
/// (NAT64 Phase C4 PR 2). Present → decoded; absent (pre-C4 controller) → nil.
final class RegisterResponseNAT64Tests: XCTestCase {
    private let selfPeer = """
    {"id":"p1","tenantId":"t1","hostname":"h","ip":"100.64.0.5","wireguardPublicKey":"k"}
    """

    func testDecodesNAT64Config() throws {
        let json = """
        {"self":\(selfPeer),"peers":[],"policyRevision":1,
         "dns64Enabled":true,"nat64Prefix":"64:ff9b::/96"}
        """.data(using: .utf8)!
        let r = try JSONDecoder().decode(BambooClient.RegisterResponse.self, from: json)
        XCTAssertEqual(r.dns64Enabled, true)
        XCTAssertEqual(r.nat64Prefix, "64:ff9b::/96")
    }

    func testDecodesWithoutNAT64Config() throws {
        // A pre-C4 controller omits the fields → nil (DNS64 stays off).
        let json = """
        {"self":\(selfPeer),"peers":[],"policyRevision":1}
        """.data(using: .utf8)!
        let r = try JSONDecoder().decode(BambooClient.RegisterResponse.self, from: json)
        XCTAssertNil(r.dns64Enabled)
        XCTAssertNil(r.nat64Prefix)
    }
}
