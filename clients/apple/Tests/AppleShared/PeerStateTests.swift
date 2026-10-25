// SPDX-License-Identifier: Apache-2.0

import XCTest

// Shared/PeerState.swift and Shared/BambooTunnelConfig.swift are compiled into
// this target, so these types need no import.
final class PeerStateTests: XCTestCase {
    private func peer(_ id: String, ip: String, endpoints: [String]? = nil,
                      allowed: [String]? = nil) -> BambooClient.PeerJSON {
        BambooClient.PeerJSON(id: id, tenantId: "t", hostname: "h-\(id)", ip: ip, ip6: nil,
                              wireguardPublicKey: "key-\(id)", os: nil, clientVersion: nil,
                              endpoints: endpoints, peerDnsName: nil, allowedIps: allowed)
    }

    private func register(policy: Int64, selfPeer: BambooClient.PeerJSON,
                          peers: [BambooClient.PeerJSON]) throws -> BambooClient.RegisterResponse {
        struct Wire: Encodable {
            let `self`: BambooClient.PeerJSON
            let peers: [BambooClient.PeerJSON]
            let policyRevision: Int64
        }
        let data = try JSONEncoder().encode(Wire(self: selfPeer, peers: peers, policyRevision: policy))
        return try JSONDecoder().decode(BambooClient.RegisterResponse.self, from: data)
    }

    func testEnforcingDeniesAPeerWithoutAllowedIPs() throws {
        let me = peer("me", ip: "100.64.0.1")
        let resp = try register(policy: 4, selfPeer: me, peers: [
            peer("ok", ip: "100.64.0.2", allowed: ["100.64.0.2/32"]),
            peer("denied", ip: "100.64.0.3", allowed: []),
            peer("legacy", ip: "100.64.0.4", allowed: nil),
        ])
        let ids = PeerState(register: resp).peerConfigs().map(\.id)
        XCTAssertEqual(ids, ["ok"], "an empty or missing AllowedIps set is a denial under enforcement")
    }

    func testWithoutPolicyEveryPeerFallsBackToItsHostRoute() throws {
        let resp = try register(policy: 0, selfPeer: peer("me", ip: "100.64.0.1"), peers: [
            peer("a", ip: "100.64.0.2", allowed: nil),
            peer("b", ip: "100.64.0.3", allowed: ["100.64.0.3/32", "10.9.0.0/24"]),
        ])
        let cfgs = PeerState(register: resp).peerConfigs()
        XCTAssertEqual(cfgs.first { $0.id == "a" }?.allowedIPs, ["100.64.0.2/32"])
        XCTAssertEqual(cfgs.first { $0.id == "b" }?.allowedIPs, ["100.64.0.3/32", "10.9.0.0/24"])
    }

    /// Regression: the first config after connect used the AllowedIps cached
    /// from the previous session (empty on a cold start), so under enforcement
    /// it contained no peers until a later refresh. Built from the register
    /// response, it must already hold them.
    func testFirstConfigAfterRegisterReflectsThatResponse() throws {
        let resp = try register(policy: 4, selfPeer: peer("me", ip: "100.64.0.1"), peers: [
            peer("mac", ip: "100.64.0.2", endpoints: ["203.0.113.5:51820"], allowed: ["100.64.0.2/32"]),
        ])
        let cfg = PeerState(register: resp).tunnelConfig(
            privateKey: "k", dnsServers: [], mtu: 1280, wgListenPort: 51820, presence: nil)
        XCTAssertEqual(cfg?.peers.map(\.id), ["mac"])
        XCTAssertEqual(cfg?.peers.first?.endpoint, "203.0.113.5:51820")
    }

    func testRelayEndpointBeatsThePeersOwnEndpoint() throws {
        let resp = try register(policy: 0, selfPeer: peer("me", ip: "100.64.0.1"), peers: [
            peer("viaRelay", ip: "100.64.0.2", endpoints: ["203.0.113.5:1"]),
            peer("direct", ip: "100.64.0.3", endpoints: ["203.0.113.6:2"]),
            peer("none", ip: "100.64.0.4"),
        ])
        let state = PeerState(register: resp, relayEndpoints: ["viaRelay": "127.0.0.1:40001"])
        let byID = Dictionary(uniqueKeysWithValues: state.peerConfigs().map { ($0.id, $0.endpoint) })
        XCTAssertEqual(byID["viaRelay"] ?? nil, "127.0.0.1:40001")
        XCTAssertEqual(byID["direct"] ?? nil, "203.0.113.6:2")
        XCTAssertNil(byID["none"] ?? nil)
    }

    func testPeerOrderIsStableSoEqualStatesGiveEqualConfigs() {
        var a = PeerState(); a.selfID = "me"; a.selfIPv4 = "100.64.0.1"
        var b = a
        for id in ["c", "a", "b"] { a.peers[id] = peer(id, ip: "100.64.0.\(id.utf8.first!)") }
        for id in ["b", "c", "a"] { b.peers[id] = peer(id, ip: "100.64.0.\(id.utf8.first!)") }
        XCTAssertEqual(a.peerConfigs().map(\.id), ["a", "b", "c"])
        XCTAssertEqual(a.peerConfigs(), b.peerConfigs())
    }

    func testSelfIsNeverAPeer() throws {
        let resp = try register(policy: 0, selfPeer: peer("me", ip: "100.64.0.1"), peers: [peer("x", ip: "100.64.0.2")])
        let state = PeerState(register: resp)
        XCTAssertNotNil(state.peers["me"], "self stays in the map for MagicDNS")
        XCTAssertFalse(state.peerConfigs().contains { $0.id == "me" })
    }

    func testNoConfigBeforeThisDeviceHasAnAddress() {
        XCTAssertNil(PeerState().tunnelConfig(privateKey: "k", dnsServers: [], mtu: nil, wgListenPort: 0, presence: nil))
    }

    func testAddressFormatsAndPresencePassThrough() throws {
        var me = peer("me", ip: "100.64.0.1"); me.ip6 = "fdba:1100::6440:1"
        let resp = try register(policy: 0, selfPeer: me, peers: [])
        let presence = PresenceConfig(controllerURL: "https://x", tenantSlug: "d", peerID: "me", sessionToken: "t")
        let cfg = PeerState(register: resp).tunnelConfig(
            privateKey: "k", dnsServers: ["1.1.1.1"], mtu: 1280, wgListenPort: 5, presence: presence)
        XCTAssertEqual(cfg?.address, "100.64.0.1/32")
        XCTAssertEqual(cfg?.address6, "fdba:1100::6440:1/128")
        XCTAssertEqual(cfg?.dnsServers, ["1.1.1.1"])
        XCTAssertEqual(cfg?.presence, presence)
    }

    /// A watch event replaces a peer but carries no AllowedIps. The cached
    /// set must survive, or a PeerUpdated for an endpoint change would drop
    /// the peer under enforcement.
    func testReplacingAPeerFromAnEventKeepsItsAllowedIPs() throws {
        let resp = try register(policy: 4, selfPeer: peer("me", ip: "100.64.0.1"), peers: [
            peer("mac", ip: "100.64.0.2", allowed: ["100.64.0.2/32"]),
        ])
        var state = PeerState(register: resp)
        state.peers["mac"] = peer("mac", ip: "100.64.0.2", endpoints: ["198.51.100.9:7"], allowed: nil)
        let cfg = state.peerConfigs().first
        XCTAssertEqual(cfg?.allowedIPs, ["100.64.0.2/32"])
        XCTAssertEqual(cfg?.endpoint, "198.51.100.9:7")
    }

    func testAdoptingARefreshDropsRemovedPeersButKeepsIdentityAndRelays() throws {
        let first = try register(policy: 4, selfPeer: peer("me", ip: "100.64.0.1"), peers: [
            peer("a", ip: "100.64.0.2", allowed: ["100.64.0.2/32"]),
            peer("b", ip: "100.64.0.3", allowed: ["100.64.0.3/32"]),
        ])
        var state = PeerState(register: first, relayEndpoints: ["a": "127.0.0.1:1"])
        let refreshed = try register(policy: 5, selfPeer: peer("me", ip: "100.64.0.1"), peers: [
            peer("a", ip: "100.64.0.2", allowed: ["100.64.0.2/32", "10.0.0.0/24"]),
        ])
        state.adopt(register: refreshed)
        XCTAssertEqual(state.policyRevision, 5)
        XCTAssertEqual(state.peerConfigs().map(\.id), ["a"], "b was removed")
        XCTAssertEqual(state.peerConfigs().first?.allowedIPs, ["100.64.0.2/32", "10.0.0.0/24"])
        XCTAssertEqual(state.selfID, "me")
        XCTAssertEqual(state.relayEndpoints, ["a": "127.0.0.1:1"])
    }
}
