import SwiftUI

struct SessionDetailView: View {
    let store: SessionStore
    let sessionID: AgentSession.ID
    let palette: ThemePalette

    @State private var history: [HistoryEntry] = []
    @State private var nextCursor: String?
    @State private var loadingHistory = false
    @State private var historyError: String?
    @State private var interactionNotice: String?
    @State private var pendingMessage = false
    @State private var messageNotice: String?
    @State private var checkingMessage = false
    @State private var historyLoad = UUID()
    @State private var sentMessage = UUID()
    @State private var autoApprove: Bool?
    @State private var updatingApproval = false
    @State private var confirmsAutoApprove = false
    @FocusState private var composing: Bool

    private var session: AgentSession? { store.sessions.first { $0.id == sessionID } }
    private var approvalAvailable: Bool {
        guard let session, session.canAutoApprove else { return false }
        return store.workstations.contains { $0.id == session.workstationID && $0.isOnline }
    }

    private var message: Binding<String> {
        Binding(
            get: { store.messageDrafts[sessionID, default: ""] },
            set: { store.messageDrafts[sessionID] = $0.isEmpty ? nil : $0 }
        )
    }

    var body: some View {
        Group {
            if let session {
                ScrollViewReader { scroll in
                    List {
                        Section {
                            sessionHeading(session)
                                .listRowBackground(palette.surface)
                                .listRowSeparator(.hidden)
                            if let interactionNotice {
                                Label(interactionNotice, systemImage: "info.circle")
                                    .font(.subheadline)
                                    .foregroundStyle(palette.accent)
                                    .listRowBackground(palette.surface)
                            }
                        }
                        ForEach(session.interactions) { interaction in
                            Section {
                                InteractionCard(store: store, session: session, interaction: interaction, palette: palette) { reply in
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
                                .listRowInsets(EdgeInsets())
                            } header: {
                                WorkspaceSectionTitle(title: interaction.title, palette: palette)
                            }
                            .listRowBackground(palette.surface)
                        }
                        historySection
                    }
                    .listStyle(.plain)
                    .listSectionSpacing(WorkspaceStyle.sectionSpacing)
                    .scrollContentBackground(.hidden)
                    .scrollDismissesKeyboard(.interactively)
                    .onChange(of: sentMessage) {
                        if let latest = history.first {
                            scroll.scrollTo(latest.id, anchor: .bottom)
                        }
                    }
                    .refreshable {
                        await store.refresh(showErrors: true)
                        await loadHistory()
                        await loadAutoApprove()
                    }
                    .safeAreaInset(edge: .bottom, spacing: 0) {
                        if session.canSendMessage || pendingMessage { composer(for: session) }
                    }
                }
            } else {
                ContentUnavailableView("会话不可用", systemImage: "text.bubble")
            }
        }
        .foregroundStyle(palette.ink)
        .workspaceCanvas(palette)
        .navigationTitle("会话")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        .task(id: session?.updatedAt) { await loadHistory() }
        .task(id: store.generation) { await restorePendingMessage() }
        .task(id: approvalAvailable) { await loadAutoApprove() }
        .confirmationDialog("开启自动审批？", isPresented: $confirmsAutoApprove, titleVisibility: .visible) {
            Button("开启") { Task { await updateAutoApprove(true) } }
        } message: {
            Text("此会话后续可批准的命令请求会自动获得一次性批准。已待处理的请求仍需手动处理。")
        }
    }

    private func loadAutoApprove() async {
        guard !updatingApproval else { return }
        guard approvalAvailable, let remote = store.remote else {
            autoApprove = nil
            return
        }
        let enabled = try? await remote.autoApprove(for: sessionID)
        if !Task.isCancelled && approvalAvailable && !updatingApproval { autoApprove = enabled }
    }

    private func updateAutoApprove(_ enabled: Bool) async {
        guard approvalAvailable, !updatingApproval, let remote = store.remote else { return }
        let current = store.generation
        updatingApproval = true
        defer { updatingApproval = false }
        do {
            let result = try await remote.autoApprove(for: sessionID, enabled: enabled)
            if current == store.generation { autoApprove = result }
        } catch {
            let confirmed = try? await remote.autoApprove(for: sessionID)
            guard current == store.generation else { return }
            autoApprove = confirmed
            store.error = error.localizedDescription
        }
    }

    private func sessionHeading(_ session: AgentSession) -> some View {
        VStack(alignment: .leading, spacing: WorkspaceStyle.entrySpacing) {
            Text(session.title)
                .font(.title2.bold())
                .accessibilityAddTraits(.isHeader)
                .fixedSize(horizontal: false, vertical: true)
            if let workstation = store.workstations.first(where: { $0.id == session.workstationID }) {
                Label("Codex / \(workstation.name)", systemImage: "desktopcomputer")
                    .font(.subheadline)
                    .foregroundStyle(palette.muted)
            }
            if let directory = session.directory {
                Text(directory)
                    .font(.footnote.monospaced())
                    .foregroundStyle(palette.muted)
            }
            StatusLabel(status: session.status, palette: palette)
            if approvalAvailable {
                if let autoApprove {
                    Toggle("自动审批", isOn: Binding(get: { autoApprove }, set: { enabled in
                        if enabled { confirmsAutoApprove = true }
                        else { Task { await updateAutoApprove(false) } }
                    }))
                    .disabled(updatingApproval)
                } else {
                    Button("查询自动审批状态") { Task { await loadAutoApprove() } }
                        .disabled(updatingApproval)
                }
            }
        }
        .padding(.vertical, 12)
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
                TextField("补充指令", text: message, axis: .vertical)
                    .accessibilityIdentifier("session-message-input")
                    .focused($composing)
                    .lineLimit(1...5)
                    .padding(10)
                    .background(palette.canvas, in: RoundedRectangle(cornerRadius: WorkspaceStyle.surfaceRadius))
                    .disabled(pendingMessage || store.submitting)
                if composing {
                    Button {
                        composing = false
                    } label: {
                        Image(systemName: "keyboard.chevron.compact.down")
                            .frame(minWidth: 44, minHeight: 44)
                    }
                    .accessibilityLabel("收起键盘")
                }
                Button {
                    composing = false
                    Task { await sendMessage() }
                } label: {
                    Image(systemName: "arrow.up.circle.fill")
                        .font(.title2)
                        .frame(minWidth: 44, minHeight: 44)
                }
                .accessibilityLabel(pendingMessage ? "重试发送" : "发送消息")
                .disabled(!enabled || message.wrappedValue.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .padding(12)
        .background(palette.surface)
        .overlay(alignment: .top) {
            Rectangle().fill(palette.rule).frame(height: 1).accessibilityHidden(true)
        }
    }

    private func sendMessage() async {
        messageNotice = nil
        do {
            let result = try await store.sendMessage(message.wrappedValue, to: sessionID)
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
            message.wrappedValue = pending
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
            message.wrappedValue = ""
            pendingMessage = false
            messageNotice = "工作站已接收消息。"
            await loadHistory()
            sentMessage = UUID()
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
        Section {
            if let historyError {
                Text(historyError).foregroundStyle(palette.muted)
                Button("重试加载") { Task { await loadHistory() } }
            }
            if let nextCursor {
                Button("加载更早的消息") { Task { await loadHistory(cursor: nextCursor) } }
                    .disabled(loadingHistory)
            }
            ForEach(history.reversed()) { entry in
                HistoryRow(entry: entry, palette: palette)
                    .id(entry.id)
            }
            if loadingHistory {
                ProgressView().accessibilityLabel("正在加载消息")
            }
            if !loadingHistory && history.isEmpty && historyError == nil {
                Text("此会话暂无历史消息").foregroundStyle(palette.muted)
            }
        } header: {
            WorkspaceSectionTitle(title: "最近消息", palette: palette)
        }
        .listRowBackground(palette.surface)
        .listRowSeparatorTint(palette.rule)
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
    let palette: ThemePalette

    var body: some View {
        let isUser = entry.author == "你"
        VStack(alignment: .leading, spacing: WorkspaceStyle.entrySpacing) {
            Label(entry.author, systemImage: isUser ? "person" : "text.bubble")
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(isUser ? palette.accent : palette.muted)
                .accessibilityAddTraits(.isHeader)
            if isUser {
                Text(entry.text)
                    .font(.body)
                    .textSelection(.enabled)
            } else {
                MarkdownReply(source: entry.text, palette: palette)
            }
            if entry.truncated {
                Text("部分内容已截断").font(.footnote).foregroundStyle(palette.muted)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.vertical, WorkspaceStyle.entrySpacing)
    }
}

struct InteractionCard: View {
    @Environment(\.colorScheme) private var colorScheme

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
        VStack(alignment: .leading, spacing: 12) {
            switch interaction.prompt {
            case .approval(let prompt): approval(prompt)
            case .question(let questions): question(questions)
            }
            if let result = store.outcomes[interaction.id] {
                Label(result.description, systemImage: "info.circle")
                    .font(.subheadline)
                    .foregroundStyle(palette.muted)
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
                .buttonStyle(.borderless)
                .frame(minHeight: 44)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(14)
        .disabled(store.submitting || store.refreshing)
    }

    private func approval(_ prompt: ApprovalPrompt) -> some View {
        Group {
            VStack(alignment: .leading, spacing: WorkspaceStyle.entrySpacing) {
                Text(prompt.summary).font(.headline)
                if let reason = prompt.reason { Text(reason).foregroundStyle(palette.muted) }
            }
            .padding(.vertical, 5)
            if let action = prompt.action {
                VStack(alignment: .leading, spacing: 8) {
                    if let directory = prompt.directory {
                        Label(directory, systemImage: "folder")
                            .font(.footnote.monospaced())
                            .foregroundStyle(palette.muted)
                    }
                    Text(action)
                        .font(.body.monospaced())
                        .textSelection(.enabled)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(12)
                .background(palette.canvas, in: RoundedRectangle(cornerRadius: WorkspaceStyle.surfaceRadius))
            } else if let directory = prompt.directory {
                Text(directory).font(.footnote.monospaced()).foregroundStyle(palette.muted)
            }
            if allowsReply {
                if !prompt.canApprove {
                    Text("审批明细不足，无法允许执行。").foregroundStyle(palette.muted)
                }
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: 10) { approvalActions(canApprove: prompt.canApprove) }
                    VStack(spacing: 8) { approvalActions(canApprove: prompt.canApprove) }
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

    @ViewBuilder private func approvalActions(canApprove: Bool) -> some View {
        Button {
            approving = false
            confirmation = true
        } label: {
            Text("拒绝执行")
                .frame(maxWidth: .infinity, minHeight: 44)
        }
        .buttonStyle(.bordered)
        if canApprove {
            Button {
                approving = true
                confirmation = true
            } label: {
                Text("仅允许一次").bold()
                    .foregroundStyle(colorScheme == .dark ? palette.canvas : .white)
                    .frame(maxWidth: .infinity, minHeight: 44)
            }
            .buttonStyle(.borderedProminent)
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
                        .buttonStyle(.bordered)
                        .accessibilityAddTraits(answers[question.id] == choice ? .isSelected : [])
                    }
                    if question.allowsText {
                        TextField("填写回答", text: Binding(
                            get: { answers[question.id] ?? "" },
                            set: { answers[question.id] = $0 }
                        ), axis: .vertical)
                        .lineLimit(2...5)
                        .padding(10)
                        .background(palette.canvas, in: RoundedRectangle(cornerRadius: WorkspaceStyle.surfaceRadius))
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
                    .buttonStyle(.borderless)
                .disabled(!questions.allSatisfy { cleaned[$0.id]?.isEmpty == false })
            }
        }
    }
}
