import CryptoKit
import Foundation

enum WireError: LocalizedError {
    case invalidConfiguration, invalidMessage, oversized, disconnected, timeout
    case relay(String), control(String), unsafeApproval

    var errorDescription: String? {
        switch self {
        case .invalidConfiguration: "请核对中转地址、设备 ID、令牌和工作站公钥。"
        case .invalidMessage: "工作站返回了无法识别的消息。"
        case .oversized: "消息超过中转大小限制。"
        case .disconnected: "与中转的连接已断开。"
        case .timeout: "工作站未在期限内回复。"
        case .relay("offline"): "工作站离线。"
        case .relay("forbidden"), .control("forbidden"): "设备未获得这项操作的权限。"
        case .control("conflict"): "该请求已有不同的回复。"
        case .control("not_found"): "工作站上已找不到该请求。"
        case .unsafeApproval: "审批缺少所需明细，或请求已经过期。"
        case .relay(let status): "中转返回 \(status)。"
        case .control(let code): "工作站返回 \(code)。"
        }
    }
}

enum ControlCrypto {
    static let packetLimit = 256 * 1024
    static let info = Data("agent-done/control/v1".utf8)

    static func seal(_ plaintext: Data, from: String, to: String,
                     local: Curve25519.KeyAgreement.PrivateKey,
                     peer: Curve25519.KeyAgreement.PublicKey) throws -> Data {
        guard plaintext.count <= packetLimit - 48 else { throw WireError.oversized }
        var sender = try HPKE.Sender(recipientKey: peer, ciphersuite: .Curve25519_SHA256_ChachaPoly,
                                     info: info, authenticatedBy: local)
        let encapsulatedKey = sender.encapsulatedKey
        return encapsulatedKey + (try sender.seal(plaintext, authenticating: aad(from, to)))
    }

    static func open(_ packet: Data, from: String, to: String,
                     local: Curve25519.KeyAgreement.PrivateKey,
                     peer: Curve25519.KeyAgreement.PublicKey) throws -> Data {
        guard (48...packetLimit).contains(packet.count) else { throw WireError.invalidMessage }
        var recipient = try HPKE.Recipient(privateKey: local, ciphersuite: .Curve25519_SHA256_ChachaPoly,
                                           info: info, encapsulatedKey: Data(packet.prefix(32)), authenticatedBy: peer)
        return try recipient.open(Data(packet.dropFirst(32)), authenticating: aad(from, to))
    }

    private static func aad(_ from: String, _ to: String) -> Data {
        info + Data("\n\(from)\n\(to)".utf8)
    }
}

struct RelayFrame: Codable {
    let type: String
    var id: String? = nil
    var to: String? = nil
    var payload: Data? = nil
    var device: String? = nil
    var from: String? = nil
    var status: String? = nil
    var online: Bool? = nil
}

struct ControlHeader: Decodable {
    let v: Int
    let type: String
    let id: String?
    let event: String?
}

struct ControlRequest<Params: Encodable>: Encodable {
    let v = 1
    let type = "request"
    let id: String
    let method: String
    let params: Params
}

struct ControlResponse<Result: Decodable>: Decodable {
    struct Failure: Decodable { let code: String }

    let result: Result?
    let error: Failure?

    func value() throws -> Result {
        switch (result, error) {
        case (.some(let result), nil): return result
        case (nil, .some(let error)): throw WireError.control(error.code)
        default: throw WireError.invalidMessage
        }
    }
}

struct PageQuery: Encodable {
    var sessionId: String? = nil
    var cursor: String? = nil
    let limit = 20
}

struct ControlPage<Item: Decodable & Sendable>: Decodable, Sendable {
    let items: [Item]
    let nextCursor: String?
}

struct WireSession: Decodable {
    let sessionId: String
    let title: String
    let state: String
    let updatedAtMs: Int64
}

struct WireHistory: Decodable {
    let messageId: String
    let role: String
    let text: String
    let truncated: Bool
}

struct WireInteraction: Decodable {
    struct Details: Decodable {
        let kind: String
        let cwd: String
        let reason: String?
        let command: String?
        let changes: String?
    }

    let sessionId: String
    let interactionId: String
    let kind: String
    let expiresAtMs: Int64?
    let summary: String?
    let canApprove: Bool?
    let details: Details?
    let questions: [SessionQuestion]?
}

enum InteractionState: String, Decodable {
    case pending, submitted, applied, closed, expired, unknown

    var description: String {
        switch self {
        case .pending: "工作站确认请求仍待处理。"
        case .submitted: "工作站已提交，等待 Codex 确认。"
        case .applied: "工作站已确认处理。"
        case .closed: "Codex 已结束等待。"
        case .expired: "请求已过期。"
        case .unknown: "工作站尚未确认结果，请同步后再操作。"
        }
    }
}

struct WireOutcome: Decodable {
    let interactionId: String
    let status: InteractionState
}

struct WireResponseBody: Codable, Equatable {
    let kind: String
    var decision: String? = nil
    var answers: [String: [String]]? = nil
}

struct ReplyQuery: Codable {
    let sessionId: String
    let interactionId: String
    let idempotencyKey: String
    var response: WireResponseBody? = nil
}

struct StatusQuery: Encodable {
    let sessionId: String
    let interactionId: String
}
