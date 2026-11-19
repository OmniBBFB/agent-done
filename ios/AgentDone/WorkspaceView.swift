import SwiftUI

struct WorkspaceView: View {
    private enum Tab: Hashable {
        case attention, sessions, settings
    }

    @Bindable var store: SessionStore
    @AppStorage("agentDoneTheme") private var theme = WorkspaceTheme.terminal
    @Environment(\.colorScheme) private var scheme
    @Environment(\.colorSchemeContrast) private var contrast
    @State private var selectedTab: Tab = .attention
    @State private var attentionPath: [AgentSession.ID] = []
    @State private var sessionsPath: [AgentSession.ID] = []
    @State private var confirmsRemoval = false

    private var palette: ThemePalette { theme.palette(for: scheme, highContrast: contrast == .increased) }

    var body: some View {
        TabView(selection: $selectedTab) {
            sessionStack(path: $attentionPath, attentionOnly: true)
                .tabItem { Label("待处理", systemImage: "tray.fill") }
                .badge(store.sessions.filter { $0.status.needsAttention }.count)
                .tag(Tab.attention)

            sessionStack(path: $sessionsPath, attentionOnly: false)
                .tabItem { Label("会话", systemImage: "text.bubble.fill") }
                .tag(Tab.sessions)

            NavigationStack {
                settings
                    .navigationTitle("设置")
            }
            .tabItem { Label("设置", systemImage: "gearshape.fill") }
            .tag(Tab.settings)
        }
        .tint(palette.accent)
        .onChange(of: store.generation) {
            attentionPath.removeAll()
            sessionsPath.removeAll()
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

    private func sessionStack(path: Binding<[AgentSession.ID]>, attentionOnly: Bool) -> some View {
        NavigationStack(path: path) {
            VStack(spacing: 0) {
                sessionList(attentionOnly: attentionOnly)
            }
            .navigationTitle(attentionOnly ? "待处理" : "会话")
            .navigationDestination(for: AgentSession.ID.self) { id in
                SessionDetailView(store: store, sessionID: id, theme: theme, palette: palette)
            }
        }
    }

    private func sessionList(attentionOnly: Bool) -> some View {
        let sessions = store.sessions.filter { !attentionOnly || $0.status.needsAttention }

        return List {
            Section {
                overview(count: sessions.count, attentionOnly: attentionOnly)
                    .listRowBackground(palette.canvas)
                    .listRowSeparator(.hidden)
            }
            if !store.loaded {
                ProgressView("正在读取会话")
                    .listRowBackground(palette.surface)
            } else if sessions.isEmpty {
                emptyState(attentionOnly: attentionOnly)
                    .listRowBackground(palette.canvas)
            } else {
                ForEach(store.workstations) { workstation in
                    let items = sessions.filter { $0.workstationID == workstation.id }
                    if !items.isEmpty {
                        Section {
                            ForEach(items) { session in
                                NavigationLink(value: session.id) {
                                    SessionRow(session: session, theme: theme, palette: palette)
                                }
                                .listRowBackground(theme == .workbench ? palette.surface : palette.canvas)
                                .listRowSeparatorTint(palette.rule)
                            }
                        } header: {
                            WorkstationLabel(workstation: workstation, palette: palette)
                        }
                    }
                }
            }
        }
        .listStyle(.insetGrouped)
        .scrollContentBackground(.hidden)
        .background(palette.canvas)
        .refreshable { await store.refresh(showErrors: true) }
    }

    private func overview(count: Int, attentionOnly: Bool) -> some View {
        let state = connection
        return VStack(alignment: .leading, spacing: 10) {
            if store.loaded, count > 0 {
                Label {
                    Text(attentionOnly ? "\(count) 个会话待处理" : "\(count) 个会话")
                        .font(.system(.title2, design: theme.headingDesign).weight(.bold))
                } icon: {
                    Image(systemName: theme.icon).foregroundStyle(palette.accent)
                }
                .accessibilityAddTraits(.isHeader)
            }
            Label(state.text, systemImage: state.icon)
                .font(.subheadline)
                .foregroundStyle(palette.muted)
                .fixedSize(horizontal: false, vertical: true)
        }
        .foregroundStyle(palette.ink)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.vertical, 14)
        .padding(.horizontal, 16)
        .background(palette.surface, in: RoundedRectangle(cornerRadius: theme == .workbench ? 18 : 10))
        .overlay(alignment: .leading) {
            if theme != .workbench {
                UnevenRoundedRectangle(topLeadingRadius: 10, bottomLeadingRadius: 10)
                    .fill(palette.accent)
                    .frame(width: 3)
                    .accessibilityHidden(true)
            }
        }
    }

    private var connection: (text: String, icon: String) {
        if store.isDemo { return ("演示模式", "iphone") }
        if let issue = store.connectionIssue { return (issue, "wifi.slash") }
        if !store.loaded { return ("正在连接工作站", "link") }
        if !store.workstations.contains(where: \.isOnline) { return ("工作站离线", "wifi.slash") }
        return ("已连接工作站", "link")
    }

    private func emptyState(attentionOnly: Bool) -> some View {
        let online = store.isDemo || store.workstations.contains(where: \.isOnline)
        return ContentUnavailableView(
            online ? (attentionOnly ? "没有待处理会话" : "没有会话") : "暂时无法读取会话",
            systemImage: online ? (attentionOnly ? "checkmark.circle" : "text.bubble") : "wifi.slash",
            description: online ? nil : Text("下拉以重新连接。")
        )
        .foregroundStyle(palette.ink)
    }

    private var settings: some View {
        List {
            Section("主题") {
                ForEach(WorkspaceTheme.allCases) { option in
                    Button {
                        theme = option
                    } label: {
                        HStack(spacing: 12) {
                            let swatch = option.palette(for: scheme, highContrast: contrast == .increased)
                            Image(systemName: option.icon)
                                .foregroundStyle(swatch.accent)
                                .frame(width: 36, height: 36)
                                .background(swatch.canvas, in: RoundedRectangle(cornerRadius: 8))
                                .accessibilityHidden(true)
                            VStack(alignment: .leading, spacing: 4) {
                                Text(option.title).font(.headline).foregroundStyle(palette.ink)
                                Text(option.summary).font(.subheadline).foregroundStyle(palette.muted)
                            }
                            Spacer(minLength: 6)
                            if option == theme {
                                Image(systemName: "checkmark").accessibilityHidden(true)
                            }
                        }
                        .frame(minHeight: 44)
                        .contentShape(Rectangle())
                    }
                    .accessibilityAddTraits(option == theme ? .isSelected : [])
                    .listRowBackground(palette.surface)
                }
            }

            Section("连接") {
                LabeledContent("运行模式", value: store.isDemo ? "演示" : "远程")
                NavigationLink {
                    PairingView(store: store)
                } label: {
                    Label(store.isDemo ? "配对工作站" : "管理配对", systemImage: "link")
                }
                if let pairing = store.pairing {
                    Text(pairing.relay).font(.footnote.monospaced()).foregroundStyle(palette.muted)
                }
            }
            .listRowBackground(palette.surface)

            if !store.workstations.isEmpty {
                Section("工作站") {
                    ForEach(store.workstations) { workstation in
                        WorkstationLabel(workstation: workstation, palette: palette)
                    }
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
        .scrollContentBackground(.hidden)
        .background(palette.canvas)
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
        Label(workstation.name, systemImage: "desktopcomputer")
            .foregroundStyle(palette.ink)
    }

    private var status: some View {
        Text(workstation.isOnline ? "在线" : "离线")
            .foregroundStyle(workstation.isOnline ? palette.accent : palette.muted)
    }
}

private struct SessionRow: View {
    let session: AgentSession
    let theme: WorkspaceTheme
    let palette: ThemePalette

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            if theme == .terminal { status }
            Text(session.title)
                .font(.system(.headline, design: theme.headingDesign))
                .foregroundStyle(palette.ink)
            if theme != .terminal { status }
            if let directory = session.directory {
                Text(directory).font(.footnote.monospaced()).foregroundStyle(palette.muted)
            } else if let date = session.updatedAt {
                Text(date, format: .dateTime.month().day().hour().minute())
                    .font(.footnote.monospacedDigit()).foregroundStyle(palette.muted)
            }
        }
        .frame(minHeight: 44, alignment: .leading)
        .padding(.vertical, theme == .workbench ? 7 : 4)
        .accessibilityElement(children: .combine)
    }

    private var status: some View { StatusLabel(status: session.status, palette: palette) }
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
        Label(status.title, systemImage: icon)
            .font(.footnote.weight(.semibold))
            .foregroundStyle(status.needsAttention ? palette.accent : palette.muted)
    }
}
