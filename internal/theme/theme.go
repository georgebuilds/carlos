// Package theme is the single source of truth for the colors every TUI
// surface (chat, manage, onboarding) renders with.
//
// Why centralize:
//
//   - Before this package each TUI sub-package re-declared its own
//     copy of the brand palette as local lipgloss.Color vars (see the
//     legacy "duplicated from onboarding" comments in
//     chat/chat.go, manage/styles.go). Three copies drift independently;
//     a one-character hex fix needed three edits.
//
//   - `NO_COLOR` is a user-facing accessibility / pipe-to-less knob the
//     duplicated palette did not honor. Centralizing the construction
//     gives us exactly one place to enforce it.
//
//   - Light terminals exist. We autodetect via `COLORFGBG`, which most
//     modern terminal emulators set (kitty, alacritty, iTerm2, gnome-
//     terminal, vte family). Falling back to "dark" matches the
//     historical assumption baked into the literals.
//
//   - Users with a green / amber / purple aesthetic preference can
//     override the accent slot via `cfg.Theme.Accent` without
//     reskinning everything else; the rest of the palette stays
//     coherent.
//
// Architecture:
//
//   - [Palette] is the loaded color set; one per process. Construct it
//     once at startup via [Load], pass it explicitly to each TUI
//     package's `ApplyPalette(p)` entry point.
//
//   - [Load] is a pure function over [Options] + the process env. The
//     `Env` hook in Options lets tests inject a fake environment.
//
//   - No package-level mutable state lives here. The TUI packages keep
//     their `colorAccent` etc. vars so the existing callsites are
//     unchanged; their `ApplyPalette` is what populates them from a
//     Palette this package built.
package theme

import (
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Variant selects between the two hand-tuned base palettes.
//
// Dark assumes a near-black background (the historical assumption); it
// matches the literals chat/manage/onboarding used pre-centralization.
// Light assumes a near-white background; the accent darkens and the
// neutrals lighten so contrast stays legible.
type Variant int

const (
	// Dark is the default - the legacy palette.
	Dark Variant = iota
	// Light is the inverted-lightness variant for light-bg terminals.
	Light
)

// String returns "dark" / "light" - matches the YAML config tokens
// users type in `cfg.Theme.Variant`.
func (v Variant) String() string {
	if v == Light {
		return "light"
	}
	return "dark"
}

// Palette is the centralized color set every TUI surface consumes.
// Field names mirror the legacy `colorX` vars (User, Agent, Tool, …)
// so the TUI-side rename is a one-for-one fill-in.
//
// Slots are populated from [Load]; treat the struct as immutable once
// returned.
//
// When NoColor is true every color slot holds the empty lipgloss.Color,
// which lipgloss treats as "no styling" - the output is plain text.
// Bold / Italic still render in monochrome terminals, so emphasis
// survives. Callers that want to distinguish "no color, but still
// emphasize" can read NoColor and toggle Bold/Italic accordingly.
type Palette struct {
	// Accent is the brand color - borders, active highlights, key hints.
	Accent lipgloss.Color
	// Muted is the dim neutral - separators, hint text.
	Muted lipgloss.Color
	// User colors the human's chat messages.
	User lipgloss.Color
	// Agent colors the assistant's chat messages and the manage roster
	// body text. Neutral-bright on dark; near-black on light.
	Agent lipgloss.Color
	// Tool colors tool-call markers and amber-coded statuses (e.g.
	// "blocked" badges in manage).
	Tool lipgloss.Color
	// Warn colors error states and orphaned badges.
	Warn lipgloss.Color
	// OK colors success / done states.
	OK lipgloss.Color
	// Subtle is a between-Muted-and-Agent neutral; tertiary text.
	Subtle lipgloss.Color
	// Brand is the deep brand color (cap navy) used by onboarding +
	// manage as a darker counterpart to Accent.
	Brand lipgloss.Color
	// Cyan is the paused-by-user signal in manage.
	Cyan lipgloss.Color
	// ErrHi is the bright-red used for orphaned badges in manage (a
	// louder Warn).
	ErrHi lipgloss.Color

	// Variant records which base palette was selected. Renderers can
	// read this to make variant-aware decisions (e.g. choosing a darker
	// shade of green when on light).
	Variant Variant
	// NoColor records whether `NO_COLOR` was set at construction time.
	// When true every color slot is empty; downstream renderers should
	// lean on Bold/Italic for emphasis instead of color.
	NoColor bool
}

// Options drives [Load]. All fields optional; an empty Options reads
// the real process env and applies the autodetect rules.
type Options struct {
	// ForcedVariant overrides autodetect when set to "dark" or "light".
	// Empty string falls through to autodetect. Sourced from
	// `cfg.Theme.Variant` in main.
	ForcedVariant string
	// AccentOverride is a hex color (#rrggbb or #rgb) or a terminal-256
	// palette index (0-255). When non-empty it replaces the variant's
	// default Accent slot. Sourced from `cfg.Theme.Accent`.
	AccentOverride string
	// Env is a hook for the process env lookup. Defaults to os.Getenv.
	// Tests inject a fake to exercise NO_COLOR / COLORFGBG branches
	// without touching the real environment.
	Env func(string) string
}

// envOf returns the env lookup function, defaulting to os.Getenv when
// nil. Pulled out so callers see the resolution explicitly.
func (o Options) envOf() func(string) string {
	if o.Env != nil {
		return o.Env
	}
	return os.Getenv
}

// Load constructs a Palette from env + options. Pure function; safe to
// call on config reload.
//
// Resolution order:
//  1. If `NO_COLOR` is set (any non-empty value, per no-color.org spec),
//     return a monochrome palette and stop.
//  2. Pick the variant: ForcedVariant ("dark"/"light") wins; else parse
//     `COLORFGBG` (`"FG;BG"` ANSI indices - BG < 8 is dark, BG >= 8 is
//     light); else default to Dark.
//  3. Build the base palette for the chosen variant.
//  4. If AccentOverride is non-empty, replace the Accent slot.
func Load(opts Options) Palette {
	env := opts.envOf()

	// NO_COLOR check - overrides everything. Per https://no-color.org
	// the variable is treated as a boolean: any value (even "0") means
	// "user wants monochrome".
	if env("NO_COLOR") != "" {
		return monochromePalette(detectVariant(opts, env))
	}

	v := detectVariant(opts, env)
	var p Palette
	if v == Light {
		p = lightPalette()
	} else {
		p = darkPalette()
	}
	p.Variant = v

	if opts.AccentOverride != "" {
		if c, ok := parseColor(opts.AccentOverride); ok {
			p.Accent = c
		}
	}
	return p
}

// detectVariant resolves Dark vs Light per the precedence in [Load].
func detectVariant(opts Options, env func(string) string) Variant {
	switch strings.ToLower(strings.TrimSpace(opts.ForcedVariant)) {
	case "light":
		return Light
	case "dark":
		return Dark
	}
	// COLORFGBG format: "FG;BG" where each is an ANSI 0-15 index. Some
	// terminals emit three fields ("FG;_;BG"); the BG is always the last
	// semicolon-separated token. BG indices 0-7 are dark; 8-15 are light.
	if raw := strings.TrimSpace(env("COLORFGBG")); raw != "" {
		parts := strings.Split(raw, ";")
		bg := strings.TrimSpace(parts[len(parts)-1])
		if n, err := strconv.Atoi(bg); err == nil {
			if n >= 8 && n <= 15 {
				return Light
			}
			if n >= 0 && n <= 7 {
				return Dark
			}
		}
	}
	return Dark
}

// ReducedMotion reports whether the user asked TUI surfaces to skip
// animation. The convention mirrors NO_COLOR (handled in [Load] above):
// the `PREFERS_REDUCED_MOTION` environment variable is treated as a
// boolean - any non-empty value (even "0") means "reduce motion".
//
// Consumers today: the chat typewriter reveal on streaming assistant
// text (disabled - text appears as it arrives) and the thinking-row
// dot animation (renders a static variant). New animated surfaces
// should gate on this same helper.
//
// Deliberately independent of NO_COLOR: motion and color are separate
// accessibility axes, and neither implies the other.
//
// env is a lookup hook for tests; nil falls back to os.Getenv.
func ReducedMotion(env func(string) string) bool {
	if env == nil {
		env = os.Getenv
	}
	return env("PREFERS_REDUCED_MOTION") != ""
}

// parseColor accepts "#rrggbb", "#rgb", or a decimal 0-255 ANSI index
// and returns the lipgloss.Color. Returns ok=false on any other input.
//
// We bias toward "permissive but never silently wrong": malformed
// overrides are ignored (the caller keeps the variant default) rather
// than producing a black-on-black surprise.
func parseColor(s string) (lipgloss.Color, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if strings.HasPrefix(s, "#") {
		hex := s[1:]
		if len(hex) != 3 && len(hex) != 6 {
			return "", false
		}
		for _, r := range hex {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return "", false
			}
		}
		return lipgloss.Color(s), true
	}
	// ANSI palette index 0-255.
	if n, err := strconv.Atoi(s); err == nil && n >= 0 && n <= 255 {
		return lipgloss.Color(s), true
	}
	return "", false
}

// darkPalette is the legacy literal set - the same hex values
// chat/manage/onboarding declared inline before centralization. Keeping
// them identical means the visual baseline is unchanged for the (vast)
// majority of users on dark terminals.
func darkPalette() Palette {
	return Palette{
		Accent: lipgloss.Color("#4a6bd6"),
		Muted:  lipgloss.Color("240"),
		User:   lipgloss.Color("#7fb3ff"),
		Agent:  lipgloss.Color("252"),
		Tool:   lipgloss.Color("214"),
		Warn:   lipgloss.Color("203"),
		OK:     lipgloss.Color("34"),
		Subtle: lipgloss.Color("244"),
		Brand:  lipgloss.Color("#1d2a4d"),
		Cyan:   lipgloss.Color("44"),
		ErrHi:  lipgloss.Color("196"),
	}
}

// lightPalette is the inverted-lightness counterpart. Accent darkens
// for legibility against a white background; neutrals lighten so they
// recede instead of dominate.
//
// Hand-tuned, not computed - color theory algorithms produce muddy
// results on a brand-anchored palette this small. Tune by eye.
func lightPalette() Palette {
	return Palette{
		Accent: lipgloss.Color("#2a4cae"),
		Muted:  lipgloss.Color("250"),
		User:   lipgloss.Color("#3a6dc0"),
		Agent:  lipgloss.Color("238"),
		Tool:   lipgloss.Color("#9c5300"),
		Warn:   lipgloss.Color("#b91c1c"),
		OK:     lipgloss.Color("#16a34a"),
		Subtle: lipgloss.Color("247"),
		Brand:  lipgloss.Color("#0f1c3d"),
		Cyan:   lipgloss.Color("#0e7490"),
		ErrHi:  lipgloss.Color("#7f1d1d"),
	}
}

// monochromePalette returns a Palette with every color slot empty.
// lipgloss treats `Color("")` as "no styling" so the rendered output
// is plain text. Bold/Italic still apply - callers that want emphasis
// in NO_COLOR mode toggle those instead of color.
//
// The Variant is preserved so a renderer can still pick a different
// layout for light vs dark even when NO_COLOR is set.
func monochromePalette(v Variant) Palette {
	return Palette{
		Variant: v,
		NoColor: true,
	}
}
