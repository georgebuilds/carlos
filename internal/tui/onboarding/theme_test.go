package onboarding

import (
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/theme"
)

// TestApplyPalette_WiresEverySlot pins onboarding's slot mapping. Like
// manage, onboarding's `colorWarn` historically aliased the amber/Tool
// slot; `colorSuccess` is OK.
func TestApplyPalette_WiresEverySlot(t *testing.T) {
	t.Cleanup(func() {
		ApplyPalette(theme.Load(theme.Options{}))
	})

	p := theme.Palette{
		Accent: lipgloss.Color("#aa0000"),
		Muted:  lipgloss.Color("#aa1111"),
		Tool:   lipgloss.Color("#aa3333"), // → colorWarn
		OK:     lipgloss.Color("#aa5555"), // → colorSuccess
		Brand:  lipgloss.Color("#aa7777"),
	}
	ApplyPalette(p)

	cases := []struct {
		name string
		got  lipgloss.Color
		want lipgloss.Color
	}{
		{"colorBrand", colorBrand, p.Brand},
		{"colorAccent", colorAccent, p.Accent},
		{"colorMuted", colorMuted, p.Muted},
		{"colorWarn←Tool", colorWarn, p.Tool},
		{"colorSuccess←OK", colorSuccess, p.OK},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, string(c.got), string(c.want))
		}
	}
}

// TestApplyPalette_RebuildsStyles verifies that the cached style values
// (styleTagline etc.) reflect the new colors after ApplyPalette is
// called. lipgloss styles capture colors by value at construction; if
// we forgot to rebuildStyles, the styles would stay stuck on the
// init-time palette and a NO_COLOR / accent override would silently
// have no visual effect.
//
// We assert against Style.GetForeground() rather than Render() output:
// rendered output depends on terminfo and the test harness's
// COLORTERM/TERM env, which lipgloss may downgrade to plain ASCII —
// making before/after compare identical even when the underlying color
// changed. The Foreground field IS the value we care about wiring.
func TestApplyPalette_RebuildsStyles(t *testing.T) {
	t.Cleanup(func() {
		ApplyPalette(theme.Load(theme.Options{}))
	})

	ApplyPalette(theme.Palette{Accent: lipgloss.Color("#ff00ff")})
	if got := styleTagline.GetForeground(); got != lipgloss.Color("#ff00ff") {
		t.Errorf("styleTagline foreground = %v, want #ff00ff", got)
	}
	if got := stylePrompt.GetForeground(); got != lipgloss.Color("#ff00ff") {
		t.Errorf("stylePrompt foreground = %v, want #ff00ff", got)
	}

	ApplyPalette(theme.Palette{Accent: lipgloss.Color("#00ff00")})
	if got := styleTagline.GetForeground(); got != lipgloss.Color("#00ff00") {
		t.Errorf("styleTagline foreground after second Apply = %v, want #00ff00", got)
	}
}
