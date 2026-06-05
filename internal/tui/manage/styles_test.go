package manage

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/theme"
)

// TestStateBadge_ContainsGlyphAndLabel asserts the slice-9c contract:
// every rendered badge carries BOTH the unicode shape (from
// theme.StateGlyph) AND the human-readable state name. Color is an
// accelerator; the glyph + label are the canonical signals.
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
		{"done", agent.StateDone, "✓", "done"},
		{"orphaned", agent.StateOrphaned, "◯", "orphaned"},
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

// TestStateBadge_GlyphBeforeLabel pins the visual order: the eye must
// land on the shape first so colorblind users get the signal before
// they parse the word. If a future edit swaps the order this test
// fires before screenshots do.
func TestStateBadge_GlyphBeforeLabel(t *testing.T) {
	out := stateBadge(agent.StateRunning)
	gIdx := strings.Index(out, "●")
	lIdx := strings.Index(out, "running")
	if gIdx < 0 || lIdx < 0 {
		t.Fatalf("stateBadge(running) = %q: glyph or label missing", out)
	}
	if gIdx >= lIdx {
		t.Errorf("stateBadge(running) = %q: glyph at %d must precede label at %d", out, gIdx, lIdx)
	}
}

// TestStateBadge_NoColor_GlyphStillDistinguishes is the accessibility
// proof. With a NO_COLOR palette applied, both badges render through
// the SAME monochrome style — color carries no signal — but the glyph
// alone keeps two distinct states distinct. Without this guarantee,
// 9c's whole premise (shape as redundant encoding) fails.
func TestStateBadge_NoColor_GlyphStillDistinguishes(t *testing.T) {
	// Restore the default palette when this test ends so it doesn't
	// pollute neighbours in the same package.
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
	// And specifically — the differing bytes must include the glyphs,
	// not just the label. Confirm both glyphs are present.
	if !strings.Contains(running, "●") {
		t.Errorf("NO_COLOR running badge %q missing glyph ●", running)
	}
	if !strings.Contains(failed, "✗") {
		t.Errorf("NO_COLOR failed badge %q missing glyph ✗", failed)
	}
}
