// SPDX-License-Identifier: Apache-2.0

import Foundation
import Network

/// Parse a user-typed comma- or whitespace-separated list of CIDR
/// prefixes for the Settings "advertise routes" field.
///
/// Mirrors the CLI's `validateAdvertisedRoutes` (in
/// `clients/cli/cmd/bamboo/advertise.go`) but with the additional
/// concern that the input is one string the user typed into a UI
/// field — we tokenise on `,` / whitespace / newline, trim each
/// token, and discard empty tokens (so leading/trailing/double
/// commas don't trip the user up).
///
/// Returns the parsed CIDR list (preserved in the input order) on
/// success. Throws `AdvertiseRoutesError.invalid(token:)` on the
/// first malformed token so the caller can surface the offending
/// input in `lastError`.
///
/// Empty / whitespace-only input is valid and returns []. The
/// caller uses an empty array as "do not advertise anything",
/// which serialises to an omitted JSON field in
/// `BambooClient.RegisterRequest`.
internal func parseAdvertiseRoutes(_ raw: String) throws -> [String] {
    let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !trimmed.isEmpty else { return [] }
    let tokens = trimmed.split(whereSeparator: { c in
        c == "," || c == " " || c == "\t" || c == "\n"
    })
    var out: [String] = []
    for t in tokens {
        let s = String(t).trimmingCharacters(in: .whitespacesAndNewlines)
        if s.isEmpty { continue }
        guard let cidr = validatedCIDR(s) else {
            throw AdvertiseRoutesError.invalid(token: s)
        }
        out.append(cidr)
    }
    return out
}

internal enum AdvertiseRoutesError: Error, CustomStringConvertible {
    case invalid(token: String)
    var description: String {
        switch self {
        case .invalid(let t):
            return "advertise-routes: \"\(t)\" is not a valid CIDR"
        }
    }
}

/// validatedCIDR returns the input string when it parses as a valid
/// IPv4 or IPv6 CIDR prefix, or nil otherwise. Apple's `Network`
/// framework only provides `IPv4Address` / `IPv6Address` for the
/// address half — there is no built-in CIDR parser, so we split on
/// `/` ourselves and bound the prefix by address family.
private func validatedCIDR(_ s: String) -> String? {
    let parts = s.split(separator: "/", maxSplits: 1, omittingEmptySubsequences: false)
    guard parts.count == 2,
          let prefix = Int(parts[1]),
          prefix >= 0
    else { return nil }
    let ip = String(parts[0])
    if IPv4Address(ip) != nil, prefix <= 32 { return s }
    if IPv6Address(ip) != nil, prefix <= 128 { return s }
    return nil
}
