package theme

import (
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
)

// allStates is the canonical roster — every State the supervisor can
// hold per internal/agent/supervisor.go. Mirrored here (not imported)
// because agent does not export a slice and we want the test to break
// LOUDLY if a new state is added without a matching glyph.
var allStates = []agent.State{
	agent.StateSpawning,
	agent.StateQueued,
	agent.StateRunning,
	agent.StateAwaitingInput,
	agent.StateBlocked,
	agent.StatePausedByUser,
	agent.StateCompacting,
	agent.StateCancelling,
	agent.StateDone,
	agent.StateFailed,
	agent.StateOrphaned,
}

// TestStateGlyph_EveryStateHasGlyph asserts the mapping is total over
// the 11 known states and that each glyph is non-empty AND occupies
// exactly one terminal cell (lipgloss.Width == 1). The cell-width
// check is load-bearing: an emoji glyph would render as width 2 in
// some terminals and break the manage roster's column alignment.
func TestStateGlyph_EveryStateHasGlyph(t *testing.T) {
	for _, s := range allStates {
		g := StateGlyph(s)
		if g == "" {
			t.Errorf("StateGlyph(%s) = empty, want non-empty", s)
			continue
		}
		if g == "·" {
			t.Errorf("StateGlyph(%s) = %q (the unknown fallback), want a state-specific glyph", s, g)
		}
		if w := lipgloss.Width(g); w != 1 {
			t.Errorf("StateGlyph(%s) = %q: width=%d, want 1 (single terminal cell)", s, g, w)
		}
		// Sanity — must be a single rune. Multi-rune glyphs (combining
		// sequences, ZWJ emoji) defeat column-alignment math elsewhere.
		if utf8.RuneCountInString(g) != 1 {
			t.Errorf("StateGlyph(%s) = %q: %d runes, want 1", s, g, utf8.RuneCountInString(g))
		}
	}
}

// TestStateGlyph_UnknownReturnsMiddleDot pins the fallback so callers
// can rely on a stable sentinel when the state value drifts out of the
// declared range (e.g. a stale projection row from a newer binary).
func TestStateGlyph_UnknownReturnsMiddleDot(t *testing.T) {
	// State is an int; anything outside the iota'd range is "unknown".
	// Pick a value comfortably past the last declared constant.
	bogus := agent.State(9999)
	if g := StateGlyph(bogus); g != "·" {
		t.Errorf("StateGlyph(unknown) = %q, want %q", g, "·")
	}
}

// TestChipSigils_SingleCellAndDistinct extends the StateGlyph cell
// contract to the slice I-1 composer chip sigils: each must be one
// rune, one terminal cell, and distinct from the others so the shape
// alone encodes the chip kind under NO_COLOR. The width==1 check is
// the canary for the documented U+2307 (ChipSigilPaste) font risk -
// if a sigil ever needs swapping, this test pins the replacement to
// the same constraints.
func TestChipSigils_SingleCellAndDistinct(t *testing.T) {
	sigils := map[string]string{
		"paste":   ChipSigilPaste,
		"image":   ChipSigilImage,
		"mention": ChipSigilMention,
	}
	seen := make(map[string]string, len(sigils))
	for name, g := range sigils {
		if w := lipgloss.Width(g); w != 1 {
			t.Errorf("ChipSigil %s = %q: width=%d, want 1 (single terminal cell)", name, g, w)
		}
		if n := utf8.RuneCountInString(g); n != 1 {
			t.Errorf("ChipSigil %s = %q: %d runes, want 1", name, g, n)
		}
		if prev, dup := seen[g]; dup {
			t.Errorf("sigil collision: %s and %s both map to %q", prev, name, g)
		}
		seen[g] = name
	}
}

// TestStateGlyph_AllDistinct enforces the accessibility promise: two
// different states must render to two different glyphs. Without this,
// the NO_COLOR fallback collapses (e.g. running and done both being
// `●` would defeat the whole slice).
func TestStateGlyph_AllDistinct(t *testing.T) {
	seen := make(map[string]agent.State, len(allStates))
	for _, s := range allStates {
		g := StateGlyph(s)
		if prev, dup := seen[g]; dup {
			t.Errorf("glyph collision: %s and %s both map to %q", prev, s, g)
		}
		seen[g] = s
	}
	if len(seen) != len(allStates) {
		t.Errorf("distinct glyph count = %d, want %d", len(seen), len(allStates))
	}
}
