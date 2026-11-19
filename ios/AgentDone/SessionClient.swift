import Foundation

enum SessionStatus {
    case approval, question, waiting, running, completed, idle, closed, failed

    var title: String {
        switch self {
        case .approval: "待审批"
        case .question: "待回答"
        case .waiting: "等待中"
        case .running: "运行中"
        case .completed: "已完成"
        case .idle: "空闲"
        case .closed: "已关闭"
        case .failed: "执行失败"
        }
    }

    var needsAttention: Bool {
        switch self {
        case .approval, .question, .waiting, .failed: true
        default: false
        }
    }
}

struct Workstation: Identifiable {
    let id: String
    let name: String
    let isOnline: Bool
}

struct AgentSession: Identifiable {
    let id: String
    let workstationID: Workstation.ID
    let title: String
    var directory: String? = nil
    var updatedAt: Date? = nil
    var status: SessionStatus
    var interactions: [Interaction]
}

struct SessionQuestion: Identifiable, Decodable {
    let id: String
    let text: String
    let options: [String]
    let allowsText: Bool
}

struct ApprovalPrompt {
    let summary: String
    let action: String?
    var reason: String? = nil
    var directory: String? = nil
    let canApprove: Bool
}

struct Interaction: Identifiable {
    enum Prompt {
        case approval(ApprovalPrompt)
        case question([SessionQuestion])
    }

    let id: String
    let prompt: Prompt
    var expiresAt: Date? = nil
    var isExpired: Bool { expiresAt.map { $0 <= .now } ?? false }
}

struct HistoryEntry: Identifiable {
    let id: String
    let author: String
    let text: String
    var truncated = false
}

struct HistoryPage {
    let entries: [HistoryEntry]
    let nextCursor: String?
}

enum InteractionReply {
    case approve
    case decline
    case answer([String: [String]])

    var wire: WireResponseBody {
        switch self {
        case .approve: WireResponseBody(kind: "approval", decision: "allow_once")
        case .decline: WireResponseBody(kind: "approval", decision: "deny")
        case .answer(let answers): WireResponseBody(kind: "answer", answers: answers)
        }
    }
}

struct SessionSnapshot {
    let workstations: [Workstation]
    let sessions: [AgentSession]
}

protocol SessionClient: Sendable {
    func snapshot() async throws -> SessionSnapshot
    func history(for session: AgentSession.ID, cursor: String?) async throws -> HistoryPage
    func submit(_ reply: InteractionReply, session: AgentSession.ID, interaction: Interaction) async throws -> InteractionState
}

enum SessionClientError: LocalizedError {
    case unavailable, resolved, invalidReply
    case uncertain

    var errorDescription: String? {
        switch self {
        case .unavailable: "工作站离线或会话已不可用。"
        case .resolved: "这项请求已经处理。"
        case .invalidReply: "请检查回复内容。"
        case .uncertain: "结果尚未确认。再次提交同一回复前，会先向工作站查询状态。"
        }
    }
}

actor DemoSessionClient: SessionClient {
    private let workstations = [
        Workstation(id: "studio", name: "Linux 工作站", isOnline: true),
        Workstation(id: "lab", name: "备用工作站", isOnline: false),
    ]

    private var sessions = [
        AgentSession(id: "relay", workstationID: "studio", title: "检查中转服务测试",
                     directory: "~/dev/agent-done", status: .approval,
                     interactions: [Interaction(id: "allow-test", prompt: .approval(ApprovalPrompt(
                        summary: "运行项目测试并生成报告。", action: "go test ./...", canApprove: true
                     )))]),
        AgentSession(id: "mobile", workstationID: "studio", title: "整理 iPhone 客户端流程",
                     directory: "~/dev/agent-done", status: .question,
                     interactions: [Interaction(id: "first-screen", prompt: .question([
                        SessionQuestion(id: "initial-view", text: "打开 App 时，优先显示哪个页面？",
                                        options: ["待处理请求", "最近会话"], allowsText: true)
                     ]))]),
        AgentSession(id: "docs", workstationID: "studio", title: "更新开发文档",
                     directory: "~/dev/agent-done", status: .completed, interactions: []),
        AgentSession(id: "archive", workstationID: "lab", title: "核对归档记录",
                     directory: "~/dev/archive", status: .completed, interactions: []),
    ]

    private var histories: [String: [HistoryEntry]] = [
        "relay": [
            HistoryEntry(id: "4", author: "Codex", text: "我准备运行 go test ./...，正在等待你的授权。"),
            HistoryEntry(id: "3", author: "你", text: "顺便验证现有 Go 测试。"),
            HistoryEntry(id: "2", author: "Codex", text: "我正在核对工作站隔离和断线重连的测试场景。"),
            HistoryEntry(id: "1", author: "你", text: "检查中转服务的测试覆盖。"),
        ],
        "mobile": [
            HistoryEntry(id: "3", author: "Codex", text: "默认打开待处理列表，还是最近的会话列表？"),
            HistoryEntry(id: "2", author: "Codex", text: "会话详情可以集中显示上下文和待处理请求。"),
            HistoryEntry(id: "1", author: "你", text: "梳理 iPhone 上查看和处理会话的流程。"),
        ],
        "docs": [HistoryEntry(id: "1", author: "Codex", text: "开发文档已更新，包含本地安装和回退说明。")],
        "archive": [HistoryEntry(id: "1", author: "Codex", text: "归档记录已核对。")],
    ]

    func snapshot() -> SessionSnapshot {
        SessionSnapshot(workstations: workstations, sessions: sessions)
    }

    func history(for session: AgentSession.ID, cursor: String?) throws -> HistoryPage {
        guard let entries = histories[session] else { throw SessionClientError.unavailable }
        var remaining = entries[...]
        if let cursor {
            guard let position = entries.firstIndex(where: { $0.id == cursor }) else {
                throw SessionClientError.unavailable
            }
            remaining = entries.dropFirst(position + 1)
        }
        let page = Array(remaining.prefix(2))
        return HistoryPage(entries: page, nextCursor: remaining.count > page.count ? page.last?.id : nil)
    }

    func submit(_ reply: InteractionReply, session: AgentSession.ID, interaction: Interaction) throws -> InteractionState {
        guard let index = sessions.firstIndex(where: { $0.id == session }),
              workstations.contains(where: { $0.id == sessions[index].workstationID && $0.isOnline })
        else { throw SessionClientError.unavailable }

        guard let request = sessions[index].interactions.first(where: { $0.id == interaction.id })
        else { throw SessionClientError.resolved }

        let text: String
        switch (request.prompt, reply) {
        case (.approval, .approve): text = "已允许执行。"
        case (.approval, .decline): text = "已拒绝执行。"
        case (.question(let questions), .answer(let answers)):
            text = questions.compactMap { answers[$0.id]?.joined(separator: "、") }.joined(separator: "\n")
            guard !text.isEmpty else { throw SessionClientError.invalidReply }
        default:
            throw SessionClientError.invalidReply
        }

        histories[session, default: []].insert(HistoryEntry(id: UUID().uuidString, author: "你", text: text), at: 0)
        sessions[index].status = .running
        sessions[index].interactions.removeAll { $0.id == interaction.id }
        return .applied
    }
}
