package onboarding

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestRenderStepDots_PulseFramesGoIntermediate proves the slice-9e
// microinteraction renders the current dot through ◐ → ◉ → ● over
// three pulse frames before settling.
func TestRenderStepDots_PulseFramesGoIntermediate(t *testing.T) {
	const total = 6
	const cur = 2
	cases := []struct {
		frame int
		want  string
	}{
		{0, "●"}, // no animation in flight; static fill
		{1, "◐"}, // first frame
		{2, "◉"}, // mid frame (brightest)
		{3, "●"}, // settle frame
	}
	for _, c := range cases {
		out := renderStepDots(cur, total, c.frame)
		// renderStepDots joins dots with spaces. Position `cur` is the
		// (cur+1)-th glyph. Splitting on space picks it out cheaply.
		dots := strings.Split(stripStyle(out), " ")
		if got := dots[cur]; got != c.want {
			t.Errorf("frame=%d: dot[%d] = %q, want %q (full row %q)", c.frame, cur, got, c.want, out)
		}
	}
}

// TestPulseGlyph_KnownFrames covers the helper used by renderStepDots.
func TestPulseGlyph_KnownFrames(t *testing.T) {
	cases := map[int]string{
		1: "◐",
		2: "◉",
		3: "●",
		4: "●", // out-of-range falls back to settled glyph
		0: "●",
	}
	for f, want := range cases {
		if got := pulseGlyph(f); got != want {
			t.Errorf("pulseGlyph(%d) = %q, want %q", f, got, want)
		}
	}
}

// TestFlow_AdvanceTriggersPulse proves advance kicks the pulse loop
// (pulseFrame = 1 + a Cmd scheduled). Subsequent ticks walk the
// frame to 2 → 3 → 0; we exercise the full chain via Update.
func TestFlow_AdvanceTriggersPulse(t *testing.T) {
	f := New()
	if f.pulseFrame != 0 {
		t.Fatalf("pulseFrame nonzero before advance: %d", f.pulseFrame)
	}
	// Inject a nextScreenMsg directly so we don't need a child Update.
	next, cmd := f.Update(nextScreenMsg{})
	ff := next.(*Flow)
	if ff.pulseFrame != 1 {
		t.Errorf("pulseFrame after first advance = %d, want 1", ff.pulseFrame)
	}
	if cmd == nil {
		t.Fatal("advance returned nil cmd; want a scheduled tick")
	}
	// Walk the pulse to completion: each tick bumps the frame; after
	// frame 3 the next tick resets to 0.
	for want := 2; want <= 3; want++ {
		next, cmd = ff.Update(pulseTickMsg{})
		ff = next.(*Flow)
		if ff.pulseFrame != want {
			t.Errorf("after tick %d: pulseFrame = %d, want %d", want, ff.pulseFrame, want)
		}
		if cmd == nil {
			t.Fatalf("tick at frame %d returned nil cmd before settle", want)
		}
	}
	// One more tick settles.
	next, cmd = ff.Update(pulseTickMsg{})
	ff = next.(*Flow)
	if ff.pulseFrame != 0 {
		t.Errorf("after settle tick: pulseFrame = %d, want 0", ff.pulseFrame)
	}
	if cmd != nil {
		t.Error("settle tick should return nil cmd; pulse loop is done")
	}
}

// stripStyle drops lipgloss SGR escape sequences so tests can match
// glyphs without worrying about colors. Naive but sufficient for the
// short rows step-dot rendering produces.
func stripStyle(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == 0x1b: // ESC begins a CSI sequence
			in = true
		case in && r == 'm':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Ensure pulseTickMsg satisfies tea.Msg so the Update chain type-checks.
var _ tea.Msg = pulseTickMsg{}
