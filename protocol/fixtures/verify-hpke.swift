import CryptoKit
import Foundation

struct Vector: Decodable {
    let from: String
    let to: String
    let senderPublicKey: String
    let receiverPrivateKey: String
    let payload: String
    let plaintext: String
}

struct Fixture: Decodable {
    let vectors: [Vector]
}

enum VerificationFailure: Error {
    case usage
    case invalidBase64
    case invalidPacket
    case wrongPlaintext
    case unexpectedlyOpened
}

func decode(_ text: String) throws -> Data {
    guard let data = Data(base64Encoded: text) else {
        throw VerificationFailure.invalidBase64
    }
    return data
}

func open(_ vector: Vector, payload: Data, aad: Data) throws -> Data {
    guard payload.count >= 48 else {
        throw VerificationFailure.invalidPacket
    }
    let privateKey = try Curve25519.KeyAgreement.PrivateKey(
        rawRepresentation: decode(vector.receiverPrivateKey)
    )
    let senderKey = try Curve25519.KeyAgreement.PublicKey(
        rawRepresentation: decode(vector.senderPublicKey)
    )
    let info = Data("agent-done/control/v1".utf8)
    var recipient = try HPKE.Recipient(
        privateKey: privateKey,
        ciphersuite: .Curve25519_SHA256_ChachaPoly,
        info: info,
        encapsulatedKey: Data(payload.prefix(32)),
        authenticatedBy: senderKey
    )
    return try recipient.open(Data(payload.dropFirst(32)), authenticating: aad)
}

func expectRejection(_ operation: () throws -> Data) throws {
    do {
        _ = try operation()
    } catch {
        return
    }
    throw VerificationFailure.unexpectedlyOpened
}

guard CommandLine.arguments.count == 2 else {
    print("usage: swift verify-hpke.swift hpke-auth-v1.json")
    throw VerificationFailure.usage
}

let decoder = JSONDecoder()
decoder.keyDecodingStrategy = .convertFromSnakeCase
let fixture = try decoder.decode(
    Fixture.self, from: Data(contentsOf: URL(fileURLWithPath: CommandLine.arguments[1]))
)
for vector in fixture.vectors {
    let payload = try decode(vector.payload)
    let aad = Data("agent-done/control/v1\n\(vector.from)\n\(vector.to)".utf8)
    let plaintext = try open(vector, payload: payload, aad: aad)
    guard plaintext == (try decode(vector.plaintext)) else {
        throw VerificationFailure.wrongPlaintext
    }
    var modified = payload
    modified[modified.count - 1] ^= 1
    try expectRejection { try open(vector, payload: modified, aad: aad) }
    try expectRejection { try open(vector, payload: payload, aad: aad + Data([0])) }
}
print("HPKE vectors OK (\(fixture.vectors.count))")
