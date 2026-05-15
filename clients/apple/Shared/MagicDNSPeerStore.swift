// SPDX-License-Identifier: Apache-2.0

import Foundation

/// MagicDNSPeerStore is the IPC channel between the host bamboo app
/// and the DNSProxy extension process. Both targets carry the
/// `group.dev.hanfour.bamboo` App Group entitlement so this shared
/// UserDefaults suite is the only sanctioned way to ferry the peer
/// name→IP map across the sandbox boundary at the OS-blessed rate
/// (UserDefaults reads from the App Group suite are coherent, atomic,
/// and KVO-observable).
///
/// The data is small (~kilobytes for typical tenants), changes
/// infrequently (peer add/remove on the order of minutes), and is
/// non-confidential within the device — UserDefaults is the right
/// granularity. Migrating to a CFMessagePort or XPC channel would
/// only be justified if the resolver needed sub-millisecond fresh-
/// ness or cryptographic isolation between processes.
public struct MagicDNSPeerStore {

    /// The App Group identifier. Must match the
    /// `com.apple.security.application-groups` entitlement value on
    /// both the host app and the DNSProxy extension. Drift here
    /// silently breaks resolution — host writes succeed, extension
    /// reads return empty, every *.bamboo lookup NXDOMAINs.
    public static let appGroup = "group.dev.hanfour.bamboo"

    /// Stored value is a JSON-encoded `[String: PeerEntry]` keyed by
    /// the DNS-safe label (e.g. "mac-mini" → entry for 100.64.0.1).
    /// JSON instead of a plist-encoded dict because Codable→JSON is
    /// the path of least drift between Swift versions on iOS/macOS.
    private static let peersKey = "magicdns.peers.v1"

    private let defaults: UserDefaults

    public init?() {
        guard let d = UserDefaults(suiteName: Self.appGroup) else {
            return nil
        }
        self.defaults = d
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

    /// Replace the entire peer map atomically. The extension reads
    /// the suite on every flow, so a write here is effective on the
    /// next DNS query. Callers (ConnectionViewModel) write the full
    /// snapshot rather than incremental diffs to keep the IPC
    /// surface trivial — at 5-50 peers the JSON is well under a
    /// page and re-encoding cost is negligible.
    public func setPeers(_ peers: [String: PeerEntry]) {
        do {
            let data = try JSONEncoder().encode(peers)
            defaults.set(data, forKey: Self.peersKey)
        } catch {
            // Encoding a `[String: Codable]` is essentially
            // infallible; if it ever fails we'd rather log + skip
            // than crash the host app.
            NSLog("[MagicDNSPeerStore] encode failed: \(error)")
        }
    }

    /// Read the current peer map. Returns empty when nothing has
    /// been written yet (fresh install, host app hasn't connected
    /// in this OS session) — the extension then NXDOMAINs every
    /// `*.bamboo` query.
    public func peers() -> [String: PeerEntry] {
        guard let data = defaults.data(forKey: Self.peersKey) else {
            return [:]
        }
        do {
            return try JSONDecoder().decode([String: PeerEntry].self, from: data)
        } catch {
            NSLog("[MagicDNSPeerStore] decode failed: \(error)")
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
        defaults.removeObject(forKey: Self.peersKey)
    }
}
