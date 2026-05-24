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
}
