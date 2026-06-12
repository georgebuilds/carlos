package theme

import "github.com/georgebuilds/carlos/internal/agent"

// Composer chip sigils (slice I-1). One single-cell glyph per chip
// kind so the shape alone encodes the kind under NO_COLOR, mirroring
// the StateGlyph contract below. Color pairing happens at the render
// site (chat composer): paste=Tool, image=OK, mention=Accent.
//
// Named constants on purpose: ChipSigilPaste is U+2307 WAVY LINE,
// which sits outside the Geometric Shapes block the rest of our
// glyphs come from and has weaker monospace-font coverage (it can
// render as tofu in older terminal fonts). If field reports confirm
// that, the swap is this one line.
const (
	ChipSigilPaste   = "⌇" // U+2307 WAVY LINE - font-coverage risk, see above
	ChipSigilImage   = "▣" // U+25A3 WHITE SQUARE CONTAINING BLACK SMALL SQUARE
	ChipSigilMention = "◇" // U+25C7 WHITE DIAMOND
)

// StateGlyph returns the unicode shape paired with the colored badge for
// an agent state. Color encodes priority; the glyph encodes the state
// itself - together they survive both colorblind viewers and NO_COLOR
// monochrome rendering.
//
// Why theme owns this:
//
//   - Manage AND chat both render state badges. Pre-9c they each had
//     their own stateBadge with its own color switch. Slice 9a already
//     hoisted the palette out so the two surfaces stayed in lockstep;
//     this is the same move for glyphs. One source of truth means a
//     future tweak (new state, glyph swap) is a one-file change.
//
//   - theme is the centralized visual-atom package - palette lives here,
//     glyphs are the same kind of leaf primitive. Putting them in
//     internal/tui/manage would force chat to import manage just for
//     a rune, which is the wrong dependency direction.
//
// The mapping is deliberate. Shapes encode semantics:
//
//   - spawning/compacting are half-circles in different phases ("coming
//     online" vs "mid-cycle")
//   - queued is a dotted circle ("placeholder, not yet running")
//   - running is a filled disc ("active")
//   - awaiting-input is a diamond ("user attention here")
//   - blocked is a filled square ("stuck, opaque")
//   - paused-by-user is a vertical bar (a pause-button stroke)
//   - cancelling is a diamond-with-crossmarks ("being torn down")
//   - done/failed are check/cross
//   - orphaned is an empty circle ("labeled but absent")
//
// Every glyph is in the Unicode Geometric Shapes block (U+25xx) or the
// Dingbats block (U+27xx, checkmark/cross), hand-picked to render as a
// single terminal cell in every common monospace font. Emoji are
// avoided - their cell width varies between terminals (some 1, some 2),
// which breaks the manage roster's column alignment.
//
// Returns "·" (U+00B7 middle dot) for any unknown / future state value
// - visually neutral so a forgotten case prints something rather than
// nothing and an alert reader can spot the missing mapping.
func StateGlyph(s agent.State) string {
	switch s {
	case agent.StateSpawning:
		return "◐"
	case agent.StateQueued:
		return "◌"
	case agent.StateRunning:
		return "●"
	case agent.StateAwaitingInput:
		return "◆"
	case agent.StateBlocked:
		return "◼"
	case agent.StatePausedByUser:
		return "▮"
	case agent.StateCompacting:
		return "◓"
	case agent.StateCancelling:
		return "◈"
	case agent.StateDone:
		return "✓"
	case agent.StateFailed:
		return "✗"
	case agent.StateOrphaned:
		return "◯"
	}
	return "·"
}
