// SPDX-License-Identifier: Apache-2.0

import XCTest

// Note: Shared/MagicDNSPeerStore.swift is compiled directly into the
// AppleSharedTests target (per project.yml), so the types are visible
// at the module level without an @testable import. This sidesteps
// having to expose a separate BambooShared framework just for tests.

/// Tests for the host↔extension peer-map IPC store.
///
/// These tests use the `internal init(path:)` so each test gets its
/// own temp file under `NSTemporaryDirectory()` — never touches the
/// production `/private/tmp/dev.hanfour.bamboo.magicdns-peers.v1.json`.
final class MagicDNSPeerStoreTests: XCTestCase {

    private var testPath: String = ""

    override func setUp() {
        super.setUp()
        testPath = NSTemporaryDirectory()
            + "bamboo-tests-\(UUID().uuidString).json"
    }

    override func tearDown() {
        try? FileManager.default.removeItem(atPath: testPath)
        super.tearDown()
    }

    private func makeStore() -> MagicDNSPeerStore {
        MagicDNSPeerStore(path: testPath)
    }

    // MARK: - basic round-trip

    func testRoundTripPreservesKeysAndValues() {
        let store = makeStore()
        let input: [String: MagicDNSPeerStore.PeerEntry] = [
            "mac-mini": .init(ipv4: "100.64.0.1",
                              ipv6: nil,
                              hostname: "Mac mini"),
            "h4": .init(ipv4: "100.64.0.3",
                        ipv6: "fd7a::3",
                        hostname: "H4"),
        ]
        store.setPeers(input)

        let read = store.peers()
        XCTAssertEqual(read, input)
    }

    func testRoundTripEmptyMap() {
        let store = makeStore()
        store.setPeers([:])

        XCTAssertTrue(store.peers().isEmpty)
    }

    // MARK: - missing / malformed file

    func testPeersWhenFileMissing() {
        // Test path doesn't exist (setUp hasn't written anything).
        let store = makeStore()
        XCTAssertTrue(store.peers().isEmpty,
                      "peers() should return empty when file is missing")
    }

    func testPeersWhenMalformedJSON() throws {
        try "{ not valid json".data(using: .utf8)!
            .write(to: URL(fileURLWithPath: testPath))

        let store = makeStore()
        XCTAssertTrue(store.peers().isEmpty,
                      "peers() should return empty when file is malformed")
    }

    func testPeersWhenWrongShape() throws {
        // Valid JSON but not the right schema (array instead of dict).
        try "[1, 2, 3]".data(using: .utf8)!
            .write(to: URL(fileURLWithPath: testPath))

        let store = makeStore()
        XCTAssertTrue(store.peers().isEmpty,
                      "peers() should return empty when JSON shape is wrong")
    }

    // MARK: - clear

    func testClearRemovesFile() {
        let store = makeStore()
        store.setPeers(["a": .init(ipv4: "100.64.0.1")])
        XCTAssertTrue(FileManager.default.fileExists(atPath: testPath))

        store.clear()
        XCTAssertFalse(FileManager.default.fileExists(atPath: testPath))
        XCTAssertTrue(store.peers().isEmpty)
    }

    func testClearWhenAlreadyEmpty() {
        let store = makeStore()
        // Should not crash; the underlying `removeItem` is wrapped
        // in `try?`.
        store.clear()
        XCTAssertTrue(store.peers().isEmpty)
    }

    // MARK: - file permissions (cross-uid access)

    func testWrittenFileHasMode0600() throws {
        let store = makeStore()
        store.setPeers(["a": .init(ipv4: "100.64.0.1")])

        let attrs = try FileManager.default.attributesOfItem(atPath: testPath)
        let perms = (attrs[.posixPermissions] as? NSNumber)?.uint16Value
        // 0600 == 0o600 — owner-only read/write. Tightened from
        // 0644 in issue #154 to block other local users from
        // reading the peer-map. The root-running DNSProxy SystemExt
        // bypasses POSIX so the tight mode is fine.
        XCTAssertEqual(perms, 0o600,
                       "file must be owner-only (0600) — non-root users blocked, root bypasses")
    }

    func testCreatedParentDirHasMode0700() throws {
        // Point at a path under a fresh subdir that doesn't exist
        // yet — exercises the createDirectory(withIntermediate)
        // branch and asserts the new dir is mode 0700.
        let subdir = NSTemporaryDirectory()
            + "bamboo-tests-dir-\(UUID().uuidString)"
        defer { try? FileManager.default.removeItem(atPath: subdir) }
        let path = subdir + "/peers.json"
        let store = MagicDNSPeerStore(path: path)

        store.setPeers(["a": .init(ipv4: "100.64.0.1")])

        let attrs = try FileManager.default.attributesOfItem(atPath: subdir)
        let perms = (attrs[.posixPermissions] as? NSNumber)?.uint16Value
        XCTAssertEqual(perms, 0o700,
                       "parent dir must be owner-only-traversable (0700) so other users can't even open the file")
    }

    // MARK: - atomic write (rename-based, not unlink+move)

    func testAtomicReplaceDoesNotBriefMissingFile() throws {
        // Pre-seed with v1 content so a subsequent setPeers must
        // replace, not just create. The POSIX rename(2) used by
        // setPeers must atomically swap inodes so the destination
        // path never returns a "file not found" mid-write.
        let store = makeStore()
        store.setPeers(["v1": .init(ipv4: "100.64.0.1")])
        XCTAssertTrue(FileManager.default.fileExists(atPath: testPath))

        store.setPeers(["v2": .init(ipv4: "100.64.0.2")])

        // Post-replace, file exists with new content. Most importantly
        // the file is *still present* — the earlier unlink+move
        // pattern would have left a microsecond gap; with rename(2)
        // the path is occupied throughout.
        XCTAssertTrue(FileManager.default.fileExists(atPath: testPath))
        XCTAssertEqual(store.peers().keys.sorted(), ["v2"])
    }

    // MARK: - lookup convenience

    func testPeerForLabelLowercased() {
        let store = makeStore()
        store.setPeers(["mac-mini": .init(ipv4: "100.64.0.1")])

        // Stored keys are lowercase (per ConnectionViewModel's sync
        // step). Lookups via peer(forLabel:) must lowercase too so
        // "MAC-MINI" / "Mac-Mini" all resolve.
        XCTAssertEqual(store.peer(forLabel: "mac-mini")?.ipv4, "100.64.0.1")
        XCTAssertEqual(store.peer(forLabel: "MAC-MINI")?.ipv4, "100.64.0.1")
        XCTAssertEqual(store.peer(forLabel: "Mac-Mini")?.ipv4, "100.64.0.1")
    }

    func testPeerForLabelMissing() {
        let store = makeStore()
        store.setPeers(["mac-mini": .init(ipv4: "100.64.0.1")])

        XCTAssertNil(store.peer(forLabel: "unknown-peer"))
    }
}
