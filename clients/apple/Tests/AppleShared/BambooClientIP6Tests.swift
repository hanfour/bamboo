// SPDX-License-Identifier: Apache-2.0

import XCTest

final class BambooClientIP6Tests: XCTestCase {
    func testPeerJSONDecodesIP6() throws {
        let json = """
        {"id":"p1","tenantId":"t1","hostname":"h","ip":"100.64.0.5",
         "ip6":"fdba:1100::6440:5","wireguardPublicKey":"k"}
        """.data(using: .utf8)!
        let p = try JSONDecoder().decode(BambooClient.PeerJSON.self, from: json)
        XCTAssertEqual(p.ip, "100.64.0.5")
        XCTAssertEqual(p.ip6, "fdba:1100::6440:5")
    }

    func testPeerJSONIP6AbsentIsNil() throws {
        let json = """
        {"id":"p1","tenantId":"t1","hostname":"h","ip":"100.64.0.5",
         "wireguardPublicKey":"k"}
        """.data(using: .utf8)!
        let p = try JSONDecoder().decode(BambooClient.PeerJSON.self, from: json)
        XCTAssertNil(p.ip6)
    }
}
