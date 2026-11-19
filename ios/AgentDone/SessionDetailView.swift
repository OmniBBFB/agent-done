import SwiftUI

struct SessionDetailView: View {
    let store: SessionStore
    let sessionID: AgentSession.ID

    @State private var history: [HistoryEntry] = []
    @State private var nextCursor: String?
    @State private var loadingHistory = false
    @State private var historyError: String?

    var body: some View {
        Group {
            if let session = store.sessions.first(where: { $0.id == sessionID }) {
                List {
                    Section {
                        VStack(alignment: .leading, spacing: 8) {
                            StatusLabel(status: session.status)
                            Text(session.title).font(.title2.bold())
                            if let directory = session.directory {
                                Text(directory).font(.subheadline.monospaced()).foregroundStyle(.secondary)
                            }
                        }
                        .padding(.vertical, 8)
                    }
                    ForEach(session.interactions) { interaction in
                        InteractionSection(store: store, session: session, interaction: interaction) { reply in
                            Task {
                                if await store.submit(reply, session: session.id, interaction: interaction) {
                                    await loadHistory()
                                }
                            }
                        }
                    }
                    historySection
                }
                .refreshable {
                    await store.refresh(showErrors: true)
                    await loadHistory()
                }
            } else {
                ContentUnavailableView("会话不可用", systemImage: "text.bubble")
            }
        }
        .navigationTitle("会话详情")
        .navigationBarTitleDisplayMode(.inline)
        .task(id: store.generation) { await loadHistory() }
    }

    private var historySection: some View {
        Section("会话历史") {
            if let historyError {
                Text(historyError).foregroundStyle(.secondary)
                Button("重试加载") { Task { await loadHistory() } }
            }
            ForEach(history) { entry in
                VStack(alignment: .leading, spacing: 5) {
                    Text(entry.author).font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                    Text(entry.text).textSelection(.enabled)
                    if entry.truncated {
                        Text("部分内容已截断").font(.caption).foregroundStyle(.secondary)
                    }
                }
                .padding(.vertical, 3)
            }
            if loadingHistory { ProgressView() }
            if let nextCursor {
                Button("加载更早的消息") { Task { await loadHistory(cursor: nextCursor) } }
                    .disabled(loadingHistory)
            }
        }
    }

    private func loadHistory(cursor: String? = nil) async {
        guard !loadingHistory else { return }
        let generation = store.generation
        loadingHistory = true
        defer { loadingHistory = false }
        do {
            let page = try await store.client.history(for: sessionID, cursor: cursor)
            guard store.generation == generation, !Task.isCancelled else { return }
            var seen = Set<String>()
            let entries = cursor == nil ? page.entries : history + page.entries
            history = entries.filter { seen.insert($0.id).inserted }
            nextCursor = page.nextCursor
            historyError = nil
        } catch {
            guard store.generation == generation, !Task.isCancelled else { return }
            historyError = error.localizedDescription
        }
    }
}

private struct InteractionSection: View {
    let store: SessionStore
    let session: AgentSession
    let interaction: Interaction
    let submit: (InteractionReply) -> Void

    @State private var answers: [String: String] = [:]
    @State private var approving = false
    @State private var confirmation = false

    private var online: Bool {
        store.workstations.contains { $0.id == session.workstationID && $0.isOnline }
    }

    private var allowsReply: Bool {
        guard online, !interaction.isExpired else { return false }
        switch store.outcomes[interaction.id] {
        case nil, .pending: return true
        default: return false
        }
    }

    var body: some View {
        Section("等待你的回复") {
            switch interaction.prompt {
            case .approval(let prompt): approval(prompt)
            case .question(let questions): question(questions)
            }
            if let result = store.outcomes[interaction.id] {
                Text(result.description).foregroundStyle(.secondary)
            }
            if !online {
                Label("工作站离线", systemImage: "wifi.slash").foregroundStyle(.secondary)
            } else if interaction.isExpired {
                Text("此请求可能已过期").foregroundStyle(.secondary)
            }
            if online && !store.isDemo {
                Button("查询处理状态") {
                    Task { await store.checkStatus(session: session.id, interaction: interaction) }
                }
            }
        }
        .disabled(store.submitting || store.refreshing)
    }

    private func approval(_ prompt: ApprovalPrompt) -> some View {
        Group {
            Text(prompt.summary)
            if let reason = prompt.reason { Text(reason) }
            if let directory = prompt.directory { Text(directory).font(.caption.monospaced()) }
            if let action = prompt.action {
                Text(action).font(.body.monospaced()).textSelection(.enabled)
            }
            if allowsReply {
                if prompt.canApprove {
                    Button("仅允许一次") { approving = true; confirmation = true }
                } else {
                    Text("审批明细不足，无法允许执行。").foregroundStyle(.secondary)
                }
                Button("拒绝执行", role: .destructive) { approving = false; confirmation = true }
                    .confirmationDialog("提交审批决定？", isPresented: $confirmation, titleVisibility: .visible) {
                        if approving {
                            Button("确认允许一次") { submit(.approve) }
                        } else {
                            Button("确认拒绝", role: .destructive) { submit(.decline) }
                        }
                    }
            }
        }
    }

    private func question(_ questions: [SessionQuestion]) -> some View {
        Group {
            ForEach(questions) { question in
                Text(question.text).font(.headline)
                if allowsReply {
                    ForEach(Array(question.options.enumerated()), id: \.offset) { _, choice in
                        Button {
                            answers[question.id] = choice
                        } label: {
                            HStack {
                                Text(choice)
                                Spacer()
                                if answers[question.id] == choice {
                                    Image(systemName: "checkmark").accessibilityHidden(true)
                                }
                            }
                        }
                        .accessibilityAddTraits(answers[question.id] == choice ? .isSelected : [])
                    }
                    if question.allowsText {
                        TextField("填写回答", text: Binding(
                            get: { answers[question.id] ?? "" },
                            set: { answers[question.id] = $0 }
                        ), axis: .vertical)
                        .lineLimit(2...5)
                    }
                }
            }
            if allowsReply {
                let cleaned = answers.mapValues { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
                Button("提交回答") { submit(.answer(cleaned.mapValues { [$0] })) }
                    .disabled(!questions.allSatisfy { cleaned[$0.id]?.isEmpty == false })
            }
        }
    }
}
