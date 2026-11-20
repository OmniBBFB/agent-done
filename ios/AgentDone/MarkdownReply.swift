import SwiftUI
import UIKit

/// Foundation handles Markdown syntax. Keep block boundaries so SwiftUI can lay
/// out lists, quotes, tables and code independently of inline formatting.
struct ReplyMarkdown {
    struct Block {
        let path: [PresentationIntent.IntentType]
        var text = AttributedString()
    }

    enum Section {
        case block(Block)
        case table(rows: [[AttributedString]], columns: [PresentationIntent.TableColumn])
    }

    let sections: [Section]

    init(_ source: String) {
        let options = AttributedString.MarkdownParsingOptions(
            interpretedSyntax: .full, failurePolicy: .returnPartiallyParsedIfPossible
        )
        guard let content = try? AttributedString(markdown: source, options: options),
              !content.characters.isEmpty else {
            sections = [.block(Block(path: [], text: AttributedString(source)))]
            return
        }

        var blocks: [Block] = []
        for run in content.runs {
            let path = run.presentationIntent?.components ?? []
            var fragment = AttributedString(content[run.range])
            if let link = run.link, !Self.isWebLink(link) { fragment.link = nil }
            if run.inlinePresentationIntent?.contains(.code) == true { fragment.font = .body.monospaced() }
            if let last = blocks.indices.last, blocks[last].path.first?.identity == path.first?.identity {
                blocks[last].text.append(fragment)
            } else {
                blocks.append(Block(path: path, text: fragment))
            }
        }

        var result: [Section] = []
        var index = 0
        while index < blocks.count {
            let block = blocks[index]
            guard let table = block.path.last, case .table(let columns) = table.kind else {
                result.append(.block(block))
                index += 1
                continue
            }

            var rows: [[AttributedString]] = []
            var rowID: Int?
            while index < blocks.count, blocks[index].path.last?.identity == table.identity {
                let cell = blocks[index]
                if cell.path.count >= 3, case .tableCell(let column) = cell.path[0].kind,
                   columns.indices.contains(column) {
                    let parentID = cell.path[1].identity
                    if rowID != parentID {
                        rows.append(Array(repeating: AttributedString(), count: columns.count))
                        rowID = parentID
                    }
                    rows[rows.count - 1][column] = cell.text
                }
                index += 1
            }
            if !rows.isEmpty { result.append(.table(rows: rows, columns: columns)) }
        }
        sections = result
    }

    private static func isWebLink(_ url: URL) -> Bool {
        guard let scheme = url.scheme?.lowercased() else { return false }
        return (scheme == "https" || scheme == "http") && url.host?.isEmpty == false
    }
}

struct MarkdownReply: View {
    let source: String
    let palette: ThemePalette

    var body: some View {
        let sections = ReplyMarkdown(source).sections
        VStack(alignment: .leading, spacing: 10) {
            ForEach(sections.indices, id: \.self) { index in
                switch sections[index] {
                case .block(let block):
                    decorated(block, previous: index == 0 ? nil : sections[index - 1])
                case .table(let rows, let columns):
                    table(rows, columns: columns)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .tint(palette.accent)
        .textSelection(.enabled)
    }

    private func decorated(_ block: ReplyMarkdown.Block, previous: ReplyMarkdown.Section?) -> some View {
        let items = block.path.enumerated().filter { if case .listItem = $0.element.kind { return true }; return false }
        let nearestItem = items.first
        let quote = block.path.contains { if case .blockQuote = $0.kind { return true }; return false }
        let newItem: Bool
        if let nearestItem, case .block(let earlier) = previous {
            newItem = !earlier.path.contains { $0.identity == nearestItem.element.identity }
        } else {
            newItem = true
        }

        return HStack(alignment: .top, spacing: 8) {
            if newItem, let nearestItem,
               case .listItem(let ordinal) = nearestItem.element.kind {
                let parents = block.path.dropFirst(nearestItem.offset + 1)
                let ordered = parents.first.map { if case .orderedList = $0.kind { return true }; return false } ?? false
                Text(ordered ? "\(ordinal)." : "•")
                    .foregroundStyle(palette.muted)
            }
            blockContent(block)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(quote ? 10 : 0)
                .background(quote ? palette.canvas : .clear, in: RoundedRectangle(cornerRadius: 6))
                .overlay(alignment: .leading) {
                    if quote {
                        Rectangle().fill(palette.accent).frame(width: 2).accessibilityHidden(true)
                    }
                }
        }
        .padding(.leading, CGFloat(min(3, max(0, items.count - (newItem ? 1 : 0)))) * 14)
    }

    @ViewBuilder private func blockContent(_ block: ReplyMarkdown.Block) -> some View {
        switch block.path.first?.kind {
        case .header(let level):
            Text(block.text)
                .font((level <= 2 ? Font.title3 : Font.headline).weight(.semibold))
                .accessibilityAddTraits(.isHeader)
        case .codeBlock(let language):
            ReplyCodeBlock(text: String(block.text.characters), language: language, palette: palette)
        case .thematicBreak:
            Rectangle().fill(palette.rule).frame(height: 1).accessibilityHidden(true)
        default:
            Text(block.text).lineSpacing(3)
        }
    }

    private func table(_ rows: [[AttributedString]], columns: [PresentationIntent.TableColumn]) -> some View {
        ScrollView(.horizontal) {
            Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 0) {
                ForEach(rows.indices, id: \.self) { row in
                    GridRow {
                        ForEach(columns.indices, id: \.self) { column in
                            Text(rows[row][column])
                                .font(row == 0 ? .subheadline.weight(.semibold) : .subheadline)
                                .fixedSize()
                                .padding(.vertical, 8)
                                .gridColumnAlignment(alignment(columns[column]))
                        }
                    }
                    if row < rows.count - 1 { Divider().gridCellUnsizedAxes(.horizontal) }
                }
            }
            .padding(.horizontal, 12)
        }
        .background(palette.canvas, in: RoundedRectangle(cornerRadius: 8))
        .accessibilityLabel("表格")
    }

    private func alignment(_ column: PresentationIntent.TableColumn) -> HorizontalAlignment {
        switch column.alignment {
        case .left: .leading
        case .center: .center
        case .right: .trailing
        @unknown default: .leading
        }
    }
}

private struct ReplyCodeBlock: View {
    let text: String
    let language: String?
    let palette: ThemePalette
    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Text(language.flatMap { $0.isEmpty ? nil : $0 } ?? "代码")
                    .font(.footnote)
                    .foregroundStyle(palette.muted)
                Spacer()
                Button {
                    UIPasteboard.general.string = text
                    copied = true
                    AccessibilityNotification.Announcement("代码已复制").post()
                } label: {
                    Label(copied ? "已复制" : "复制", systemImage: copied ? "checkmark" : "document.on.document")
                        .font(.footnote.weight(.semibold))
                        .frame(minWidth: 44, minHeight: 44)
                }
                .buttonStyle(.borderless)
                .accessibilityLabel(copied ? "代码已复制" : "复制代码")
            }
            .padding(.horizontal, 12)
            Divider()
            ScrollView(.horizontal) {
                Text(text.last == "\n" ? String(text.dropLast()) : text)
                    .font(.callout.monospaced())
                    .fixedSize()
                    .padding(12)
            }
        }
        .background(palette.canvas, in: RoundedRectangle(cornerRadius: 8))
        .task(id: copied) {
            guard copied else { return }
            do { try await Task.sleep(for: .seconds(2)); copied = false }
            catch { }
        }
    }
}
