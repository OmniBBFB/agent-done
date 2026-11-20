// Hallmark · redesign: full · shape: app screen · style: cool white and petrol teal · signature: the live request queue
// Hallmark · pre-emit critique: P4 H4 E4 S4 R5 V4 · lead with each session's actual status or request, and use native controls throughout
import SwiftUI

enum WorkspaceStyle {
    static let sectionSpacing: CGFloat = 14
    static let entrySpacing: CGFloat = 6
    static let surfaceRadius: CGFloat = 10

    static func palette(for scheme: ColorScheme, highContrast: Bool) -> ThemePalette {
        switch scheme {
        case .dark: .init(0x151C23, 0x202A32, 0xEFF5F8, 0xB5C4CD, 0x78DDD0, 0xFFACB9, highContrast)
        default: .init(0xF2F6F9, 0xFAFCFD, 0x17222C, 0x455460, 0x006961, 0xA12E3C, highContrast)
        }
    }
}

struct ThemePalette {
    let canvas: Color
    let surface: Color
    let ink: Color
    let muted: Color
    let accent: Color
    let danger: Color
    let rule: Color

    init(
        _ canvas: UInt32, _ surface: UInt32, _ ink: UInt32, _ muted: UInt32,
        _ accent: UInt32, _ danger: UInt32, _ highContrast: Bool
    ) {
        self.canvas = Color(rgb: canvas)
        self.surface = Color(rgb: surface)
        self.ink = Color(rgb: ink)
        self.muted = Color(rgb: highContrast ? ink : muted)
        self.accent = Color(rgb: accent)
        self.danger = Color(rgb: danger)
        rule = Color(rgb: ink).opacity(highContrast ? 0.65 : 0.25)
    }
}

struct WorkspaceHeading: View {
    let title: String
    let palette: ThemePalette

    var body: some View {
        Text(title)
            .font(.largeTitle.bold())
            .foregroundStyle(palette.ink)
            .accessibilityAddTraits(.isHeader)
            .fixedSize(horizontal: false, vertical: true)
    }
}

struct WorkspaceSectionTitle: View {
    let title: String
    let palette: ThemePalette

    var body: some View {
        Text(title)
            .font(.subheadline.weight(.semibold))
            .foregroundStyle(palette.ink)
            .textCase(nil)
            .accessibilityAddTraits(.isHeader)
    }
}

extension View {
    func workspaceCanvas(_ palette: ThemePalette) -> some View {
        frame(maxWidth: 720)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(palette.canvas)
    }
}

private extension Color {
    init(rgb: UInt32) {
        self.init(red: Double((rgb >> 16) & 255) / 255,
                  green: Double((rgb >> 8) & 255) / 255,
                  blue: Double(rgb & 255) / 255)
    }
}
