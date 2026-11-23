import CryptoKit
import Foundation

actor RemoteSessionClient: SessionClient {
    let connection: RelayConnection
    private let workstationID: String
    private let intentStorage: String
    private let messageStorage: String
    private let creationStorage: String
    private var creatingSession = false
    private var canCreateSession = false
    private var intents: [String: ReplyQuery]
    private var messageDrafts: [String: MessageDraft]
    private var activeMessageOperations = Set<String>()
    private var confirmed: [String: InteractionState] = [:]
    private var cachedSessions: [AgentSession] = []

    init(pairing: Pairing, key: Curve25519.KeyAgreement.PrivateKey, session: URLSession? = nil) throws {
        connection = try RelayConnection(pairing: pairing, local: key, session: session)
        workstationID = pairing.workstationID
        let identity = "\(pairing.phoneID)\n\(pairing.workstationID)\n\(pairing.workstationKey)"
        let digest = SHA256.hash(data: Data(identity.utf8)).map { String(format: "%02x", $0) }.joined()
        intentStorage = PairingVault.intents + "." + digest
        messageStorage = "messages." + digest
        creationStorage = "session-creation." + digest
        intents = try PairingVault.load([String: ReplyQuery].self, name: intentStorage) ?? [:]
        messageDrafts = try PairingVault.load([String: MessageDraft].self, name: messageStorage) ?? [:]
    }

    func snapshot() async throws -> SessionSnapshot {
        guard try await connection.presence() else {
            return result(online: false)
        }
        let sessions: [WireSession] = try await allPages("sessions.list", as: WireSession.self)
        let pending: [WireInteraction] = try await allPages("interactions.snapshot", as: WireInteraction.self)
        if let capabilities = try? await connection.request("workstation.capabilities", params: [String: String](),
                                                            as: WorkstationCapabilities.self) {
            canCreateSession = capabilities.canCreateSession
        }
        let active = Set(pending.map(\.interactionId))
        let retired = confirmed.keys.filter { !active.contains($0) }
        if !retired.isEmpty {
            var next = intents
            for id in retired { next.removeValue(forKey: id) }
            try? persist(next)
            for id in retired { confirmed.removeValue(forKey: id) }
        }
        let interactions = Dictionary(grouping: pending, by: \.sessionId)
        var seen = Set<String>()
        cachedSessions = sessions.compactMap { session in
            guard seen.insert(session.sessionId).inserted else { return nil }
            var seenInteractions = Set<String>()
            let cards = interactions[session.sessionId, default: []].compactMap { item in
                seenInteractions.insert(item.interactionId).inserted ? Self.card(item) : nil
            }
            return AgentSession(id: session.sessionId, workstationID: workstationID, title: session.title, directory: session.cwd,
                                updatedAt: Date(timeIntervalSince1970: Double(session.updatedAtMs) / 1000),
                                status: Self.status(session.state, cards: cards), interactions: cards,
                                canSendMessage: session.canSendMessage == true, canAutoApprove: session.canAutoApprove == true)
        }
        return result(online: true)
    }

    func createSession() async throws -> String {
        guard canCreateSession, !creatingSession else { throw SessionClientError.unavailable }
        creatingSession = true
        defer { creatingSession = false }

        let intent: SessionCreationIntent
        if let pending = try PairingVault.load(SessionCreationIntent.self, name: creationStorage) {
            guard let status = try? await creationOutcome("sessions.create.status", intent: pending) else {
                throw SessionClientError.sessionCreationUnconfirmed
            }
            switch status.status {
            case .created:
                return try finishCreation(status)
            case .unknown:
                throw SessionClientError.sessionCreationUnconfirmed
            case .notReceived:
                intent = pending
            case .expired:
                intent = try newCreationIntent()
            }
        } else {
            intent = try newCreationIntent()
        }

        let outcome: SessionCreationOutcome
        do {
            outcome = try await creationOutcome("sessions.create", intent: intent)
        } catch WireError.control(let code) {
            if ["invalid_request", "unavailable", "forbidden"].contains(code) {
                try PairingVault.remove(creationStorage)
            }
            throw WireError.control(code)
        } catch {
            guard let checked = try? await creationOutcome("sessions.create.status", intent: intent) else {
                throw SessionClientError.sessionCreationUnconfirmed
            }
            return try finishCreation(checked)
        }
        return try finishCreation(outcome)
    }

    func discardSessionCreation() throws {
        guard !creatingSession else { throw SessionClientError.unavailable }
        try PairingVault.remove(creationStorage)
    }

    func autoApprove(for session: String, enabled: Bool? = nil) async throws -> Bool {
        let method = enabled == nil ? "sessions.auto_approve.get" : "sessions.auto_approve.set"
        var query = AutoApproveQuery(sessionId: session, enabled: enabled)
        if enabled != nil {
            query.idempotencyKey = UUID().uuidString
            query.expiresAtMs = Int64((Date.now.timeIntervalSince1970 + 120) * 1000)
        }
        let outcome = try await connection.request(method, params: query, as: AutoApproveOutcome.self)
        guard outcome.sessionId == session else { throw WireError.invalidMessage }
        if let enabled = outcome.enabled { return enabled }
        throw WireError.control(outcome.status ?? "unavailable")
    }

    private func newCreationIntent() throws -> SessionCreationIntent {
        let intent = SessionCreationIntent(idempotencyKey: UUID().uuidString,
                                           expiresAtMs: Int64((Date.now.timeIntervalSince1970 + 120) * 1000))
        try PairingVault.save(intent, name: creationStorage)
        return intent
    }

    private func creationOutcome(_ method: String, intent: SessionCreationIntent) async throws -> SessionCreationOutcome {
        try await connection.request(method, params: intent, as: SessionCreationOutcome.self)
    }

    private func finishCreation(_ outcome: SessionCreationOutcome) throws -> String {
        if outcome.status == .expired {
            try PairingVault.remove(creationStorage)
            throw SessionClientError.unavailable
        }
        guard outcome.status == .created else { throw SessionClientError.sessionCreationUnconfirmed }
        guard let session = outcome.sessionId, !session.isEmpty else { throw WireError.invalidMessage }
        try PairingVault.remove(creationStorage)
        return session
    }

    func history(for session: AgentSession.ID, cursor: String?) async throws -> HistoryPage {
        let page = try await connection.request("history.page", params: PageQuery(sessionId: session, cursor: cursor),
                                                as: ControlPage<WireHistory>.self)
        guard page.nextCursor == nil || page.nextCursor != cursor else { throw WireError.invalidMessage }
        let entries = page.items.map {
            HistoryEntry(id: $0.messageId, author: $0.role == "user" ? "你" : "Codex",
                         text: $0.text, truncated: $0.truncated)
        }
        return HistoryPage(entries: entries, nextCursor: page.nextCursor)
    }

    func pendingMessage(for session: String) -> String? { messageDrafts[session]?.text }

    func sendMessage(_ text: String, to session: String) async throws -> MessageDelivery {
        guard activeMessageOperations.insert(session).inserted else { throw SessionClientError.unavailable }
        defer { activeMessageOperations.remove(session) }

        let text = try MessageDraft.validatedText(text)
        let draft: MessageDraft
        if let previous = messageDrafts[session] {
            guard previous.text == text else { throw SessionClientError.previousMessageUnconfirmed }
            let current = try await messageStatus(previous)
            guard current == .notReceived else { return current }
            guard cachedSessions.contains(where: { $0.id == session && $0.acceptsMessage }) else {
                throw SessionClientError.unavailable
            }
            draft = previous
        } else {
            guard cachedSessions.contains(where: { $0.id == session && $0.acceptsMessage }) else {
                throw SessionClientError.unavailable
            }
            draft = MessageDraft(sessionId: session, idempotencyKey: UUID().uuidString,
                                 expiresAtMs: Int64((Date.now.timeIntervalSince1970 + 120) * 1000), text: text)
            try saveMessageDraft(draft, for: session)
        }

        do {
            let outcome = try await connection.request("messages.send", params: draft, as: MessageOutcome.self)
            return try resolveMessage(outcome, for: session)
        } catch WireError.control(let code) {
            if ["invalid_request", "forbidden", "not_found", "unavailable", "busy"].contains(code) {
                try saveMessageDraft(nil, for: session)
            }
            throw WireError.control(code)
        } catch {
            // A transport failure can happen after the workstation accepts the message.
            return (try? await messageStatus(draft)) ?? .unknown
        }
    }

    func checkMessageStatus(for session: String) async throws -> MessageDelivery? {
        guard let draft = messageDrafts[session] else { return nil }
        guard activeMessageOperations.insert(session).inserted else { throw SessionClientError.unavailable }
        defer { activeMessageOperations.remove(session) }
        return try await messageStatus(draft)
    }

    private func messageStatus(_ draft: MessageDraft) async throws -> MessageDelivery {
        let query = MessageStatusQuery(sessionId: draft.sessionId, idempotencyKey: draft.idempotencyKey,
                                       expiresAtMs: draft.expiresAtMs)
        let result = try await connection.request("messages.status", params: query, as: MessageOutcome.self)
        return try resolveMessage(result, for: draft.sessionId)
    }

    private func resolveMessage(_ outcome: MessageOutcome, for session: String) throws -> MessageDelivery {
        guard outcome.sessionId == session else { throw WireError.invalidMessage }
        if outcome.status == .accepted || outcome.status == .expired { try saveMessageDraft(nil, for: session) }
        return outcome.status
    }

    private func saveMessageDraft(_ draft: MessageDraft?, for session: String) throws {
        var updated = messageDrafts
        updated[session] = draft
        try PairingVault.save(updated, name: messageStorage)
        messageDrafts = updated
    }

    func submit(_ reply: InteractionReply, session: AgentSession.ID, interaction: Interaction) async throws -> InteractionState {
        if let confirmed = confirmed[interaction.id] { return confirmed }
        try validate(reply, interaction: interaction)
        let body = reply.wire
        let request: ReplyQuery
        if let prior = intents[interaction.id] {
            guard prior.sessionId == session, prior.response == body else {
                throw WireError.control("conflict")
            }
            var lookup = prior
            lookup.response = nil
            let status = try await outcome("interaction.status", lookup)
            if status != .pending { return status }
            request = prior
        } else {
            request = ReplyQuery(sessionId: session, interactionId: interaction.id,
                                 idempotencyKey: UUID().uuidString, response: body)
            // Record the intent before any control message can leave this device.
            var next = intents
            next[interaction.id] = request
            try persist(next)
        }

        do {
            return try await outcome("interaction.respond", request)
        } catch WireError.control(let code) {
            throw WireError.control(code)
        } catch {
            // Never replay a control message after an uncertain delivery.
            var lookup = request
            lookup.response = nil
            if let status = try? await outcome("interaction.status", lookup) { return status }
            throw SessionClientError.uncertain
        }
    }

    func checkStatus(session: String, interaction: String) async throws -> InteractionState {
        if let confirmed = confirmed[interaction] { return confirmed }
        if var lookup = intents[interaction], lookup.sessionId == session {
            lookup.response = nil
            return try await outcome("interaction.status", lookup)
        }
        let result = try await connection.request("interaction.status", params: StatusQuery(sessionId: session,
                                                  interactionId: interaction), as: WireOutcome.self)
        guard result.interactionId == interaction else { throw WireError.invalidMessage }
        return result.status
    }

    private func validate(_ reply: InteractionReply, interaction: Interaction) throws {
        guard !interaction.isExpired else { throw WireError.unsafeApproval }
        switch (interaction.prompt, reply) {
        case (.approval(let prompt), .approve):
            guard prompt.canApprove else { throw WireError.unsafeApproval }
        case (.approval, .decline):
            break
        case (.question(let questions), .answer(let answers)):
            guard answers.count == questions.count, questions.allSatisfy({ question in
                guard let values = answers[question.id], values.count == 1, let value = values.first,
                      !value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                else { return false }
                return question.allowsText || question.options.contains(value)
            }) else { throw SessionClientError.invalidReply }
        default:
            throw SessionClientError.invalidReply
        }
    }

    private func outcome(_ method: String, _ request: ReplyQuery) async throws -> InteractionState {
        let result = try await connection.request(method, params: request, as: WireOutcome.self)
        guard result.interactionId == request.interactionId else { throw WireError.invalidMessage }
        switch result.status {
        case .applied, .closed, .expired:
            confirmed[request.interactionId] = result.status
        case .submitted, .pending, .unknown: break
        }
        return result.status
    }

    private func persist(_ next: [String: ReplyQuery]) throws {
        try PairingVault.save(next, name: intentStorage)
        intents = next
    }

    private func allPages<Item: Decodable & Sendable>(_ method: String, as: Item.Type) async throws -> [Item] {
        var items: [Item] = []
        var cursor: String?
        var visited = Set<String>()
        repeat {
            let page = try await connection.request(method, params: PageQuery(cursor: cursor),
                                                    as: ControlPage<Item>.self)
            items += page.items
            cursor = page.nextCursor
            if let cursor, !visited.insert(cursor).inserted { throw WireError.invalidMessage }
        } while cursor != nil
        return items
    }

    private func result(online: Bool) -> SessionSnapshot {
        SessionSnapshot(workstations: [Workstation(id: workstationID, name: workstationID, isOnline: online, canCreateSession: canCreateSession)],
                        sessions: cachedSessions)
    }

    private static func card(_ wire: WireInteraction) -> Interaction? {
        let prompt: Interaction.Prompt
        switch wire.kind {
        case "approval":
            guard let summary = wire.summary, let canApprove = wire.canApprove else { return nil }
            let action: String?
            switch wire.details?.kind {
            case "command": action = wire.details?.command
            case "file_change": action = wire.details?.changes
            default: action = nil
            }
            prompt = .approval(ApprovalPrompt(summary: summary, action: action, reason: wire.details?.reason,
                                              directory: wire.details?.cwd,
                                              canApprove: canApprove && action?.isEmpty == false && wire.details?.cwd.isEmpty == false))
        case "question":
            guard let questions = wire.questions, !questions.isEmpty,
                  Set(questions.map(\.id)).count == questions.count else { return nil }
            prompt = .question(questions)
        default:
            return nil
        }
        return Interaction(id: wire.interactionId, prompt: prompt,
                           expiresAt: wire.expiresAtMs.map { Date(timeIntervalSince1970: Double($0) / 1000) })
    }

    private static func status(_ state: String, cards: [Interaction]) -> SessionStatus {
        for card in cards {
            if case .approval = card.prompt { return .approval }
        }
        if !cards.isEmpty { return .question }
        switch state {
        case "running": return .running
        case "waiting": return .waiting
        case "closed": return .closed
        default: return .idle
        }
    }
}
