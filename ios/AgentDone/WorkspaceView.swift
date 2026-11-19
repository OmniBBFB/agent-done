import SwiftUI

struct WorkspaceView: View {
    @Bindable var store: SessionStore

    var body: some View {
        TabView {
            NavigationStack {
                sessionList(attentionOnly: true)
                    .navigationTitle("待处理")
            }
            .id(store.generation)
            .tabItem { Label("待处理", systemImage: "tray") }

            NavigationStack {
                sessionList(attentionOnly: false)
                    .navigationTitle("会话")
            }
            .id(store.generation)
            .tabItem { Label("会话", systemImage: "text.bubble") }

            NavigationStack {
                settings
                    .navigationTitle("设置")
            }
            .tabItem { Label("设置", systemImage: "gearshape") }
        }
        .task { await store.refresh() }
        .alert("操作提示", isPresented: Binding(
            get: { store.error != nil },
            set: { if !$0 { store.error = nil } }
        )) {
            Button("好", role: .cancel) { store.error = nil }
        } message: {
            Text(store.error ?? "")
        }
    }

    private func sessionList(attentionOnly: Bool) -> some View {
        let sessions = store.sessions.filter { !attentionOnly || $0.status.needsAttention }

        return List {
            if store.isDemo {
                Section {
                    Label("演示模式", systemImage: "iphone")
                        .font(.subheadline.weight(.medium))
                        .foregroundStyle(.indigo)
                }
            } else if let issue = store.connectionIssue {
                Label(issue, systemImage: "wifi.slash").foregroundStyle(.secondary)
            } else if store.workstations.contains(where: { !$0.isOnline }) {
                Label("工作站离线", systemImage: "wifi.slash").foregroundStyle(.secondary)
            }
            if !store.loaded {
                ProgressView("正在读取会话")
            } else if sessions.isEmpty {
                ContentUnavailableView(attentionOnly ? "没有待处理会话" : "没有会话", systemImage: "tray")
            } else {
                ForEach(store.workstations) { workstation in
                    let items = sessions.filter { $0.workstationID == workstation.id }
                    if !items.isEmpty {
                        Section {
                            ForEach(items) { session in
                                NavigationLink(value: session.id) {
                                    SessionRow(session: session)
                                }
                            }
                        } header: {
                            WorkstationLabel(workstation: workstation)
                        }
                    }
                }
            }
        }
        .navigationDestination(for: AgentSession.ID.self) { id in
            SessionDetailView(store: store, sessionID: id)
        }
        .refreshable { await store.refresh(showErrors: true) }
    }

    private var settings: some View {
        List {
            Section("连接") {
                LabeledContent("运行模式", value: store.isDemo ? "演示" : "远程")
                NavigationLink {
                    PairingView(store: store)
                } label: {
                    Label(store.isDemo ? "配对工作站" : "管理配对", systemImage: "link")
                }
                if let pairing = store.pairing {
                    Text(pairing.relay).font(.footnote.monospaced()).foregroundStyle(.secondary)
                }
            }
            Section("工作站") {
                ForEach(store.workstations) { workstation in
                    WorkstationLabel(workstation: workstation)
                }
            }
            Section {
                if store.isDemo {
                    Button("重置演示数据") { store.resetDemo() }
                } else {
                    Button("移除本机配对", role: .destructive) {
                        do { try store.useDemo() }
                        catch { store.error = error.localizedDescription }
                    }
                }
            }
            .disabled(store.submitting || store.refreshing)
        }
    }
}

private struct WorkstationLabel: View {
    let workstation: Workstation

    var body: some View {
        LabeledContent {
            Text(workstation.isOnline ? "在线" : "离线")
                .foregroundStyle(workstation.isOnline ? Color.green : Color.secondary)
        } label: {
            Label(workstation.name, systemImage: "desktopcomputer")
        }
    }
}

private struct SessionRow: View {
    let session: AgentSession

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            StatusLabel(status: session.status)
            Text(session.title)
                .font(.headline)
            if let directory = session.directory {
                Text(directory).font(.caption.monospaced()).foregroundStyle(.secondary)
            } else if let date = session.updatedAt {
                Text(date, format: .dateTime.month().day().hour().minute())
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 6)
        .accessibilityElement(children: .combine)
    }
}

struct StatusLabel: View {
    let status: SessionStatus

    private var appearance: (icon: String, color: Color) {
        switch status {
        case .approval: ("lock.shield", .orange)
        case .question: ("questionmark.bubble", .indigo)
        case .waiting: ("hourglass", .orange)
        case .running: ("bolt.circle", .blue)
        case .completed: ("checkmark.circle", .green)
        case .idle: ("circle", .secondary)
        case .closed: ("archivebox", .secondary)
        case .failed: ("exclamationmark.circle", .red)
        }
    }

    var body: some View {
        Label(status.title, systemImage: appearance.icon)
            .font(.caption.weight(.semibold))
            .foregroundStyle(appearance.color)
    }
}
