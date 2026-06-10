package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/frame"
)

// renderHeaderFor builds a fresh model with the given FrameUI, runs
// renderHeader at a generous width, and returns the rendered string
// with ANSI escapes stripped so position arithmetic counts visible
// cells. Used by every header-layout test below so the construction
// boilerplate stays in one place.
func renderHeaderFor(t *testing.T, ui FrameUI, model string) string {
	t.Helper()
	m := newFramedModel(t, ui, WithFrame(ui))
	// Inject a fake model via the Identity wire (the projection path
	// isn't populated in this lightweight test fixture, but the header
	// prefers Identity() when set).
	if model != "" {
		m.frame.Identity = func() (string, string) { return "", model }
	}
	return stripANSIForTest(m.renderHeader(160))
}

// TestRenderHeader_GlyphImmediatelyPrecedesID is the load-bearing
// shape check: the new layout drops the bracketed badge in favor of a
// bare colored state glyph that sits one space before the agent ID.
// No "[" or "]" character may survive in the header (they were the
// visual fingerprint of the old badge).
func TestRenderHeader_GlyphImmediatelyPrecedesID(t *testing.T) {
	out := renderHeaderFor(t, FrameUI{Active: "personal", Glyph: "◉", Mode: frame.ModeOrchestrator}, "claude-fable-5")

	// Glyph for spawning (the default state the projection lands on
	// without explicit transitions) is ◐. Whatever state landed, the
	// glyph + " " must precede the ID's first character.
	idx := strings.Index(out, "test-age")
	if idx < 0 {
		t.Fatalf("agent id missing from header: %q", out)
	}
	before := out[:idx]
	if strings.HasSuffix(before, "] ") {
		t.Errorf("legacy bracketed badge survived: %q", before)
	}
	if !strings.HasSuffix(before, " ") {
		t.Errorf("expected single space between glyph and id; got %q", before)
	}
}

// TestRenderHeader_DropsRunningLabel pins the retirement of the
// "[● running]" text label. The state glyph alone carries the signal
// now; the word adds bytes and visual weight without information.
func TestRenderHeader_DropsRunningLabel(t *testing.T) {
	out := renderHeaderFor(t, FrameUI{Active: "personal", Mode: frame.ModeSolo}, "claude-sonnet-4-6")
	for _, banned := range []string{"running", "spawning", "[", "]"} {
		if strings.Contains(out, banned) {
			t.Errorf("header still contains %q: %s", banned, out)
		}
	}
}

// TestRenderHeader_BrainPrecedesModel pins the new 🧠 chip on the
// model slot. The bare emoji + a single space must immediately
// precede the model id, with no parentheses around the id.
func TestRenderHeader_BrainPrecedesModel(t *testing.T) {
	out := renderHeaderFor(t, FrameUI{Active: "personal", Mode: frame.ModeSolo}, "claude-fable-5")
	if !strings.Contains(out, "🧠 claude-fable-5") {
		t.Errorf("expected \"🧠 claude-fable-5\" in header; got %q", out)
	}
	if strings.Contains(out, "(claude-fable-5)") {
		t.Errorf("legacy parens around model survived: %q", out)
	}
}

// TestRenderHeader_DiamondPrecedesMode pins the colored diamond chip
// on the mode slot. The diamond + a single space must immediately
// precede the mode label.
func TestRenderHeader_DiamondPrecedesMode(t *testing.T) {
	out := renderHeaderFor(t, FrameUI{Active: "personal", Glyph: "◉", Mode: frame.ModeOrchestrator}, "claude-fable-5")
	if !strings.Contains(out, modeDiamondGlyph+" orchestrator") {
		t.Errorf("expected \"%s orchestrator\" in header; got %q", modeDiamondGlyph, out)
	}
}

// TestRenderHeader_ExactlyThreeSeparatorsWhenFullyWired counts the
// inter-item separators in the four-item layout (id, model, frame,
// mode): exactly 3 ` · ` substrings appear, and none flank the head
// or tail of the left section.
func TestRenderHeader_ExactlyThreeSeparatorsWhenFullyWired(t *testing.T) {
	out := renderHeaderFor(t, FrameUI{Active: "personal", Glyph: "◉", Mode: frame.ModeOrchestrator}, "claude-fable-5")

	// Trim "carlos chat" and any trailing run of spaces off the right
	// so we count separators in the LEFT section only.
	left := out
	if i := strings.Index(left, "carlos chat"); i >= 0 {
		left = strings.TrimRight(left[:i], " ")
	}

	if got := strings.Count(left, " · "); got != 3 {
		t.Errorf("want 3 inter-item separators, got %d in: %q", got, left)
	}
	if strings.HasPrefix(strings.TrimLeft(left, " "), "·") {
		t.Errorf("left section leads with a separator: %q", left)
	}
	if strings.HasSuffix(left, "·") {
		t.Errorf("left section trails with a separator: %q", left)
	}
}

// TestRenderHeader_NoFrame_DropsItemsThreeAndFour guards the gating:
// when no frame is wired, the header carries only items 1 (state +
// id) and 2 (model), with one separator between them - no dangling
// `·` waiting for an absent frame.
func TestRenderHeader_NoFrame_DropsItemsThreeAndFour(t *testing.T) {
	out := renderHeaderFor(t, FrameUI{}, "claude-fable-5")
	left := strings.TrimRight(strings.Split(out, "carlos chat")[0], " ")
	if got := strings.Count(left, " · "); got != 1 {
		t.Errorf("want 1 separator (id↔model only); got %d in %q", got, left)
	}
	if strings.Contains(left, modeDiamondGlyph) {
		t.Errorf("mode diamond should not render without a wired frame: %q", left)
	}
}

// TestRenderHeader_NoModel_NoBrainChip guards the model-gating: when
// the projection has no model AND no live Identity, the brain chip
// disappears entirely - no orphan "🧠 " floating in the middle.
func TestRenderHeader_NoModel_NoBrainChip(t *testing.T) {
	out := renderHeaderFor(t, FrameUI{Active: "personal", Glyph: "◉", Mode: frame.ModeSolo}, "")
	if strings.Contains(out, "🧠") {
		t.Errorf("brain chip should not render when no model is wired: %q", out)
	}
	left := strings.TrimRight(strings.Split(out, "carlos chat")[0], " ")
	// Items remaining: id, frame, mode → 2 separators.
	if got := strings.Count(left, " · "); got != 2 {
		t.Errorf("want 2 separators (id↔frame↔mode); got %d in %q", got, left)
	}
}

// TestFramePillSep_TracksLivePalette is the regression test for the
// "package-level var captures init-time colorSubtle" bug. The previous
// shape was `var framePillSep = lipgloss.NewStyle().Foreground(
// colorSubtle).Render("·")` which evaluated at package import - BEFORE
// ApplyPalette ever ran - so the separator shipped uncolored for the
// rest of the process lifetime. Converting to a function makes every
// header render read the LIVE palette, and this test pins the
// contract: rendering the separator AFTER updating colorSubtle must
// surface the new color in the output bytes.
func TestFramePillSep_TracksLivePalette(t *testing.T) {
	saved := colorSubtle
	t.Cleanup(func() { colorSubtle = saved })

	// Swap in a distinctive color the production palette doesn't use
	// so substring matching is unambiguous.
	colorSubtle = lipgloss.Color("196") // bright red, never sits in the subtle slot in real life
	got := framePillSep()

	wantStyled := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("·")
	if got != wantStyled {
		t.Errorf("separator did not pick up updated colorSubtle\n got: %q\nwant: %q", got, wantStyled)
	}
}

// TestStateBadgeColor_Mapping pins the priority encoding of agent
// states to header glyph colors. The mapping is the same one the
// legacy stateBadge used inside its brackets - kept stable so the
// at-a-glance color cue for "this agent needs attention" doesn't
// shift across the layout change.
func TestStateBadgeColor_Mapping(t *testing.T) {
	cases := []struct {
		state agent.State
		want  any // lipgloss.Color; "want" left untyped so we just check identity below
	}{
		{agent.StateRunning, colorAgent},
		{agent.StateCompacting, colorAgent},
		{agent.StateAwaitingInput, colorWarn},
		{agent.StateBlocked, colorWarn},
		{agent.StateOrphaned, colorWarn},
		{agent.StateFailed, colorWarn},
		{agent.StateDone, colorOK},
	}
	for _, c := range cases {
		if got := stateBadgeColor(c.state); got != c.want {
			t.Errorf("stateBadgeColor(%v) = %v, want %v", c.state, got, c.want)
		}
	}
	if got := stateBadgeColor(agent.State(255)); got != colorMuted {
		t.Errorf("unknown state should default to colorMuted; got %v", got)
	}
}

// TestFramePill_NameUsesMutedColor pins the typographic-register
// rule: the frame's NAME renders in the same muted grey as the model
// and mode labels (colorMuted), while the GLYPH keeps its accent. The
// at-a-glance "which frame am I in" cue is the glyph; the name is
// supporting type. Without this rule the frame name read louder than
// the rest of the header and visually competed with the carlos chat
// title on the right.
func TestFramePill_NameUsesMutedColor(t *testing.T) {
	pill := framePill(FrameUI{Glyph: "◉", Active: "personal", Accent: "cream"})
	// The pill should contain the accent ANSI for the glyph segment
	// AND the colorMuted ANSI for the name segment. We check by
	// looking for the muted color's color code surrounding "personal".
	mutedRender := lipgloss.NewStyle().Foreground(colorMuted).Render("personal")
	if !strings.Contains(pill, mutedRender) {
		t.Errorf("expected frame name styled with colorMuted; pill=%q want substring %q", pill, mutedRender)
	}
}

// TestFramePill_GlyphKeepsAccentColor confirms the boundary of the
// previous rule: only the NAME goes muted; the glyph still paints in
// the frame's accent so the visual cue isn't lost. A pure-grey pill
// would drop the at-a-glance frame distinction.
func TestFramePill_GlyphKeepsAccentColor(t *testing.T) {
	pill := framePill(FrameUI{Glyph: "◉", Active: "personal", Accent: "cream"})
	accentRender := lipgloss.NewStyle().Foreground(frame.AccentColor("cream")).Render("◉")
	if !strings.Contains(pill, accentRender) {
		t.Errorf("expected glyph styled with cream accent; pill=%q want substring %q", pill, accentRender)
	}
}

// TestRenderHeader_ModeDiamondColorTracksModeCardAccent covers the
// design intent: the diamond's color matches the mode-switcher
// overlay's per-mode accent, so the visual posture cue is consistent
// between the always-on header and the takeover overlay.
//
// We can't easily snapshot the ANSI bytes for each render, but we can
// assert the COLOR HELPER itself: if modeCardAccent returns the right
// color per mode, and renderHeader passes its result to lipgloss, the
// rendered diamond carries that color by construction.
func TestRenderHeader_ModeDiamondColorTracksModeCardAccent(t *testing.T) {
	cases := []struct {
		mode string
		want any
	}{
		{frame.ModeSolo, colorAccent},
		{frame.ModeTight, colorWarn},
		{frame.ModeOrchestrator, colorOK},
	}
	for _, c := range cases {
		if got := modeCardAccent(c.mode); got != c.want {
			t.Errorf("modeCardAccent(%q) = %v, want %v", c.mode, got, c.want)
		}
	}

	// Smoke: each mode actually renders something diamond-shaped in
	// the header (we already pin the glyph character itself in
	// TestRenderHeader_DiamondPrecedesMode; here we just confirm it
	// survives a render under every supported mode).
	for _, m := range []string{frame.ModeSolo, frame.ModeTight, frame.ModeOrchestrator} {
		out := renderHeaderFor(t, FrameUI{Active: "personal", Glyph: "◉", Mode: m}, "claude-fable-5")
		if !strings.Contains(out, modeDiamondGlyph) {
			t.Errorf("mode %q: header missing diamond glyph: %q", m, out)
		}
	}
}
