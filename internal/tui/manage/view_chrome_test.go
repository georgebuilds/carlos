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
//
// Correctness note: CSI sequences are ESC `[` <params> <intermediate>
// <final> where final ∈ [0x40, 0x7e]. The `[` itself is 0x5b which is
// in that final-byte range, so a naïve "skip until 0x40-0x7e" loop
// terminates one byte too early and lets the SGR digits leak through.
// Special-case the leading `[` so the loop only considers bytes AFTER
// it as candidates for the final byte.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2 // skip ESC + [
			for j < len(s) {
				c := s[j]
				j++
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
			i = j
			continue
		}
		if s[i] == 0x1b {
			// Non-CSI escape (OSC, etc.). Skip until BEL or ST.
			j := i + 1
			for j < len(s) && s[j] != 0x07 {
				j++
			}
			if j < len(s) {
				j++ // consume the BEL
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
