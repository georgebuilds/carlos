package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/theme"
)

// TestApplyPalette_WiresEverySlot verifies that every chat-side
// package var picks up its corresponding slot from a freshly-applied
// theme.Palette. Catches drift if a new slot is added to Palette and
// the chat ApplyPalette forgets to wire it.
func TestApplyPalette_WiresEverySlot(t *testing.T) {
	// Snapshot + restore so the test doesn't pollute other tests in
	// the same package.
	t.Cleanup(func() {
		ApplyPalette(theme.Load(theme.Options{}))
	})

	p := theme.Palette{
		Accent: lipgloss.Color("#aa0000"),
		Muted:  lipgloss.Color("#aa1111"),
		User:   lipgloss.Color("#aa2222"),
		Agent:  lipgloss.Color("#aa3333"),
		Tool:   lipgloss.Color("#aa4444"),
		Warn:   lipgloss.Color("#aa5555"),
		OK:     lipgloss.Color("#aa6666"),
		Subtle: lipgloss.Color("#aa7777"),
	}
	ApplyPalette(p)

	cases := []struct {
		name string
		got  lipgloss.Color
		want lipgloss.Color
	}{
		{"Accent", colorAccent, p.Accent},
		{"Muted", colorMuted, p.Muted},
		{"User", colorUser, p.User},
		{"Agent", colorAgent, p.Agent},
		{"Tool", colorTool, p.Tool},
		{"Warn", colorWarn, p.Warn},
		{"OK", colorOK, p.OK},
		{"Subtle", colorSubtle, p.Subtle},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, string(c.got), string(c.want))
		}
	}
}

// TestApplyPalette_NoColor verifies that a NO_COLOR-loaded palette
// zeroes the chat slots — lipgloss treats empty as "no styling" so
// the result is plain monochrome text.
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
	for _, c := range []struct {
		name string
		c    lipgloss.Color
	}{
		{"Accent", colorAccent},
		{"User", colorUser},
		{"Tool", colorTool},
	} {
		if string(c.c) != "" {
			t.Errorf("NO_COLOR: %s = %q, want empty", c.name, string(c.c))
		}
	}
}

// TestStateBadge_ContainsGlyphAndLabel is the chat-side mirror of the
// manage test. The header badge must carry both shape and label so
// the focused-agent header is colorblind / NO_COLOR safe.
func TestStateBadge_ContainsGlyphAndLabel(t *testing.T) {
	cases := []struct {
		name  string
		state agent.State
		glyph string
		label string
	}{
		{"running", agent.StateRunning, "●", "running"},
		{"failed", agent.StateFailed, "✗", "failed"},
		{"awaiting-input", agent.StateAwaitingInput, "◆", "awaiting-input"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := stateBadge(c.state)
			if !strings.Contains(out, c.glyph) {
				t.Errorf("stateBadge(%s) = %q: missing glyph %q", c.state, out, c.glyph)
			}
			if !strings.Contains(out, c.label) {
				t.Errorf("stateBadge(%s) = %q: missing label %q", c.state, out, c.label)
			}
		})
	}
}

// TestStateBadge_GlyphBeforeLabel — glyph must precede the label so
// the eye lands on the shape first. Same contract as manage's badge.
func TestStateBadge_GlyphBeforeLabel(t *testing.T) {
	out := stateBadge(agent.StateRunning)
	gIdx := strings.Index(out, "●")
	lIdx := strings.Index(out, "running")
	if gIdx < 0 || lIdx < 0 {
		t.Fatalf("stateBadge(running) = %q: glyph or label missing", out)
	}
	if gIdx >= lIdx {
		t.Errorf("stateBadge(running) = %q: glyph %d must precede label %d", out, gIdx, lIdx)
	}
}

// TestStateBadge_NoColor_GlyphStillDistinguishes — accessibility
// proof for the chat header. With NO_COLOR applied, running vs failed
// still differ because their glyphs differ; without the glyph the two
// would collapse into identical "[<state>]" text.
func TestStateBadge_NoColor_GlyphStillDistinguishes(t *testing.T) {
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

	running := stateBadge(agent.StateRunning)
	failed := stateBadge(agent.StateFailed)
	if running == failed {
		t.Fatalf("NO_COLOR: stateBadge(running)=%q must differ from stateBadge(failed)=%q", running, failed)
	}
	if !strings.Contains(running, "●") {
		t.Errorf("NO_COLOR running badge %q missing glyph ●", running)
	}
	if !strings.Contains(failed, "✗") {
		t.Errorf("NO_COLOR failed badge %q missing glyph ✗", failed)
	}
}
