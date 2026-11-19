import SwiftUI

@main
struct AgentDoneApp: App {
    @Environment(\.scenePhase) private var scenePhase
    @State private var store = SessionStore()

    var body: some Scene {
        WindowGroup {
            WorkspaceView(store: store)
                .task(id: MonitorID(active: scenePhase == .active, generation: store.generation)) {
                    if scenePhase == .active {
                        await store.monitor()
                    } else {
                        await store.remote?.connection.disconnect()
                    }
                }
        }
    }
}

private struct MonitorID: Equatable {
    let active: Bool
    let generation: UUID
}

@MainActor
@Observable
final class SessionStore {
    private(set) var workstations: [Workstation] = []
    private(set) var sessions: [AgentSession] = []
    private(set) var outcomes: [String: InteractionState] = [:]
    private(set) var loaded = false
    private(set) var refreshing = false
    private(set) var submitting = false
    private(set) var generation = UUID()
    private(set) var pairing: Pairing?
    private(set) var remote: RemoteSessionClient?
    private(set) var client: any SessionClient = DemoSessionClient()
    private(set) var connectionIssue: String?
    var error: String?

    var isDemo: Bool { remote == nil }

    init() {
        do {
            if let saved = try PairingVault.load(Pairing.self, name: PairingVault.pairing) {
                let live = try RemoteSessionClient(pairing: saved, key: PairingVault.deviceKey())
                pairing = saved
                remote = live
                client = live
            }
        } catch {
            self.error = error.localizedDescription
        }
    }

    func monitor() async {
        let current = generation
        guard let connection = remote?.connection else {
            await refresh()
            return
        }
        let events = await connection.events()
        await withTaskGroup(of: Void.self) { group in
            group.addTask {
                for await _ in events {
                    guard !Task.isCancelled else { break }
                    await self.refreshForMonitor(current)
                }
            }
            group.addTask {
                while !Task.isCancelled {
                    do { try await Task.sleep(for: .seconds(12)) }
                    catch { break }
                    await self.refreshForMonitor(current, poll: true)
                }
            }
            await refreshForMonitor(current)
            await group.waitForAll()
        }
    }

    func refresh(showErrors: Bool = false) async {
        guard !refreshing, !submitting else {
            needsRefresh = true
            return
        }
        let current = generation
        refreshing = true
        defer { if generation == current { refreshing = false } }
        repeat {
            needsRefresh = false
            do {
                let snapshot = try await client.snapshot()
                guard generation == current, !Task.isCancelled else { return }
                apply(snapshot)
            } catch {
                guard generation == current, !Task.isCancelled else { return }
                loaded = true
                connectionIssue = error.localizedDescription
                workstations = workstations.map { Workstation(id: $0.id, name: $0.name, isOnline: false) }
                if showErrors { self.error = error.localizedDescription }
                return
            }
        } while needsRefresh && !Task.isCancelled
    }

    func submit(_ reply: InteractionReply, session: AgentSession.ID, interaction: Interaction) async -> Bool {
        guard !submitting, !refreshing else { return false }
        let current = generation
        submitting = true
        defer { if generation == current { submitting = false } }
        let result: InteractionState?
        do {
            result = try await client.submit(reply, session: session, interaction: interaction)
        } catch {
            guard generation == current else { return false }
            self.error = error.localizedDescription
            result = nil
        }
        guard generation == current else { return false }
        if let result {
            outcomes[interaction.id] = result
        }
        submitting = false
        await refresh()
        return result == .applied || result == .closed
    }

    func checkStatus(session: AgentSession.ID, interaction: Interaction) async {
        guard !refreshing, !submitting, let remote else { return }
        let current = generation
        submitting = true
        defer { if generation == current { submitting = false } }
        do {
            let status = try await remote.checkStatus(session: session, interaction: interaction.id)
            guard generation == current else { return }
            outcomes[interaction.id] = status
            error = status.description
        } catch {
            guard generation == current else { return }
            self.error = error.localizedDescription
        }
        submitting = false
        await refresh()
    }

    func sendMessage(_ text: String, to session: AgentSession.ID) async throws -> MessageDelivery {
        guard !refreshing, !submitting,
              let target = sessions.first(where: { $0.id == session }), target.acceptsMessage,
              workstations.contains(where: { $0.id == target.workstationID && $0.isOnline })
        else { throw SessionClientError.unavailable }

        let current = generation
        submitting = true
        defer { if generation == current { submitting = false } }
        let result = try await client.sendMessage(text, to: session)
        guard generation == current else { throw CancellationError() }
        submitting = false
        await refresh()
        return result
    }

    func savePairing(_ value: Pairing) throws {
        let live = try RemoteSessionClient(pairing: value, key: PairingVault.deviceKey())
        try PairingVault.save(value, name: PairingVault.pairing)
        pairing = value
        replace(with: live)
    }

    func useDemo() throws {
        try PairingVault.remove(PairingVault.pairing)
        pairing = nil
        replace(with: nil)
    }

    func resetDemo() { replace(with: nil) }

    private var needsRefresh = false

    private func refreshForMonitor(_ current: UUID, poll: Bool = false) async {
        guard generation == current else { return }
        if poll && loaded && connectionIssue == nil && workstations.contains(where: \.isOnline) { return }
        await refresh()
    }

    private func replace(with live: RemoteSessionClient?) {
        if let connection = remote?.connection { Task { await connection.disconnect() } }
        remote = live
        client = live.map { $0 as any SessionClient } ?? DemoSessionClient()
        workstations = []
        sessions = []
        outcomes = [:]
        loaded = false
        refreshing = false
        submitting = false
        needsRefresh = false
        connectionIssue = nil
        generation = UUID()
    }

    private func apply(_ snapshot: SessionSnapshot) {
        workstations = snapshot.workstations
        sessions = snapshot.sessions
        loaded = true
        connectionIssue = nil
    }
}
