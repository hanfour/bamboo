// SPDX-License-Identifier: Apache-2.0

import Foundation
import Security

/// KeychainStore is a tiny wrapper over the native Keychain APIs for
/// the small set of secrets the bamboo app holds:
///
///   - WireGuard private key (32-byte Curve25519, base64)
///   - Session token (JWT from the controller, after OIDC sign-in)
///
/// Items are stored in the App Group keychain when an access group is
/// configured so the host app and the PacketTunnelProvider extension
/// can both read them. Set BAMBOO_KEYCHAIN_ACCESS_GROUP at build time
/// to your team's App Group identifier (TEAMID.dev.hanfour.bamboo).
public struct KeychainStore {
    public let service: String
    public let accessGroup: String?

    public init(service: String = "dev.hanfour.bamboo",
                accessGroup: String? = nil) {
        self.service = service
        self.accessGroup = accessGroup
    }

    public func setString(_ value: String, for key: String) throws {
        guard let data = value.data(using: .utf8) else {
            throw KeychainError.encodeFailed
        }
        try set(data, for: key)
    }

    public func getString(for key: String) -> String? {
        guard let data = get(for: key) else { return nil }
        return String(data: data, encoding: .utf8)
    }

    public func remove(for key: String) {
        var query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
        ]
        if let group = accessGroup {
            query[kSecAttrAccessGroup as String] = group
        }
        SecItemDelete(query as CFDictionary)
    }

    // MARK: - private

    private func set(_ data: Data, for key: String) throws {
        var query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
        ]
        if let group = accessGroup {
            query[kSecAttrAccessGroup as String] = group
        }
        // Delete any existing entry so SecItemAdd has a clean slot.
        SecItemDelete(query as CFDictionary)

        var add = query
        add[kSecValueData as String] = data
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
        let status = SecItemAdd(add as CFDictionary, nil)
        guard status == errSecSuccess else {
            throw KeychainError.osStatus(status)
        }
    }

    private func get(for key: String) -> Data? {
        var query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        if let group = accessGroup {
            query[kSecAttrAccessGroup as String] = group
        }
        var item: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &item)
        guard status == errSecSuccess else { return nil }
        return item as? Data
    }
}

public enum KeychainError: Error {
    case encodeFailed
    case osStatus(OSStatus)
}

/// Keys used by the bamboo app and extension. Exported so both targets
/// reference the same string and don't drift.
public enum BambooKeychainKey {
    public static let wireguardPrivateKey = "wg_private_key"
    public static let sessionToken = "session_token"
}
