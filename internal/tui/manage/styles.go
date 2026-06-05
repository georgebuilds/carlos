package manage

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/theme"
)

// Brand palette — package-level vars populated by [ApplyPalette].
//
// Centralized in internal/theme as of Phase 9 slice 9a. The legacy
// inline literals are now mapped from theme.Palette slots so a single
// config edit recolors chat + manage + onboarding in lockstep.
//
// Slot mapping:
//   - colorErr ← Palette.Warn  (manage's "red" was always the same
//     lipgloss.Color("203") chat called Warn)
//   - colorWarn ← Palette.Tool (manage's "amber 214" matches chat's Tool)
//
// init() seeds with autodetect defaults; main() overrides at startup.
var (
	colorBrand  lipgloss.Color
	colorAccent lipgloss.Color
	colorMuted  lipgloss.Color
	colorWarn   lipgloss.Color
	colorOK     lipgloss.Color
	colorErr    lipgloss.Color
	colorErrHi  lipgloss.Color
	colorCyan   lipgloss.Color
	colorAgent  lipgloss.Color
	colorSubtle lipgloss.Color
)

func init() {
	ApplyPalette(theme.Load(theme.Options{}))
}

// ApplyPalette wires a freshly-loaded [theme.Palette] into manage's
// color vars. See internal/tui/chat.ApplyPalette for the rationale; the
// shape is identical.
func ApplyPalette(p theme.Palette) {
	colorBrand = p.Brand
	colorAccent = p.Accent
	colorMuted = p.Muted
	// manage's pre-centralization colorWarn was the amber/214 slot —
	// the same shade chat called Tool.
	colorWarn = p.Tool
	colorOK = p.OK
	// manage's colorErr was 203 — the same shade chat called Warn.
	colorErr = p.Warn
	colorErrHi = p.ErrHi
	colorCyan = p.Cyan
	colorAgent = p.Agent
	colorSubtle = p.Subtle
}

// Minimum terminal size. Below this we refuse to render — the manage
// view needs the cells to keep two panes legible.
const (
	minTermWidth  = 100
	minTermHeight = 30
)

// stateColor returns the foreground color used by the roster badge for
// a given state. Per SPEC § "What the user monitors for", awaiting-
// input and orphaned are the loudest signals; everything else maps
// onto a small palette so the user can scan a row of badges and pick
// out anomalies at a glance.
//
// Color is never the sole signal — the badge always prints the state
// name in brackets. The color is just the accelerator.
func stateColor(s agent.State) lipgloss.Color {
	switch s {
	case agent.StateSpawning, agent.StateQueued:
		return colorMuted
	case agent.StateRunning:
		return colorAgent
	case agent.StateAwaitingInput:
		return colorAccent
	case agent.StateBlocked:
		return colorWarn
	case agent.StatePausedByUser:
		return colorCyan
	case agent.StateCompacting:
		return colorMuted
	case agent.StateCancelling:
		return colorWarn
	case agent.StateDone:
		return colorOK
	case agent.StateFailed:
		return colorErr
	case agent.StateOrphaned:
		return colorErrHi
	}
	return colorMuted
}

// stateBadge formats a state as a colored bracketed label. Compact
// because the roster row is already token-budgeted. The orphaned and
// awaiting-input badges go bold so they stand out from the row of
// regular-running siblings; compacting goes italic to read as a
// transient annotation.
//
// Slice 9c: a unicode glyph (theme.StateGlyph) is prefixed inside the
// brackets so the state is still distinguishable when color is stripped
// (NO_COLOR) or the viewer is colorblind. Glyph sits BEFORE the label
// so the eye lands on the shape first — color is an accelerator, shape
// is the canonical signal.
func stateBadge(s agent.State) string {
	style := lipgloss.NewStyle().Foreground(stateColor(s))
	switch s {
	case agent.StateOrphaned:
		style = style.Bold(true)
	case agent.StateAwaitingInput:
		style = style.Bold(true)
	case agent.StateCompacting:
		style = style.Italic(true)
	}
	return style.Render("[" + theme.StateGlyph(s) + " " + s.String() + "]")
}

// shortID truncates a ULID to the leading 8 chars for column display.
// Full IDs are still available via inspection of the log. Same helper
// as chat's; duplicated to keep the scope-discipline clean (chat is
// don't-edit, manage is a new package).
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
