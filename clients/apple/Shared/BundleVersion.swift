// SPDX-License-Identifier: Apache-2.0

import Foundation

/// `BundleVersion` exposes the host bundle's marketing version
/// (`CFBundleShortVersionString`) for the controller's
/// `clientVersion` field on `/api/v1/peers/register`.
///
/// Why a small standalone helper instead of an inline call in
/// ConnectionViewModel:
///
///   - The `read(from:)` form takes an explicit `[String: Any]`
///     dict so the unit test can pass a fixture; `Bundle.main` is
///     unavailable to plain `xctest` unit tests (the test bundle
///     has its own info-dict and no marketing version of its own).
///   - Keeping it in a small file lets the AppleSharedTests target
///     compile it in directly — pulling all of ConnectionViewModel
///     into the test target would drag in WireGuardKit and ~12
///     other Shared/* files transitively.
///
/// Fallback choice — "0.0.0", not "":
///
///   The controller distinguishes "no clientVersion reported"
///   (badge suppressed) from "very old client version" (badge
///   shown). An empty string would suppress the upgrade-indicator
///   badge entirely; "0.0.0" classifies as "ancient" so an
///   operator whose Info.plist accidentally loses the key still
///   sees a "please upgrade" signal instead of silently looking
///   up-to-date.
enum BundleVersion {
    /// Reads `CFBundleShortVersionString` from the given info-dict,
    /// returning "0.0.0" when the key is missing or the wrong type.
    /// `CFBundleShortVersionString` is documented String — any other
    /// type is treated as missing rather than coerced.
    static func read(from info: [String: Any]) -> String {
        if let v = info["CFBundleShortVersionString"] as? String, !v.isEmpty {
            return v
        }
        return "0.0.0"
    }

    /// The host bundle's marketing version, or "0.0.0" if absent.
    /// Used by ConnectionViewModel's register + refresh paths.
    static var current: String {
        read(from: Bundle.main.infoDictionary ?? [:])
    }
}
