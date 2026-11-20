import SwiftUI

struct WorkspaceView: View {
    private enum Tab: Hashable {
        case attention, workbench, conversation
    }

    private enum Filter: String, CaseIterable, Identifiable {
        case all = "全部"
        case running = "进行中"
        case completed = "已完成"

        var id: Self { self }

        func includes(_ status: SessionStatus) -> Bool {
            switch self {
            case .all: true
            case .running: status == .running
            case .completed: status == .completed
            }
        }
    }

    private struct PendingRequest: Identifiable {
        let session: AgentSession
        let interaction: Interaction

        var id: Key { Key(session: session.id, interaction: interaction.id) }

        struct Key: Hashable {
            let session: AgentSession.ID
            let interaction: Interaction.ID
        }
    }

    @Bindable var store: SessionStore
    @Environment(\.colorScheme) private var scheme
    @Environment(\.colorSchemeContrast) private var contrast
    @State private var selectedTab: Tab = .attention
    @State private var activeSessionID: AgentSession.ID?
    @State private var expandedRequest: PendingRequest.ID?
    @State private var changedExpansion = false
    @State private var filter: Filter = .all
    @State private var showsSettings = false
    @State private var confirmsRemoval = false

    private var palette: ThemePalette { WorkspaceStyle.palette(for: scheme, highContrast: contrast == .increased) }
    private var requests: [PendingRequest] {
        store.sessions.flatMap { session in
            session.interactions.map { PendingRequest(session: session, interaction: $0) }
        }
    }

    private var activeSession: AgentSession? {
        store.sessions.first { $0.id == activeSessionID } ?? store.sessions.first
    }

    var body: some View {
        TabView(selection: $selectedTab) {
            workspaceStack { inbox }
                .tabItem { Label("待处理", systemImage: "tray") }
                .badge(store.sessions.filter { $0.status.needsAttention }.count)
                .tag(Tab.attention)

            workspaceStack { workbench }
                .tabItem { Label("工作台", systemImage: "square.stack") }
                .tag(Tab.workbench)

            NavigationStack {
                if let session = activeSession {
                    SessionDetailView(store: store, sessionID: session.id, palette: palette)
                        .id(session.id)
                } else {
                    ContentUnavailableView("暂无会话", systemImage: "text.bubble")
                        .workspaceCanvas(palette)
                        .navigationTitle("会话")
                        .navigationBarTitleDisplayMode(.inline)
                }
            }
            .tabItem { Label("会话", systemImage: "text.bubble") }
            .tag(Tab.conversation)
        }
        .tint(palette.accent)
        .onChange(of: store.generation) {
            activeSessionID = nil
            expandedRequest = nil
            changedExpansion = false
            filter = .all
        }
        .sheet(isPresented: $showsSettings) {
            NavigationStack {
                settings
                    .navigationTitle("设置")
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar {
                        ToolbarItem(placement: .cancellationAction) {
                            Button("完成") { showsSettings = false }
                        }
                    }
            }
        }
        .alert("操作提示", isPresented: Binding(
            get: { store.error != nil },
            set: { if !$0 { store.error = nil } }
        )) {
            Button("好", role: .cancel) { store.error = nil }
        } message: {
            Text(store.error ?? "")
        }
    }

    private func workspaceStack<Content: View>(@ViewBuilder content: () -> Content) -> some View {
        NavigationStack {
            ScrollView {
                content()
                    .padding(.horizontal, 16)
                    .padding(.top, 12)
                    .padding(.bottom, 24)
                    .frame(maxWidth: 720)
                    .frame(maxWidth: .infinity, alignment: .top)
            }
            .foregroundStyle(palette.ink)
            .background(palette.canvas)
            .refreshable { await store.refresh(showErrors: true) }
            .navigationTitle("AgentDone")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        showsSettings = true
                    } label: {
                        Label("设置", systemImage: "gearshape")
                    }
                }
            }
        }
    }

    private var inbox: some View {
        let requests = requests
        let waiting = store.sessions.filter { $0.status.needsAttention && $0.interactions.isEmpty }
        let completed = Array(store.sessions.filter { $0.status == .completed }.prefix(3))

        return VStack(alignment: .leading, spacing: 16) {
            overview("待处理")
            if !store.loaded {
                ProgressView("正在读取会话")
            } else if requests.isEmpty && waiting.isEmpty {
                emptyState("没有待处理会话", icon: "checkmark.circle")
            } else {
                ForEach(requests) { request in
                    inboxRequest(request, first: requests.first?.id)
                }
                ForEach(waiting) { session in sessionLink(session) }
            }
            if !completed.isEmpty {
                WorkspaceSectionTitle(title: "最近完成", palette: palette)
                    .padding(.top, 8)
                ForEach(completed) { session in sessionLink(session) }
            }
        }
    }

    private func inboxRequest(_ request: PendingRequest, first: PendingRequest.ID?) -> some View {
        let session = request.session
        let interaction = request.interaction
        let isExpanded = changedExpansion ? expandedRequest == request.id : first == request.id
        let workstation = store.workstations.first { $0.id == session.workstationID }
        return VStack(alignment: .leading, spacing: 0) {
            Button {
                changedExpansion = true
                expandedRequest = isExpanded ? nil : request.id
            } label: {
                VStack(alignment: .leading, spacing: 8) {
                    HStack(alignment: .firstTextBaseline) {
                        switch interaction.prompt {
                        case .approval: Label("等待审批", systemImage: "lock.shield")
                        case .question: Label("等待回答", systemImage: "questionmark.bubble")
                        }
                        Spacer(minLength: 8)
                        Image(systemName: isExpanded ? "chevron.up" : "chevron.down")
                            .accessibilityHidden(true)
                    }
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(palette.accent)
                    Text(session.title).font(.headline).foregroundStyle(palette.ink)
                    if let workstation {
                        let project = session.directory.map { URL(fileURLWithPath: $0).lastPathComponent }
                        Text(project.map { "\(workstation.name) / \($0)" } ?? workstation.name)
                            .font(.subheadline).foregroundStyle(palette.muted)
                    }
                }
                .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                .padding(14)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityValue(isExpanded ? "已展开" : "已折叠")
            if isExpanded {
                InteractionCard(store: store, session: session, interaction: interaction, palette: palette) { reply in
                    Task {
                        if await store.submit(reply, session: session.id, interaction: interaction) {
                            changedExpansion = false
                        }
                    }
                }
                Button("打开会话") { openSession(session.id) }
                    .buttonStyle(.borderless)
                    .frame(minHeight: 44)
                    .padding(.horizontal, 14)
            }
        }
        .background(palette.surface, in: RoundedRectangle(cornerRadius: WorkspaceStyle.surfaceRadius))
        .overlay {
            RoundedRectangle(cornerRadius: WorkspaceStyle.surfaceRadius).strokeBorder(palette.rule)
                .allowsHitTesting(false)
                .accessibilityHidden(true)
        }
    }

    private var workbench: some View {
        LazyVStack(alignment: .leading, spacing: 12) {
            overview("工作台")
            Picker("会话状态", selection: $filter) {
                ForEach(Filter.allCases) { option in Text(option.rawValue).tag(option) }
            }
            .pickerStyle(.segmented)
            .accessibilityIdentifier("workbench-filter")
            if !store.loaded {
                ProgressView("正在读取会话")
            } else if store.workstations.isEmpty {
                emptyState("没有工作站会话", icon: "desktopcomputer")
            } else {
                ForEach(store.workstations) { workstation in
                    workstationGroup(workstation)
                }
            }
        }
    }

    private func workstationGroup(_ workstation: Workstation) -> some View {
        let sessions = store.sessions.filter { $0.workstationID == workstation.id && filter.includes($0.status) }
        return VStack(alignment: .leading, spacing: 0) {
            WorkstationLabel(workstation: workstation, palette: palette)
                .padding(.vertical, 10)
                .accessibilityAddTraits(.isHeader)
            Divider().overlay(palette.rule)
            if sessions.isEmpty {
                Text(filter == .all ? "暂无会话" : "没有符合筛选的会话")
                    .font(.subheadline)
                    .foregroundStyle(palette.muted)
                    .padding(.vertical, 12)
            }
            ForEach(sessions) { session in
                sessionLink(session, cached: !workstation.isOnline)
                Divider().overlay(palette.rule)
            }
        }
    }

    private func sessionLink(_ session: AgentSession, cached: Bool = false) -> some View {
        Button {
            openSession(session.id)
        } label: {
            HStack(spacing: 10) {
                Image(systemName: "doc.text").foregroundStyle(palette.muted).accessibilityHidden(true)
                SessionRow(session: session, palette: palette, cached: cached)
                Image(systemName: "chevron.right")
                    .font(.footnote)
                    .foregroundStyle(palette.muted)
                    .accessibilityHidden(true)
            }
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("open-session-" + session.id)
    }

    private func openSession(_ id: AgentSession.ID) {
        activeSessionID = id
        selectedTab = .conversation
    }

    private func overview(_ title: String) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            WorkspaceHeading(title: title, palette: palette)
            if let issue = store.connectionIssue {
                Label(issue, systemImage: "wifi.slash").font(.subheadline).foregroundStyle(palette.muted)
            } else if store.isDemo {
                Text("本地演示").font(.footnote).foregroundStyle(palette.muted)
            }
        }
    }

    private func emptyState(_ title: String, icon: String) -> some View {
        let connected = store.isDemo || store.workstations.contains(where: \.isOnline)
        return ContentUnavailableView(
            connected ? title : "暂时无法读取会话",
            systemImage: connected ? icon : "wifi.slash",
            description: connected ? nil : Text("下拉以重新连接。")
        )
    }

    private var settings: some View {
        List {
            Section {
                LabeledContent("运行模式", value: store.isDemo ? "演示" : "远程")
                NavigationLink {
                    PairingView(store: store, palette: palette)
                } label: {
                    Label(store.isDemo ? "配对工作站" : "管理配对", systemImage: "link")
                }
                if let pairing = store.pairing {
                    Text(pairing.relay).font(.footnote.monospaced()).foregroundStyle(palette.muted)
                }
            } header: {
                WorkspaceSectionTitle(title: "连接", palette: palette)
            }
            .listRowBackground(palette.surface)

            if !store.workstations.isEmpty {
                Section {
                    ForEach(store.workstations) { workstation in
                        WorkstationLabel(workstation: workstation, palette: palette)
                    }
                } header: {
                    WorkspaceSectionTitle(title: "工作站", palette: palette)
                }
                .listRowBackground(palette.surface)
            }
            Section {
                if store.isDemo {
                    Button("重置演示数据") { store.resetDemo() }
                } else {
                    Button("移除本机配对", role: .destructive) { confirmsRemoval = true }
                }
            }
            .listRowBackground(palette.surface)
            .disabled(store.submitting || store.refreshing)
        }
        .listStyle(.insetGrouped)
        .listSectionSpacing(WorkspaceStyle.sectionSpacing)
        .scrollContentBackground(.hidden)
        .foregroundStyle(palette.ink)
        .workspaceCanvas(palette)
        .confirmationDialog("移除本机配对？", isPresented: $confirmsRemoval, titleVisibility: .visible) {
            Button("移除本机配对", role: .destructive) {
                do { try store.useDemo() }
                catch { store.error = error.localizedDescription }
            }
            Button("取消", role: .cancel) {}
        } message: {
            Text("再次连接时需要重新填写配对配置。")
        }
    }
}

private struct WorkstationLabel: View {
    let workstation: Workstation
    let palette: ThemePalette

    var body: some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 12) {
                name.fixedSize(horizontal: true, vertical: false)
                Spacer(minLength: 8)
                status.fixedSize()
            }
            VStack(alignment: .leading, spacing: 5) {
                name
                status
            }
        }
        .font(.subheadline)
        .accessibilityElement(children: .combine)
    }

    private var name: some View {
        HStack(spacing: 6) {
            Image(systemName: "desktopcomputer").accessibilityHidden(true)
            Text(workstation.name)
        }
        .foregroundStyle(palette.ink)
    }

    private var status: some View {
        Text(workstation.isOnline ? "在线" : "离线")
            .foregroundStyle(workstation.isOnline ? palette.accent : palette.muted)
    }
}

private struct SessionRow: View {
    let session: AgentSession
    let palette: ThemePalette
    var cached = false

    var body: some View {
        VStack(alignment: .leading, spacing: WorkspaceStyle.entrySpacing) {
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 8) {
                    title.fixedSize(horizontal: true, vertical: false)
                    Spacer(minLength: 0)
                    status.fixedSize()
                }
                VStack(alignment: .leading, spacing: 4) {
                    title
                    status
                }
            }
            metadata
        }
        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        .padding(.vertical, WorkspaceStyle.entrySpacing)
        .accessibilityElement(children: .combine)
    }

    private var title: some View {
        Text(session.title)
            .font(.headline.weight(.semibold))
            .foregroundStyle(palette.ink)
            .fixedSize(horizontal: false, vertical: true)
    }

    @ViewBuilder private var metadata: some View {
        if let directory = session.directory {
            Text(directory)
                .font(.footnote.monospaced())
                .foregroundStyle(palette.muted)
        } else if let date = session.updatedAt {
            Text(date, format: .dateTime.month().day().hour().minute())
                .font(.footnote.monospacedDigit())
                .foregroundStyle(palette.muted)
        }
    }

    @ViewBuilder private var status: some View {
        if cached {
            Text("上次同步").font(.footnote).foregroundStyle(palette.muted)
        } else {
            StatusLabel(status: session.status, palette: palette)
        }
    }
}

struct StatusLabel: View {
    let status: SessionStatus
    let palette: ThemePalette

    private var icon: String {
        switch status {
        case .approval: "lock.shield"
        case .question: "questionmark.bubble"
        case .waiting: "hourglass"
        case .running: "bolt.circle"
        case .completed: "checkmark.circle"
        case .idle: "circle"
        case .closed: "archivebox"
        case .failed: "exclamationmark.circle"
        }
    }

    var body: some View {
        HStack(spacing: 5) {
            Image(systemName: icon).accessibilityHidden(true)
            Text(status.title)
        }
        .font(.footnote.weight(.semibold))
        .foregroundStyle(status.needsAttention ? palette.accent : palette.muted)
        .accessibilityElement(children: .combine)
    }
}
