import SwiftUI

struct PairingView: View {
    let store: SessionStore
    @Environment(\.dismiss) private var dismiss
    @State private var draft: Pairing
    @State private var publicKey = ""
    @State private var validationError: String?

    init(store: SessionStore) {
        self.store = store
        _draft = State(initialValue: store.pairing ?? Pairing())
    }

    var body: some View {
        Form {
            Section {
                if !publicKey.isEmpty {
                    Text(publicKey).font(.footnote.monospaced()).textSelection(.enabled)
                    ShareLink(item: publicKey) { Label("分享公钥", systemImage: "square.and.arrow.up") }
                }
            } header: {
                Text("手机公钥")
            } footer: {
                Text("通过受信渠道交换公钥，在工作站添加这台手机的公钥。")
            }
            Section("中转") {
                input("WSS 地址", text: $draft.relay)
                    .keyboardType(.URL)
                input("手机设备 ID", text: $draft.phoneID)
                SecureField("手机中转令牌", text: $draft.token)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
            }
            Section("工作站") {
                input("工作站设备 ID", text: $draft.workstationID)
                input("工作站公钥（Base64）", text: $draft.workstationKey)
            }
            Section {
                if let validationError {
                    Text(validationError).foregroundStyle(.red)
                }
                Button("保存并连接") { save() }
            }
        }
        .navigationTitle("配对工作站")
        .task {
            do { publicKey = try PairingVault.deviceKey().publicKey.rawRepresentation.base64EncodedString() }
            catch { validationError = error.localizedDescription }
        }
    }

    private func input(_ title: String, text: Binding<String>) -> some View {
        TextField(title, text: text)
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
    }

    private func save() {
        let fields: [WritableKeyPath<Pairing, String>] = [\.relay, \.phoneID, \.token, \.workstationID, \.workstationKey]
        for field in fields { draft[keyPath: field] = draft[keyPath: field].trimmingCharacters(in: .whitespacesAndNewlines) }
        do {
            try store.savePairing(draft)
            dismiss()
        } catch {
            validationError = error.localizedDescription
        }
    }
}
