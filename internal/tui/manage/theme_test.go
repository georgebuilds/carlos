package manage

import (
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/theme"
)

// TestApplyPalette_WiresEverySlot pins manage's slot mapping. Note the
// historical naming mismatch: manage's `colorWarn` was the amber/Tool
// slot (214), while its `colorErr` was the red/Warn slot (203).
// ApplyPalette routes accordingly; this test keeps that mapping honest.
func TestApplyPalette_WiresEverySlot(t *testing.T) {
	t.Cleanup(func() {
		ApplyPalette(theme.Load(theme.Options{}))
	})

	p := theme.Palette{
		Accent: lipgloss.Color("#aa0000"),
		Muted:  lipgloss.Color("#aa1111"),
		Agent:  lipgloss.Color("#aa2222"),
		Tool:   lipgloss.Color("#aa3333"), // → colorWarn
		Warn:   lipgloss.Color("#aa4444"), // → colorErr
		OK:     lipgloss.Color("#aa5555"),
		Subtle: lipgloss.Color("#aa6666"),
		Brand:  lipgloss.Color("#aa7777"),
		Cyan:   lipgloss.Color("#aa8888"),
		ErrHi:  lipgloss.Color("#aa9999"),
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
		{"colorOK", colorOK, p.OK},
		{"colorErr←Warn", colorErr, p.Warn},
		{"colorErrHi", colorErrHi, p.ErrHi},
		{"colorCyan", colorCyan, p.Cyan},
		{"colorAgent", colorAgent, p.Agent},
		{"colorSubtle", colorSubtle, p.Subtle},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, string(c.got), string(c.want))
		}
	}
}

// TestApplyPalette_NoColor verifies monochrome propagation into manage.
func TestApplyPalette_NoColor(t *testing.T) {
	t.Cleanup(func() {
		ApplyPalette(theme.Load(theme.Options{}))
	})
	ApplyPalette(theme.Load(theme.Options{
		Env: func(k string) string {
			if k == "NO_COLOR" {
				return "1"
			}
			return ""
		},
	}))
	if string(colorAccent) != "" {
		t.Errorf("NO_COLOR: colorAccent = %q, want empty", string(colorAccent))
	}
	if string(colorBrand) != "" {
		t.Errorf("NO_COLOR: colorBrand = %q, want empty", string(colorBrand))
	}
}
