// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Parse a user-typed controller URL.
///
/// Trims surrounding whitespace (macOS 26 `URL(string:)` rejects
/// trailing newline / space, and pasted Settings fields often carry
/// one), then requires a scheme of `http` or `https`. Without the
/// scheme guard, a plain hostname like "controller.example.com"
/// parses into a `URL` with `scheme == nil` and silently passes
/// through to URLSession, which then fails downstream with an
/// "unsupported URL" error far from the actual mistake — the user
/// sees a connection failure with no hint that they forgot the
/// `https://` prefix.
///
/// Mirrors the trim + ws/wss scheme guard the relay block applies
/// to `relayURL` (see ConnectionViewModel.swift connectAsync ~L410)
/// so all user-typed URLs in the host app fail loudly at the parse
/// boundary instead of silently downstream.
///
/// Returns nil when the input is empty, malformed, or has a
/// non-http(s) scheme. Callers surface their own error message —
/// `signInAsync` sets `lastError`, `connectAsync` throws,
/// `refreshPolicyFromController` logs + returns.
internal func parseControllerURL(_ raw: String) -> URL? {
    let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !trimmed.isEmpty,
          let url = URL(string: trimmed),
          let scheme = url.scheme?.lowercased(),
          scheme == "http" || scheme == "https"
    else { return nil }
    return url
}
