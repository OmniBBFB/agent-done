import SwiftUI

struct PairingView: View {
    let store: SessionStore
    let palette: ThemePalette
    @Environment(\.dismiss) private var dismiss
    @State private var draft: Pairing
    @State private var publicKey = ""
    @State private var validationError: String?

    init(store: SessionStore, palette: ThemePalette) {
        self.store = store
        self.palette = palette
        _draft = State(initialValue: store.pairing ?? Pairing())
    }

    var body: some View {
        Form {
            Section {
                if !publicKey.isEmpty {
                    Text(publicKey)
                        .font(.footnote.monospaced())
                        .textSelection(.enabled)
                        .padding(.vertical, 6)
                    ShareLink(item: publicKey) { Label("分享公钥", systemImage: "square.and.arrow.up") }
                }
            } header: {
                WorkspaceSectionTitle(title: "手机公钥", palette: palette)
            } footer: {
                Text("通过受信渠道交换公钥，在工作站添加这台手机的公钥。")
                    .foregroundStyle(palette.muted)
            }
            .listRowBackground(palette.surface)
            Section {
                input("WSS 地址", text: $draft.relay)
                    .keyboardType(.URL)
                input("手机设备 ID", text: $draft.phoneID)
                field("手机中转令牌") {
                    SecureField("手机中转令牌", text: $draft.token, prompt: Text(" ").foregroundStyle(palette.muted))
                }
            } header: {
                WorkspaceSectionTitle(title: "中转", palette: palette)
            }
            .listRowBackground(palette.surface)
            Section {
                input("工作站设备 ID", text: $draft.workstationID)
                input("工作站公钥（Base64）", text: $draft.workstationKey)
            } header: {
                WorkspaceSectionTitle(title: "工作站", palette: palette)
            }
            .listRowBackground(palette.surface)
            Section {
                if let validationError {
                    Label(validationError, systemImage: "exclamationmark.circle")
                        .foregroundStyle(palette.danger)
                }
                Button {
                    save()
                } label: {
                    Text("保存并连接")
                        .fontWeight(.semibold)
                        .frame(minHeight: 44)
                }
            }
            .listRowBackground(palette.surface)
        }
        .listSectionSpacing(WorkspaceStyle.sectionSpacing)
        .scrollContentBackground(.hidden)
        .foregroundStyle(palette.ink)
        .workspaceCanvas(palette)
        .navigationTitle("配对工作站")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        .task {
            do { publicKey = try PairingVault.deviceKey().publicKey.rawRepresentation.base64EncodedString() }
            catch { validationError = error.localizedDescription }
        }
    }

    private func input(_ title: String, text: Binding<String>) -> some View {
        field(title) {
            TextField(title, text: text, prompt: Text(" "))
        }
    }

    private func field<Content: View>(_ title: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title)
                .font(.subheadline.weight(.medium))
                .foregroundStyle(palette.muted)
                .accessibilityHidden(true)
            content()
                .font(.body.monospaced())
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .frame(minHeight: 44, alignment: .leading)
                .accessibilityLabel(Text(title))
        }
        .padding(.vertical, 4)
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
