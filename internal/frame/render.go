package frame

import "github.com/charmbracelet/lipgloss"

// AccentColor maps the curated palette name to a lipgloss color. Names
// outside the palette return the empty color so callers naturally fall
// back to the theme's default foreground. Hex values are chosen to read
// against both the light and dark theme variants — the test in
// render_test.go covers the contrast contract.
//
// Used by the chat header pill, the future 3x2 takeover-switcher tile
// border, and the headless inline picker so the same accent renders
// consistently across surfaces.
func AccentColor(name string) lipgloss.Color {
	switch name {
	case "rust":
		return lipgloss.Color("#c14a3a")
	case "slate":
		return lipgloss.Color("#6a7a8b")
	case "olive":
		return lipgloss.Color("#8b8f5a")
	case "teal":
		return lipgloss.Color("#4a9090")
	case "plum":
		return lipgloss.Color("#8a5a8b")
	case "cream":
		return lipgloss.Color("#c4a374")
	case "sand":
		return lipgloss.Color("#b89868")
	case "navy":
		return lipgloss.Color("#4a6a9a")
	}
	return lipgloss.Color("")
}

// Pill renders a compact frame label of the shape "●name" with the
// frame's glyph painted in the frame's accent. Used by the chat header
// and the inline picker. When NO_COLOR is set (passed as noColor=true),
// the glyph still distinguishes frames so colorblind/no-color users
// don't lose the signal.
//
// Caller passes the glyph rather than the frame so this helper stays
// pure — easier to test, easier to reuse from a Frame-less context (the
// new-frame wizard).
func Pill(glyph, name, accent string, noColor bool) string {
	if glyph == "" {
		glyph = DefaultGlyphFor(name)
	}
	if noColor {
		return glyph + name
	}
	col := AccentColor(accent)
	if col == "" {
		return glyph + " " + name
	}
	return lipgloss.NewStyle().Foreground(col).Render(glyph) + " " + name
}
