// SPDX-License-Identifier: Apache-2.0

import XCTest

final class EndpointDiscoveryTests: XCTestCase {
    func testPortPreservingNATAdvertisesPublicIPWithWireGuardPort() async {
        let c = await EndpointDiscovery.candidates(wgPort: 51820, lanAddresses: ["192.168.1.9"]) { local in
            "203.0.113.5:\(local)"   // the router kept the source port
        }
        XCTAssertEqual(c, ["192.168.1.9:51820", "203.0.113.5:51820"])
    }

    func testPerFlowPortNATAdvertisesOnlyLAN() async {
        let c = await EndpointDiscovery.candidates(wgPort: 51820, lanAddresses: ["10.0.0.4"]) { local in
            "203.0.113.5:\(local &+ 7)"   // the router picked another port
        }
        XCTAssertEqual(c, ["10.0.0.4:51820"])
    }

    func testStunFailureStillReturnsLAN() async {
        struct Boom: Error {}
        let c = await EndpointDiscovery.candidates(wgPort: 51820, lanAddresses: ["10.0.0.4"]) { _ in throw Boom() }
        XCTAssertEqual(c, ["10.0.0.4:51820"])
    }

    func testNoLANAndNoSTUNIsEmpty() async {
        let c = await EndpointDiscovery.candidates(wgPort: 51820, lanAddresses: []) { _ in "garbage" }
        XCTAssertEqual(c, [])
    }

    func testPrivateCandidateOnlyReachableOnSameSlash24() {
        XCTAssertTrue(EndpointDiscovery.isReachableLAN("192.168.1.9:51820", from: ["192.168.1.108"]))
        XCTAssertFalse(EndpointDiscovery.isReachableLAN("192.168.1.9:51820", from: ["192.168.31.139"]))
        XCTAssertFalse(EndpointDiscovery.isReachableLAN("10.0.0.4:51820", from: []))
        // Public addresses are always worth a try.
        XCTAssertTrue(EndpointDiscovery.isReachableLAN("203.0.113.5:51820", from: []))
    }

    func testPrivateRanges() {
        XCTAssertTrue(EndpointDiscovery.isPrivate("10.1.2.3"))
        XCTAssertTrue(EndpointDiscovery.isPrivate("172.16.0.1"))
        XCTAssertTrue(EndpointDiscovery.isPrivate("172.31.255.1"))
        XCTAssertFalse(EndpointDiscovery.isPrivate("172.32.0.1"))
        XCTAssertTrue(EndpointDiscovery.isPrivate("192.168.0.1"))
        XCTAssertFalse(EndpointDiscovery.isPrivate("100.64.0.1"))
        XCTAssertFalse(EndpointDiscovery.isPrivate("8.8.8.8"))
    }
}
