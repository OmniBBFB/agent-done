import CryptoKit
import Foundation
import Security

struct Pairing: Codable, Equatable {
    static let defaultRelay = "wss://ad.omnibbfb.top/v1/connect"

    var relay = Self.defaultRelay
    var phoneID = ""
    var token = ""
    var workstationID = ""
    var workstationKey = ""

    func validated() throws -> (URL, Curve25519.KeyAgreement.PublicKey) {
        guard let url = URLComponents(string: relay), url.scheme?.lowercased() == "wss",
              let host = url.host, !host.isEmpty, url.path == "/v1/connect",
              url.user == nil, url.password == nil, url.query == nil, url.fragment == nil,
              let endpoint = url.url,
              Self.validID(phoneID), Self.validID(workstationID), phoneID != workstationID,
              !token.isEmpty, token.count <= 256,
              token.utf8.allSatisfy({ (33...126).contains($0) }),
              let rawKey = Data(base64Encoded: workstationKey), rawKey.base64EncodedString() == workstationKey
        else { throw WireError.invalidConfiguration }
        return (endpoint, try Curve25519.KeyAgreement.PublicKey(rawRepresentation: rawKey))
    }

    private static func validID(_ value: String) -> Bool {
        value.range(of: #"\A[A-Za-z0-9][A-Za-z0-9_-]{0,63}\z"#, options: .regularExpression) != nil
    }
}

enum PairingVault {
    static let pairing = "pairing"
    static let intents = "intents"
    private static let identity = "identity"

    struct Failure: LocalizedError {
        let status: OSStatus
        var errorDescription: String? { "钥匙串操作失败（\(status)）。" }
    }

    static func deviceKey() throws -> Curve25519.KeyAgreement.PrivateKey {
        if let raw = try read(identity) {
            return try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: raw)
        }
        let key = Curve25519.KeyAgreement.PrivateKey()
        try write(key.rawRepresentation, name: identity)
        return key
    }

    static func load<T: Decodable>(_ type: T.Type, name: String) throws -> T? {
        guard let data = try read(name) else { return nil }
        return try JSONDecoder().decode(type, from: data)
    }

    static func save<T: Encodable>(_ value: T, name: String) throws {
        try write(JSONEncoder().encode(value), name: name)
    }

    static func remove(_ name: String) throws {
        let status = SecItemDelete(query(name) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw Failure(status: status) }
    }

    private static func query(_ name: String) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: (Bundle.main.bundleIdentifier ?? "dev.agentdone.ios") + ".remote-control",
         kSecAttrAccount as String: name]
    }

    private static func read(_ name: String) throws -> Data? {
        var request = query(name)
        request[kSecReturnData as String] = true
        var result: CFTypeRef?
        let status = SecItemCopyMatching(request as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = result as? Data else { throw Failure(status: status) }
        return data
    }

    private static func write(_ data: Data, name: String) throws {
        let item = query(name)
        let attributes = [kSecValueData as String: data]
        var status = SecItemUpdate(item as CFDictionary, attributes as CFDictionary)
        if status == errSecItemNotFound {
            var newItem = item.merging(attributes) { _, new in new }
            newItem[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
            status = SecItemAdd(newItem as CFDictionary, nil)
        }
        guard status == errSecSuccess else { throw Failure(status: status) }
    }
}
