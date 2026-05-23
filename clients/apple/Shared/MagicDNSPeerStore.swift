// SPDX-License-Identifier: Apache-2.0

import Foundation

/// MagicDNSPeerStore is the IPC channel between the host bamboo app
/// and the DNSProxy extension process. The host app writes the peer
/// name→IP snapshot; the DNSProxy extension reads it on every query.
///
/// **macOS quirk (mesh-debug 2026-05-23)**: the original design used
/// a shared App Group UserDefaults suite (sanctioned by Apple for
/// App Extensions). That works for sandboxed App Extensions because
/// the OS maps the App Group container into both the host app and
/// extension's sandbox. **It does NOT work for macOS System
/// Extensions running as root** — App Group containers are
/// per-user, so the host app (uid user) writes to
/// `/Users/<user>/Library/Group Containers/<group>/…` while the
/// SystemExt (uid 0) reads from `/var/root/Library/Group Containers/…`
/// (or nothing at all). Result: every *.bamboo query NXDOMAINs.
///
/// **Current design (issue #154)**: shared file under
/// `/Users/Shared/dev.hanfour.bamboo/`, parent dir mode 0700, file
/// mode 0600. /Users/Shared is itself world-traversable (1777), but
/// our 0700 subdir prevents other local users from reading the peer
/// map. The DNSProxy SystemExt runs as root and bypasses POSIX
/// permission checks; its sandbox profile grants explicit read
/// access via the `temporary-exception.files.absolute-path.read-only`
/// entitlement (DNSProxy_macOS.entitlements). Survives reboot
/// (unlike the previous /private/tmp location, which was cleared on
/// every boot).
///
/// Future hardening if the security envelope tightens further:
/// /Library/Application Support/<group>/ owned by root, written via
/// an SMAppService-installed LaunchDaemon. For today the 0700-dir
/// model satisfies the issue's "not readable by other local users"
/// acceptance without the privileged-helper UX cost (one-time
/// admin auth prompt on install).
public struct MagicDNSPeerStore {

    /// Parent directory for the shared peer-map file. Created on
    /// first write with mode 0700 (owner-only). On a multi-user Mac
    /// only the first user to run bamboo can write here; secondary
    /// users would block at the directory open. Single-user dev
    /// machines (the common case) are unaffected.
    public static let sharedDir = "/Users/Shared/dev.hanfour.bamboo"

    /// Shared file path. Mode 0600 (owner read+write). The root-
    /// running DNSProxy SystemExt bypasses POSIX, so the read still
    /// works once the sandbox entitlement is in place; other local
    /// users are blocked at the directory boundary (sharedDir is 0700).
    public static let sharedPath = sharedDir + "/magicdns-peers.v1.json"

    private let url: URL

    public init() {
        self.url = URL(fileURLWithPath: Self.sharedPath)
    }

    /// Test-only constructor that points the store at an arbitrary
    /// file path. Used by AppleSharedTests to avoid colliding with
    /// the production /private/tmp file (or polluting it). Marked
    /// internal — production code paths always use init().
    internal init(path: String) {
        self.url = URL(fileURLWithPath: path)
    }

    /// PeerEntry is the per-peer DNS record. ipv4 is always set for
    /// peers in the 100.64.0.0/10 tailnet; ipv6 is reserved for a
    /// future expansion (currently peer IPs are IPv4-only, so the
    /// resolver answers AAAA with NOERROR + empty answer rather
    /// than synthesizing anything).
    public struct PeerEntry: Codable, Equatable {
        public let ipv4: String
        public let ipv6: String?
        /// hostname is the human-readable label (preserved so logs
        /// in the extension show the user-supplied name when the
        /// query lands; the resolver matches on the dict key, not
        /// this field).
        public let hostname: String?

        public init(ipv4: String, ipv6: String? = nil, hostname: String? = nil) {
            self.ipv4 = ipv4
            self.ipv6 = ipv6
            self.hostname = hostname
        }
    }

    /// Replace the entire peer map atomically. Ensures the parent
    /// dir exists with mode 0700, writes a temp file (chmoded 0600),
    /// then uses POSIX `rename(2)` to swap it onto the destination
    /// path in one atomic kernel operation. A concurrent reader's
    /// `open(2)` lands on either the old inode or the new one —
    /// never a missing file.
    public func setPeers(_ peers: [String: PeerEntry]) {
        do {
            // Ensure parent dir exists. createDirectory with
            // withIntermediateDirectories is idempotent — if the dir
            // already exists it succeeds without modifying perms
            // (no permission-creep if some other code changed the
            // mode after we created it). First-time creation sets
            // mode 0700 so other local users can't traverse into it.
            try FileManager.default.createDirectory(
                atPath: url.deletingLastPathComponent().path,
                withIntermediateDirectories: true,
                attributes: [.posixPermissions: 0o700]
            )
            let data = try JSONEncoder().encode(peers)
            let tmp = url.deletingLastPathComponent()
                .appendingPathComponent(".magicdns-peers.\(UUID().uuidString).tmp")
            try data.write(to: tmp, options: [.atomic])
            // Permissions 0600 (owner-only). The root-running
            // DNSProxy SystemExt reads via the sandbox temporary-
            // exception entitlement; root bypasses POSIX so the
            // tight mode is fine.
            try FileManager.default.setAttributes(
                [.posixPermissions: 0o600],
                ofItemAtPath: tmp.path
            )
            if Darwin.rename(tmp.path, url.path) != 0 {
                let err = errno
                _ = try? FileManager.default.removeItem(at: tmp)
                NSLog("[MagicDNSPeerStore] rename(\(tmp.path) → \(url.path)) failed errno=\(err)")
            }
        } catch {
            NSLog("[MagicDNSPeerStore] write failed at \(Self.sharedPath): \(error)")
        }
    }

    /// Read the current peer map. Returns empty when the file is
    /// missing (fresh boot before host app launched) or malformed —
    /// in either case the resolver NXDOMAINs every *.bamboo query
    /// until the host app writes a fresh snapshot.
    public func peers() -> [String: PeerEntry] {
        guard FileManager.default.fileExists(atPath: url.path) else {
            return [:]
        }
        do {
            let data = try Data(contentsOf: url)
            return try JSONDecoder().decode([String: PeerEntry].self, from: data)
        } catch {
            NSLog("[MagicDNSPeerStore] read failed at \(Self.sharedPath): \(error)")
            return [:]
        }
    }

    /// Look up a single label. Convenience for the hot path inside
    /// the resolver — avoids re-decoding the full map per query
    /// only when the caller already knows the label.
    public func peer(forLabel label: String) -> PeerEntry? {
        peers()[label.lowercased()]
    }

    /// Clear the map. Called when the host app signs out or the
    /// user opts out of MagicDNS so the extension stops answering
    /// stale entries.
    public func clear() {
        _ = try? FileManager.default.removeItem(at: url)
    }
}
