import SwiftUI

enum WorkspaceTheme: String, CaseIterable, Identifiable {
    case terminal, workbench, journal

    var id: String { rawValue }

    var title: String {
        switch self {
        case .terminal: "终端"
        case .workbench: "工作台"
        case .journal: "纪要"
        }
    }

    var summary: String {
        switch self {
        case .terminal: "提示符、等宽字和连续输出"
        case .workbench: "分组卡片，优先扫读状态"
        case .journal: "按角色排版，适合回看长会话"
        }
    }

    var icon: String {
        switch self {
        case .terminal: "terminal"
        case .workbench: "square.grid.2x2"
        case .journal: "text.alignleft"
        }
    }

    var headingDesign: Font.Design {
        switch self {
        case .terminal: .monospaced
        case .workbench: .default
        case .journal: .serif
        }
    }

    func palette(for scheme: ColorScheme, highContrast: Bool = false) -> ThemePalette {
        let dark = scheme == .dark
        return switch (self, dark) {
        case (.terminal, false): .init(0xEDF2F1, 0xF9FCFB, 0x182B29, 0x435A55, 0x00584C, highContrast)
        case (.terminal, true): .init(0x101B1A, 0x172724, 0xEBF7F3, 0xAAC6BC, 0x85DFC5, highContrast)
        case (.workbench, false): .init(0xEDF1F9, 0xFFFFFF, 0x17253F, 0x495975, 0x234B95, highContrast)
        case (.workbench, true): .init(0x10172A, 0x1B2740, 0xF0F4FF, 0xBBC9E0, 0xADC8FF, highContrast)
        case (.journal, false): .init(0xF7F1EA, 0xFFFAF4, 0x32241F, 0x635048, 0x803F28, highContrast)
        case (.journal, true): .init(0x211916, 0x2C221E, 0xF8EDE4, 0xD2BEB0, 0xF0B995, highContrast)
        }
    }
}

struct ThemePalette {
    let canvas: Color
    let surface: Color
    let ink: Color
    let muted: Color
    let accent: Color
    let rule: Color

    init(_ canvas: UInt32, _ surface: UInt32, _ ink: UInt32, _ muted: UInt32, _ accent: UInt32, _ highContrast: Bool) {
        self.canvas = Color(rgb: canvas)
        self.surface = Color(rgb: surface)
        self.ink = Color(rgb: ink)
        self.muted = Color(rgb: highContrast ? ink : muted)
        self.accent = Color(rgb: accent)
        rule = Color(rgb: ink).opacity(highContrast ? 0.65 : 0.25)
    }
}

private extension Color {
    init(rgb: UInt32) {
        self.init(red: Double((rgb >> 16) & 255) / 255,
                  green: Double((rgb >> 8) & 255) / 255,
                  blue: Double(rgb & 255) / 255)
    }
}
