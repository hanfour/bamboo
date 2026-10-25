// SPDX-License-Identifier: Apache-2.0

import XCTest

final class ControlPlaneTests: XCTestCase {
    private let cp = ControlPlaneConfig(controllerURL: "https://c.example", tenantSlug: "t", peerID: "p1",
                                        sessionToken: "tok", hostname: "iPhone", wireguardPublicKey: "PUB",
                                        os: "ios", clientVersion: "0.1.20",
                                        advertisedRoutes: ["10.0.0.0/24"], advertiseExitNode: nil)

    func testControlPlaneConfigSurvivesJSONRoundTrip() throws {
        let c = BambooTunnelConfig(privateKey: "k", address: "100.64.0.4/32", controlPlane: cp)
        let back = try JSONDecoder().decode(BambooTunnelConfig.self, from: JSONEncoder().encode(c))
        XCTAssertEqual(back.controlPlane, cp)
    }

    func testReplacingPeersKeepsControlPlane() {
        let c = BambooTunnelConfig(privateKey: "k", address: "a", controlPlane: cp)
        XCTAssertEqual(c.replacingPeers([]).controlPlane, cp)
    }

    func testAsPresenceUsesRenewedTokenWhenGiven() {
        XCTAssertEqual(cp.asPresence().sessionToken, "tok")
        XCTAssertEqual(cp.asPresence(token: "new").sessionToken, "new")
        XCTAssertEqual(cp.asPresence().peerID, "p1")
    }

    func testPeerSnapshotResponseRoundTrips() throws {
        let snap = TunnelIPC.PeerSnapshot(
            peers: [.init(dnsName: "mac-mini", hostname: "Mac mini", ip: "100.64.0.1", ip6: nil)],
            dns64Enabled: true, nat64Prefix: "64:ff9b::/96")
        let data = try TunnelIPC.encode(TunnelIPC.Response(ok: true, snapshot: snap))
        XCTAssertEqual(try TunnelIPC.decodeResponse(data).snapshot, snap)
        let req = try TunnelIPC.decode(try TunnelIPC.encode(TunnelIPC.Request(kind: .peerSnapshot)))
        XCTAssertEqual(req.kind, .peerSnapshot)
    }

    func testResponseWithoutSnapshotStillDecodes() throws {
        let old = #"{"ok":true}"#
        XCTAssertNil(try TunnelIPC.decodeResponse(Data(old.utf8)).snapshot)
    }

    func testStoppedControlPlaneIsIdleAndEmpty() async {
        let c = ControlPlane()
        let running = await c.isRunning
        XCTAssertFalse(running)
        let auth = await c.auth()
        XCTAssertNil(auth)
        let snap = await c.snapshot()
        XCTAssertTrue(snap.peers.isEmpty)
    }
}
