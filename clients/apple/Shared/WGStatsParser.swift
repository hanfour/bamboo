// SPDX-License-Identifier: Apache-2.0

import Foundation

/// WGStatsParser reads the wg-uapi config string returned by
/// `WireGuardAdapter.getRuntimeConfiguration` and aggregates the
/// cumulative `tx_bytes` / `rx_bytes` across every peer. Mirrors
/// `clients/core/device.SumBytes` from the CLI side (PR #188).
///
/// The wg-uapi format is line-oriented `key=value` with a per-peer
/// repeating block keyed by `public_key=...`. We don't need to
/// parse the full structure — only the running totals for each
/// peer's `tx_bytes` and `rx_bytes`. So the parser keeps zero state
/// beyond the running sums.
///
/// Returns (tx_total, rx_total) — matches the controller's wire
/// pair (`bytesSent`, `bytesReceived`) order so the heartbeat
/// caller can forward them directly without a rx/tx vocabulary
/// flip that would silently swap send/receive on the dashboard.
///
/// Counter reset on adapter restart: each call returns the totals
/// observed RIGHT NOW. The controller already handles the
/// monotonic-reset detection in lib/bandwidth.ts:computeDeltas
/// (PR #189) so we don't double-handle it client-side.
internal enum WGStatsParser {
    static func sumBytes(_ config: String) -> (sent: UInt64, received: UInt64) {
        var sent: UInt64 = 0
        var received: UInt64 = 0
        // Normalize CRLF → LF first so a hand-pasted Windows-style
        // fixture or any cross-platform input parses cleanly.
        // wg-uapi on Apple platforms uses LF natively but the parser
        // shouldn't care.
        let normalized = config.replacingOccurrences(of: "\r\n", with: "\n")
        for line in normalized.split(separator: "\n", omittingEmptySubsequences: true) {
            guard let eq = line.firstIndex(of: "=") else { continue }
            let key = String(line[..<eq])
            let value = String(line[line.index(after: eq)...])
            switch key {
            case "tx_bytes":
                if let n = UInt64(value) { sent &+= n }
            case "rx_bytes":
                if let n = UInt64(value) { received &+= n }
            default:
                continue
            }
        }
        return (sent, received)
    }

    /// Per-peer endpoint, last handshake and rx bytes from the UAPI dump,
    /// keyed by the peer's base64 public key (the dump uses hex).
    static func observations(_ config: String) -> [String: WGPeerObservation] {
        var out: [String: WGPeerObservation] = [:]
        var key: String?
        var obs = WGPeerObservation(endpoint: nil, lastHandshake: nil, rxBytes: 0)
        var hsSec: TimeInterval = 0
        func flush() {
            if let k = key {
                obs.lastHandshake = hsSec > 0 ? Date(timeIntervalSince1970: hsSec) : nil
                out[k] = obs
            }
        }
        let normalized = config.replacingOccurrences(of: "\r\n", with: "\n")
        for line in normalized.split(separator: "\n", omittingEmptySubsequences: true) {
            guard let eq = line.firstIndex(of: "=") else { continue }
            let k = String(line[..<eq])
            let v = String(line[line.index(after: eq)...])
            switch k {
            case "public_key":
                flush()
                key = base64(fromHex: v)
                obs = WGPeerObservation(endpoint: nil, lastHandshake: nil, rxBytes: 0)
                hsSec = 0
            case "endpoint":
                obs.endpoint = v
            case "last_handshake_time_sec":
                hsSec = TimeInterval(v) ?? 0
            case "rx_bytes":
                obs.rxBytes = UInt64(v) ?? 0
            default:
                continue
            }
        }
        flush()
        return out
    }

    private static func base64(fromHex hex: String) -> String? {
        guard hex.count % 2 == 0 else { return nil }
        var data = Data(capacity: hex.count / 2)
        var idx = hex.startIndex
        while idx < hex.endIndex {
            let next = hex.index(idx, offsetBy: 2)
            guard let b = UInt8(hex[idx..<next], radix: 16) else { return nil }
            data.append(b)
            idx = next
        }
        return data.base64EncodedString()
    }
}
