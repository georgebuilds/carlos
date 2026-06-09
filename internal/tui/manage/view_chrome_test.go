package manage

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestView_OuterBorderPresent confirms the v0.7.3 swap back to a
// rounded outer border (we briefly dropped it in v0.7.2 to fix the
// Ghostty top-clip; better arithmetic — Height(h-2), no leading
// "\n" margin — lets the box come back without the clip).
func TestView_OuterBorderPresent(t *testing.T) {
	log := openTempLog(t)
	seedAgent(t, log, "01HV0000000000000000000001", "", "alpha", "fake", agent.StateRunning)
	src := NewSQLiteSnapshotSource(log)
	m := New(src, log, nil)
	m = driveModel(t, m, 160, 40)

	rendered := m.View()
	rows := strings.Split(rendered, "\n")
	if len(rows) < 4 {
		t.Fatalf("view too short: %d rows", len(rows))
	}
	// First non-blank row should be the rounded-border top edge,
	// starting with the rounded-top-left corner "╭".
	first := strings.TrimSpace(stripANSI(rows[0]))
	if !strings.HasPrefix(first, "╭") {
		t.Errorf("first row should start with the rounded-corner '╭'; got:\n%q", first)
	}
}

// TestRenderHRule_LengthAndContent locks the section divider:
// produces exactly w cells of light horizontal glyph, used as a
// thin rule between the status bar / body / footer.
func TestRenderHRule_LengthAndContent(t *testing.T) {
	got := renderHRule(40)
	// Strip ANSI before counting cells.
	plain := stripANSI(got)
	if got, want := len([]rune(plain)), 40; got != want {
		t.Errorf("rule width = %d runes, want %d", got, want)
	}
	if !strings.Contains(plain, "─") {
		t.Errorf("rule should be made of '─' glyphs; got %q", plain)
	}
}

// TestRenderHRule_NegativeWidthDoesNotPanic guards the floor
// branch: a narrow / zero / negative width must produce a single-
// cell rule rather than panic.
func TestRenderHRule_NegativeWidthDoesNotPanic(t *testing.T) {
	if got := renderHRule(0); got == "" {
		t.Error("rule with w=0 should still render one cell")
	}
	if got := renderHRule(-5); got == "" {
		t.Error("rule with w<0 should still render one cell")
	}
}

// stripANSI is a tiny helper that drops ANSI escape sequences for
// snapshot-style assertions. Bubbletea + lipgloss output is full of
// them; we only care about the glyph shape here.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if r == 0x1b {
			inEsc = true
			continue
		}
		if inEsc {
			// CSI runs end at a letter; OSC runs end at BEL (0x07) or
			// ST (ESC \). For our snapshot purposes a coarse "drop
			// until we see a letter or string-terminator" is enough.
			if (r >= '@' && r <= '~') || r == 0x07 {
				inEsc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
