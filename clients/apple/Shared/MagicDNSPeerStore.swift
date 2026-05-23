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
/// per-user, so the host app (uid hanfourmini) writes to
/// `/Users/hanfourmini/Library/Group Containers/<group>/…` while
/// the SystemExt (uid 0) reads from `/var/root/Library/Group Containers/…`
/// (or nothing at all). Result: every *.bamboo query NXDOMAINs.
///
/// Switching to a shared filesystem path that both processes can
/// reach. `/private/tmp` is world-readable + world-writable + sticky-
/// bit so only the writer can rewrite — adequate for non-confidential
/// peer metadata. Cleared on reboot; the host app rewrites the file
/// on every connect, so the post-reboot empty-window is bounded by
/// host-app launch latency.
///
/// Future hardening: move to a system-wide location like
/// `/Library/Application Support/bamboo/`, which would require a one-
/// time admin-elevated `mkdir` (e.g. via SMAppService or a separate
/// installer helper). For v1 the /private/tmp path matches the security
/// envelope of the existing UserDefaults App Group (process-level
/// only; no cryptographic separation).
public struct MagicDNSPeerStore {

    /// Shared file path. Both the host app and the root-running
    /// DNSProxy SystemExt read/write here. The host app's writes
    /// land owner=hanfourmini mode 0644; the SystemExt only reads.
    /// If the file is missing the reader returns an empty map, and
    /// the resolver NXDOMAINs every *.bamboo query (expected when
    /// bamboo isn't running or just launched).
    public static let sharedPath = "/private/tmp/dev.hanfour.bamboo.magicdns-peers.v1.json"

    private let url: URL

    public init?() {
        self.url = URL(fileURLWithPath: Self.sharedPath)
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

    /// Replace the entire peer map atomically. Writes a temp file
    /// (chmoded 0644 so the root SystemExt can read), then uses
    /// POSIX `rename(2)` to swap it onto the destination path in one
    /// atomic kernel operation. A concurrent reader's `open(2)`
    /// lands on either the old inode or the new one — never a
    /// missing file. The earlier `removeItem` + `moveItem` pattern
    /// (Foundation's `moveItem` throws if the destination exists)
    /// left a microsecond window where the file didn't exist and a
    /// reader saw `[:]` → NXDOMAIN.
    public func setPeers(_ peers: [String: PeerEntry]) {
        do {
            let data = try JSONEncoder().encode(peers)
            let tmp = url.deletingLastPathComponent()
                .appendingPathComponent(".magicdns-peers.\(UUID().uuidString).tmp")
            try data.write(to: tmp, options: [.atomic])
            // Permissions 0644 so the root-running SystemExt can read
            // (FileManager .write defaults to 0600 on macOS).
            try FileManager.default.setAttributes(
                [.posixPermissions: 0o644],
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
