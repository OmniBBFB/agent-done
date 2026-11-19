import CryptoKit
import Foundation

private final class RelaySessionDelegate: NSObject, URLSessionTaskDelegate {
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest,
                    completionHandler: @escaping @Sendable (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

actor RelayConnection {
    private struct Pending {
        let continuation: CheckedContinuation<Data, any Error>
        let timeout: Task<Void, Never>
        let outerID: String?
    }

    private var observers: [UUID: AsyncStream<Void>.Continuation] = [:]
    private let url: URL
    private let pairing: Pairing
    private let local: Curve25519.KeyAgreement.PrivateKey
    private let peer: Curve25519.KeyAgreement.PublicKey
    private let session: URLSession
    private var socket: URLSessionWebSocketTask?
    private var receiveTask: Task<Void, Never>?
    private var ready = false
    private var waitingReady = Set<String>()
    private var pending: [String: Pending] = [:]
    private var sends: [String: String] = [:]

    init(pairing: Pairing, local: Curve25519.KeyAgreement.PrivateKey, session: URLSession? = nil) throws {
        (url, peer) = try pairing.validated()
        self.pairing = pairing
        self.local = local
        self.session = session ?? URLSession(configuration: .ephemeral, delegate: RelaySessionDelegate(), delegateQueue: nil)
    }

    deinit { session.invalidateAndCancel() }

    func disconnect() {
        if let socket { close(socket) }
    }

    func events() -> AsyncStream<Void> {
        let id = UUID()
        let (stream, continuation) = AsyncStream<Void>.makeStream(bufferingPolicy: .bufferingNewest(1))
        observers[id] = continuation
        continuation.onTermination = { [weak self] _ in
            Task { await self?.removeObserver(id) }
        }
        return stream
    }

    private func removeObserver(_ id: UUID) { observers.removeValue(forKey: id) }

    private func notifyObservers() {
        for observer in observers.values { observer.yield() }
    }

    func presence() async throws -> Bool {
        try await connect()
        let id = UUID().uuidString
        let frame = RelayFrame(type: "presence", id: id, to: pairing.workstationID)
        let data = try await wait(id, frame: frame)
        let result = try JSONDecoder().decode(RelayFrame.self, from: data)
        guard result.status == "ok", let online = result.online else {
            throw WireError.relay(result.status ?? "invalid_request")
        }
        return online
    }

    func request<Params: Encodable & Sendable, Result: Decodable & Sendable>(_ method: String, params: Params,
                                                       as type: Result.Type) async throws -> Result {
        try await connect()
        let id = UUID().uuidString
        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        let plaintext = try encoder.encode(ControlRequest(id: id, method: method, params: params))
        let packet = try ControlCrypto.seal(plaintext, from: pairing.phoneID, to: pairing.workstationID,
                                            local: local, peer: peer)
        let outerID = UUID().uuidString
        let frame = RelayFrame(type: "send", id: outerID, to: pairing.workstationID, payload: packet)
        let data = try await wait(id, frame: frame, outerID: outerID)
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        return try decoder.decode(ControlResponse<Result>.self, from: data).value()
    }

    private func connect() async throws {
        try Task.checkCancellation()
        if ready { return }
        if socket == nil {
            var request = URLRequest(url: url)
            request.setValue("Bearer " + pairing.token, forHTTPHeaderField: "Authorization")
            request.setValue("agent-done.relay.v1", forHTTPHeaderField: "Sec-WebSocket-Protocol")
            let connection = session.webSocketTask(with: request)
            connection.maximumMessageSize = 360 * 1024
            socket = connection
            connection.resume()
            receiveTask = Task { [weak self] in
                do {
                    while !Task.isCancelled {
                        let message = try await connection.receive()
                        await self?.receive(message, on: connection)
                    }
                } catch {
                    await self?.close(connection)
                }
            }
        }
        let id = UUID().uuidString
        waitingReady.insert(id)
        _ = try await wait(id)
    }

    private func wait(_ id: String, frame: RelayFrame? = nil, outerID: String? = nil) async throws -> Data {
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { continuation in
                guard !Task.isCancelled else {
                    waitingReady.remove(id)
                    continuation.resume(throwing: CancellationError())
                    return
                }
                let timeout = Task {
                    do { try await Task.sleep(for: .seconds(12)) }
                    catch { return }
                    if self.waitingReady.contains(id), let socket = self.socket {
                        self.close(socket, error: WireError.timeout)
                    } else {
                        self.finish(id, with: .failure(WireError.timeout))
                    }
                }
                pending[id] = Pending(continuation: continuation, timeout: timeout, outerID: outerID)
                if let outerID { sends[outerID] = id }
                if let frame {
                    guard let socket, ready else {
                        finish(id, with: .failure(WireError.disconnected))
                        return
                    }
                    Task { await self.send(frame, id: id, on: socket) }
                }
            }
        } onCancel: {
            Task { await self.finish(id, with: .failure(CancellationError())) }
        }
    }

    private func send(_ frame: RelayFrame, id: String, on connection: URLSessionWebSocketTask) async {
        guard pending[id] != nil, socket === connection else { return }
        do {
            let text = String(decoding: try JSONEncoder().encode(frame), as: UTF8.self)
            try await connection.send(.string(text))
        } catch {
            close(connection)
        }
    }

    private func receive(_ message: URLSessionWebSocketTask.Message, on connection: URLSessionWebSocketTask) {
        guard socket === connection, case .string(let text) = message else { return }
        let data = Data(text.utf8)
        guard let frame = try? JSONDecoder().decode(RelayFrame.self, from: data) else { return }

        if !ready {
            guard frame.type == "ready", frame.device == pairing.phoneID else {
                close(connection, error: WireError.invalidConfiguration)
                return
            }
            ready = true
            for id in Array(waitingReady) { finish(id, with: .success(Data())) }
            return
        }

        switch frame.type {
        case "result":
            guard let id = frame.id, let status = frame.status else { return }
            if let businessID = sends[id] {
                if status != "forwarded" { finish(businessID, with: .failure(WireError.relay(status))) }
            } else if let request = pending[id], request.outerID == nil {
                finish(id, with: .success(data))
            }
        case "delivery":
            guard frame.from == pairing.workstationID, let packet = frame.payload,
                  let plaintext = try? ControlCrypto.open(packet, from: pairing.workstationID, to: pairing.phoneID,
                                                          local: local, peer: peer),
                  let header = try? JSONDecoder().decode(ControlHeader.self, from: plaintext), header.v == 1
            else { return }
            if header.type == "response", let id = header.id, pending[id]?.outerID != nil {
                finish(id, with: .success(plaintext))
            } else if header.type == "event", header.event == "state.changed" {
                notifyObservers()
            }
        default:
            break
        }
    }

    private func close(_ connection: URLSessionWebSocketTask, error: any Error = WireError.disconnected) {
        guard socket === connection else { return }
        let wasReady = ready
        ready = false
        socket = nil
        receiveTask?.cancel()
        receiveTask = nil
        connection.cancel(with: .goingAway, reason: nil)
        for id in Array(pending.keys) { finish(id, with: .failure(error)) }
        if wasReady { notifyObservers() }
    }

    private func finish(_ id: String, with result: Result<Data, any Error>) {
        waitingReady.remove(id)
        guard let request = pending.removeValue(forKey: id) else { return }
        if let outerID = request.outerID { sends.removeValue(forKey: outerID) }
        request.timeout.cancel()
        request.continuation.resume(with: result)
    }
}
