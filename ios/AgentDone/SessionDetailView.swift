import SwiftUI

struct SessionDetailView: View {
    let store: SessionStore
    let sessionID: AgentSession.ID
    let theme: WorkspaceTheme
    let palette: ThemePalette

    @State private var history: [HistoryEntry] = []
    @State private var nextCursor: String?
    @State private var loadingHistory = false
    @State private var historyError: String?
    @State private var interactionNotice: String?
    @State private var message = ""
    @State private var pendingMessage = false
    @State private var messageNotice: String?
    @State private var checkingMessage = false
    @State private var historyLoad = UUID()

    private var session: AgentSession? { store.sessions.first { $0.id == sessionID } }

    var body: some View {
        Group {
            if let session {
                List {
                    Section {
                        VStack(alignment: .leading, spacing: 8) {
                            StatusLabel(status: session.status, palette: palette)
                            Text(session.title)
                                .font(.system(.title2, design: theme.headingDesign).bold())
                                .accessibilityAddTraits(.isHeader)
                            if let directory = session.directory {
                                Label(directory, systemImage: "folder")
                                    .font(.subheadline.monospaced()).foregroundStyle(palette.muted)
                            }
                        }
                        .padding(.vertical, 12)
                        .listRowBackground(palette.surface)
                        if let interactionNotice {
                            Label(interactionNotice, systemImage: "info.circle")
                                .font(.subheadline)
                                .foregroundStyle(palette.accent)
                                .listRowBackground(palette.surface)
                        }
                    }
                    ForEach(session.interactions) { interaction in
                        InteractionSection(store: store, session: session, interaction: interaction, palette: palette) {
                            reply in
                            Task {
                                interactionNotice = nil
                                let shouldReload = await store.submit(reply, session: session.id, interaction: interaction)
                                if let outcome = store.outcomes[interaction.id],
                                   store.sessions.first(where: { $0.id == sessionID })?.interactions.contains(where: { $0.id == interaction.id }) != true {
                                    interactionNotice = outcome.description
                                }
                                if shouldReload { await loadHistory() }
                            }
                        }
                    }
                    historySection
                }
                .listStyle(.insetGrouped)
                .scrollContentBackground(.hidden)
                .refreshable {
                    await store.refresh(showErrors: true)
                    await loadHistory()
                }
                .safeAreaInset(edge: .bottom, spacing: 0) {
                    if session.canSendMessage || pendingMessage { composer(for: session) }
                }
            } else {
                ContentUnavailableView("会话不可用", systemImage: "text.bubble")
            }
        }
        .foregroundStyle(palette.ink)
        .background(palette.canvas)
        .navigationTitle("会话详情")
        .navigationBarTitleDisplayMode(.inline)
        .task(id: session?.updatedAt) { await loadHistory() }
        .task(id: store.generation) { await restorePendingMessage() }
    }

    private func composer(for session: AgentSession) -> some View {
        let online = store.workstations.contains { $0.id == session.workstationID && $0.isOnline }
        let busy = store.submitting || store.refreshing || checkingMessage
        let enabled = online && session.acceptsMessage && !busy
        return VStack(alignment: .leading, spacing: 8) {
            if let messageNotice {
                Text(messageNotice).font(.footnote).foregroundStyle(palette.muted)
            } else if !online || !session.acceptsMessage {
                Text(online ? "当前回合结束后可以继续。" : "工作站在线后可以继续。")
                    .font(.footnote).foregroundStyle(palette.muted)
            }
            if pendingMessage {
                Button("查询发送状态") { Task { await checkMessageStatus() } }
                    .disabled(!online || busy)
            }
            HStack(alignment: .bottom, spacing: 8) {
                TextField("继续问 Codex", text: $message, axis: .vertical)
                    .accessibilityIdentifier("session-message-input")
                    .lineLimit(1...5)
                    .padding(10)
                    .background(palette.canvas, in: RoundedRectangle(cornerRadius: 10))
                    .disabled(pendingMessage || store.submitting)
                Button {
                    Task { await sendMessage() }
                } label: {
                    Image(systemName: "arrow.up.circle.fill")
                        .font(.title2)
                        .frame(minWidth: 44, minHeight: 44)
                }
                .accessibilityLabel(pendingMessage ? "重试发送" : "发送消息")
                .disabled(!enabled || message.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .padding(12)
        .background(palette.surface)
    }

    private func sendMessage() async {
        messageNotice = nil
        do {
            let result = try await store.sendMessage(message, to: sessionID)
            await showDelivery(result)
        } catch is CancellationError {
            return
        } catch {
            await restorePendingMessage()
            messageNotice = error.localizedDescription
        }
    }

    private func restorePendingMessage() async {
        if let pending = await store.remote?.pendingMessage(for: sessionID) {
            message = pending
            pendingMessage = true
            messageNotice = "这条消息尚未确认。"
        } else {
            pendingMessage = false
        }
    }

    private func checkMessageStatus() async {
        guard let remote = store.remote, !checkingMessage else { return }
        checkingMessage = true
        defer { checkingMessage = false }
        do {
            if let result = try await remote.checkMessageStatus(for: sessionID) { await showDelivery(result) }
        } catch {
            messageNotice = error.localizedDescription
        }
    }

    private func showDelivery(_ delivery: MessageDelivery) async {
        switch delivery {
        case .accepted:
            message = ""
            pendingMessage = false
            messageNotice = "工作站已接收消息。"
            await loadHistory()
        case .notReceived:
            pendingMessage = true
            messageNotice = "工作站尚未接收，可以重试发送。"
        case .expired:
            pendingMessage = false
            messageNotice = "这次发送已过期，可以重新发送。"
        case .unknown:
            pendingMessage = true
            messageNotice = "工作站尚未确认，请查询发送状态。"
        }
    }

    private var historySection: some View {
        Section("最近消息") {
            if let historyError {
                Text(historyError).foregroundStyle(palette.muted)
                Button("重试加载") { Task { await loadHistory() } }
            }
            ForEach(history) { entry in
                HistoryRow(entry: entry, theme: theme, palette: palette)
                    .listRowBackground(theme == .workbench ? palette.surface : palette.canvas)
                    .listRowSeparatorTint(palette.rule)
            }
            if loadingHistory { ProgressView() }
            if !loadingHistory && history.isEmpty && historyError == nil {
                Text("此会话暂无历史消息").foregroundStyle(palette.muted)
            }
            if let nextCursor {
                Button("加载更早的消息") { Task { await loadHistory(cursor: nextCursor) } }
                    .disabled(loadingHistory)
            }
        }
    }

    private func loadHistory(cursor: String? = nil) async {
        guard !loadingHistory || cursor == nil else { return }
        let generation = store.generation
        let load = UUID()
        historyLoad = load
        loadingHistory = true
        defer { if historyLoad == load { loadingHistory = false } }
        do {
            let page = try await store.client.history(for: sessionID, cursor: cursor)
            guard historyLoad == load, store.generation == generation, !Task.isCancelled else { return }
            var seen = Set<String>()
            let entries = cursor == nil ? page.entries : history + page.entries
            history = entries.filter { seen.insert($0.id).inserted }
            nextCursor = page.nextCursor
            historyError = nil
        } catch {
            guard historyLoad == load, store.generation == generation, !Task.isCancelled else { return }
            historyError = error.localizedDescription
        }
    }
}

private struct HistoryRow: View {
    let entry: HistoryEntry
    let theme: WorkspaceTheme
    let palette: ThemePalette

    var body: some View {
        let isUser = entry.author == "你"
        VStack(alignment: .leading, spacing: theme == .journal ? 10 : 7) {
            Label(entry.author, systemImage: authorIcon(isUser: isUser))
                .font(.system(.subheadline, design: theme.headingDesign).weight(.semibold))
                .foregroundStyle(isUser ? palette.accent : palette.muted)
                .accessibilityAddTraits(.isHeader)
            Text(entry.text)
                .font(.system(.body, design: theme == .journal ? .serif : .default))
                .lineSpacing(theme == .journal ? 5 : 2)
                .textSelection(.enabled)
            if entry.truncated {
                Text("部分内容已截断").font(.footnote).foregroundStyle(palette.muted)
            }
        }
        .padding(.vertical, theme == .journal ? 12 : 7)
    }

    private func authorIcon(isUser: Bool) -> String {
        if theme == .terminal { return isUser ? "chevron.right" : "terminal" }
        return isUser ? "person" : "text.bubble"
    }
}

private struct InteractionSection: View {
    let store: SessionStore
    let session: AgentSession
    let interaction: Interaction
    let palette: ThemePalette
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
        Section {
            switch interaction.prompt {
            case .approval(let prompt): approval(prompt)
            case .question(let questions): question(questions)
            }
            if let result = store.outcomes[interaction.id] {
                Label(result.description, systemImage: "info.circle").foregroundStyle(palette.muted)
            }
            if !online {
                Label("工作站离线", systemImage: "wifi.slash").foregroundStyle(palette.muted)
            } else if interaction.isExpired {
                Text("此请求可能已过期").foregroundStyle(palette.muted)
            }
            if online && !store.isDemo {
                Button("查询处理状态") {
                    Task { await store.checkStatus(session: session.id, interaction: interaction) }
                }
            }
        } header: {
            switch interaction.prompt {
            case .approval: Label("等待审批", systemImage: "lock.shield")
            case .question: Label("等待回答", systemImage: "questionmark.bubble")
            }
        }
        .listRowBackground(palette.surface)
        .disabled(store.submitting || store.refreshing)
    }

    private func approval(_ prompt: ApprovalPrompt) -> some View {
        Group {
            Text(prompt.summary).font(.headline)
            if let reason = prompt.reason { Text(reason).foregroundStyle(palette.muted) }
            if let action = prompt.action {
                VStack(alignment: .leading, spacing: 8) {
                    if let directory = prompt.directory {
                        Label(directory, systemImage: "folder")
                            .font(.footnote.monospaced())
                            .foregroundStyle(palette.muted)
                    }
                    Text(action).font(.body.monospaced()).textSelection(.enabled)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(12)
                .background(palette.canvas, in: RoundedRectangle(cornerRadius: 8))
            } else if let directory = prompt.directory {
                Text(directory).font(.footnote.monospaced()).foregroundStyle(palette.muted)
            }
            if allowsReply {
                if prompt.canApprove {
                    Button {
                        approving = true
                        confirmation = true
                    } label: {
                        Label("仅允许一次", systemImage: "checkmark.circle")
                            .font(.body.weight(.semibold))
                            .foregroundStyle(palette.accent)
                            .frame(minHeight: 44)
                    }
                } else {
                    Text("审批明细不足，无法允许执行。").foregroundStyle(palette.muted)
                }
                Button(role: .destructive) {
                    approving = false
                    confirmation = true
                } label: {
                    Label("拒绝执行", systemImage: "xmark.circle")
                        .frame(minHeight: 44)
                }
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
                            .frame(minHeight: 44)
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
                Button {
                    submit(.answer(cleaned.mapValues { [$0] }))
                } label: {
                    Label("提交回答", systemImage: "arrow.up.circle")
                        .font(.body.weight(.semibold))
                        .foregroundStyle(palette.accent)
                        .frame(minHeight: 44)
                }
                    .disabled(!questions.allSatisfy { cleaned[$0.id]?.isEmpty == false })
            }
        }
    }
}
