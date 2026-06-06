// Package onboarding owns the six-screen first-run flow and the portrait
// protocol cascade. Public surface (this file): RenderProtocol and the
// DetectProtocol + Render entrypoints. Per-protocol renderers live in
// portrait_kitty.go, portrait_iterm2.go, portrait_halfblock.go, portrait_ascii.go.
//
// The cascade is intentional: a terminal that supports the highest-fidelity
// protocol takes that path; everything else falls through to lower-fidelity
// renderers. The face MUST always appear at some fidelity — choosing ASCII
// without first trying the cascade is a bug.
package onboarding

import (
	"bytes"
	_ "embed"
	"image"
	"image/png"
	"os"
	"sync"
)

// Embedded source assets. Kept private; renderers consume the decoded image
// or the raw PNG bytes as needed.

// The portrait assets live at the repo's branding root and are embedded via
// the package-local assets directory (symlinks aren't allowed by go:embed,
// so we keep duplicates synchronized — see assets/README.md). Keeping the
// embed paths package-local avoids the relative-path foot-gun where the
// embed directive resolves against the package dir, not the module root.

//go:embed assets/carlos-portrait.png
var portraitPNG []byte

// portraitSmallPNG is a hand-tuned-for-small-render variant of the mascot.
// Today it ships as a byte-identical placeholder copy of portraitPNG so
// the build is happy. When the user drops a regenerated low-res variant
// at assets/carlos-portrait-small.png (e.g. 128×128 with simplified
// detail and higher edge contrast for half-block sampling), the rail's
// half-block fallback picks it up automatically — see RenderRailCells.
//
//go:embed assets/carlos-portrait-small.png
var portraitSmallPNG []byte

//go:embed assets/carlos-portrait-ascii.txt
var portraitASCII string

// RenderProtocol enumerates the terminal image cascade from highest to
// lowest fidelity. Order matters — DetectProtocol returns the first match.
type RenderProtocol int

const (
	ProtoKitty RenderProtocol = iota
	ProtoITerm2
	ProtoWezTerm
	ProtoSixel
	ProtoUnicodeHalfBlock
	ProtoASCII
)

// String returns a stable lower-case identifier for the protocol — useful
// for logging, telemetry, and the --print-protocol debug hook.
func (p RenderProtocol) String() string {
	switch p {
	case ProtoKitty:
		return "kitty"
	case ProtoITerm2:
		return "iterm2"
	case ProtoWezTerm:
		return "wezterm"
	case ProtoSixel:
		return "sixel"
	case ProtoUnicodeHalfBlock:
		return "unicode-half-block"
	case ProtoASCII:
		return "ascii"
	}
	return "unknown"
}

// DetectProtocol picks the highest-fidelity protocol the current terminal
// claims to support, based on environment variables. Detection is a one-shot
// at TUI startup (cached via DetectProtocolCached) — re-running per-frame
// would re-stat env vars unnecessarily.
//
// Env-var detection is best-effort: it matches what the terminal advertises,
// not what it actually does. The cascade tolerates this by falling through:
// if a Kitty-emitting client renders garbage in some odd remote-pty setup,
// the user can force a lower fidelity via CARLOS_PORTRAIT_PROTOCOL.
func DetectProtocol() RenderProtocol {
	if forced := os.Getenv("CARLOS_PORTRAIT_PROTOCOL"); forced != "" {
		switch forced {
		case "kitty":
			return ProtoKitty
		case "iterm2":
			return ProtoITerm2
		case "wezterm":
			return ProtoWezTerm
		case "sixel":
			return ProtoSixel
		case "half-block", "unicode-half-block", "halfblock":
			return ProtoUnicodeHalfBlock
		case "ascii":
			return ProtoASCII
		}
	}

	// Kitty: env var is the canonical signal; TERM=xterm-kitty is the
	// fallback when the env var got stripped by a screen multiplexer.
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("TERM") == "xterm-kitty" {
		return ProtoKitty
	}
	// Ghostty implements the Kitty graphics protocol natively. It
	// advertises itself via $TERM_PROGRAM=ghostty (and sets
	// $GHOSTTY_RESOURCES_DIR + $TERM=xterm-ghostty). Without this
	// branch Ghostty falls through to the half-block sampler and the
	// portrait reads as pixelated cells instead of a crisp PNG.
	if os.Getenv("TERM_PROGRAM") == "ghostty" ||
		os.Getenv("TERM") == "xterm-ghostty" ||
		os.Getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return ProtoKitty
	}
	// WezTerm advertises both Kitty and iTerm graphics; Render() routes
	// ProtoWezTerm through the Kitty path because it preserves alpha
	// best. Keeping the distinct return value lets telemetry attribute
	// successes/failures correctly.
	if os.Getenv("WEZTERM_PANE") != "" {
		return ProtoWezTerm
	}
	// iTerm2: $TERM_PROGRAM is canonical and reliable.
	if os.Getenv("TERM_PROGRAM") == "iTerm.app" {
		return ProtoITerm2
	}

	// Sixel detection without DA1 probing is heuristic. We match a small
	// allow-list of TERMs known to advertise sixel out of the box. DA1
	// probing in a bubbletea pre-render path is hairy (raw mode, timing,
	// cooperation with the tea program loop) — we punt to Phase 8 when
	// the daemon owns the terminal lifecycle. See notes file.
	term := os.Getenv("TERM")
	switch term {
	case "mlterm", "foot", "foot-extra", "contour", "yaft-256color":
		return ProtoSixel
	}

	// Truecolor-capable: half-block. The COLORTERM env var is the de-facto
	// standard advertisement for 24-bit color.
	ct := os.Getenv("COLORTERM")
	if ct == "truecolor" || ct == "24bit" {
		return ProtoUnicodeHalfBlock
	}
	// Most modern terminals (Apple Terminal, gnome-terminal-256, xterm-256)
	// still render half-block acceptably in 256-color mode. Halfblock
	// renderer auto-degrades.
	if term != "" && term != "dumb" {
		return ProtoUnicodeHalfBlock
	}
	return ProtoASCII
}

var (
	detectOnce   sync.Once
	detectCached RenderProtocol
)

// DetectProtocolCached returns the cached protocol from the first call to
// DetectProtocol. Use this from per-frame view code so we don't re-read env
// on every redraw.
func DetectProtocolCached() RenderProtocol {
	detectOnce.Do(func() { detectCached = DetectProtocol() })
	return detectCached
}

// Render produces the portrait as a string ready to embed in a bubbletea
// view. The returned string includes the terminal escape sequences for
// image protocols (Kitty/iTerm2) so the bubbletea renderer must pass it
// through verbatim — lipgloss padding must not surround it.
//
// Cascade behavior: if the requested protocol fails (e.g. PNG decode error,
// terminal rejects the escape), the function falls through to ASCII rather
// than returning an error to the caller. The face must always appear.
func Render(p RenderProtocol) (string, error) {
	switch p {
	case ProtoKitty, ProtoWezTerm:
		if s, err := renderKitty(portraitPNG); err == nil {
			return s, nil
		}
		fallthrough
	case ProtoITerm2:
		if s, err := renderITerm2(portraitPNG); err == nil {
			return s, nil
		}
		fallthrough
	case ProtoSixel:
		// Sixel renderer is intentionally stubbed; see DetectProtocol
		// comment and the notes file. We fall through to half-block,
		// which is the correct degraded behavior.
		fallthrough
	case ProtoUnicodeHalfBlock:
		img, err := decodePortrait()
		if err == nil {
			// Default size: 36×18 cells. cols = 2*rows so a
			// square source displays as a visual square in a
			// terminal where cells are ~1:2 (W:H).
			return renderHalfBlock(img, 36, 18), nil
		}
		fallthrough
	case ProtoASCII:
		return portraitASCII, nil
	}
	return portraitASCII, nil
}

// RenderRail produces a portrait sized to fit a left-rail cell box of
// (cols × rows) using the half-block protocol — universal fallback.
//
// Most callers want RenderRailCells, which picks the best available
// protocol and falls back here. This helper remains for tests and for
// places that explicitly want the half-block path.
//
// Aspect: for a square source image, cols should be 2×rows. We clamp here
// in case the layout passes something off-aspect.
func RenderRail(cols, rows int) string {
	if cols < 4 {
		cols = 4
	}
	if rows < 2 {
		rows = 2
	}
	img, err := decodePortrait()
	if err != nil {
		return portraitASCII
	}
	return renderHalfBlock(img, cols, rows)
}

// RenderRailCells is the cell-box-aware rail renderer used by onboarding.
// It dispatches on the detected terminal protocol:
//
//   - Kitty (or WezTerm) → renderKittyCells: pixel-perfect at any size,
//     with C=1 so the cursor doesn't advance and `rows × cols` spaces
//     beneath compose properly with lipgloss layout.
//   - iTerm2 → renderITerm2Cells: pixel-perfect at any size; layout
//     reservation accounts for iTerm2 advancing the cursor.
//   - Anything else → half-block. Prefers a hand-tuned small variant
//     (assets/carlos-portrait-small.png) if its bytes differ from the
//     main portrait; otherwise samples the main portrait.
//
// Failure of an image-protocol renderer falls through to half-block; the
// face always appears at some fidelity.
//
// Returns the rendered string and the protocol actually used (useful for
// debug logging via the --print-protocol hook, even though onboarding
// itself doesn't surface it).
func RenderRailCells(cols, rows int) (string, RenderProtocol) {
	if cols < 4 {
		cols = 4
	}
	if rows < 2 {
		rows = 2
	}
	p := DetectProtocolCached()
	switch p {
	case ProtoKitty, ProtoWezTerm:
		if s, err := renderKittyCells(portraitPNG, cols, rows); err == nil {
			return s, ProtoKitty
		}
	case ProtoITerm2:
		if s, err := renderITerm2Cells(portraitPNG, cols, rows); err == nil {
			return s, ProtoITerm2
		}
	}
	// Fallback: half-block. Prefer the hand-tuned small variant if the
	// user has dropped one in (the default placeholder is a copy of the
	// main portrait; replacing it is a no-code change).
	img, err := decodePortraitForRail()
	if err != nil {
		return portraitASCII, ProtoASCII
	}
	return renderHalfBlock(img, cols, rows), ProtoUnicodeHalfBlock
}

// decodePortraitForRail returns the small-variant image if the embedded
// bytes differ from the main portrait (i.e., the user has replaced the
// placeholder with a hand-tuned variant); otherwise it returns the main
// portrait. Cached.
//
// The "differ from main" check uses byte comparison rather than a config
// flag because go:embed cannot conditionally include files. Shipping a
// placeholder identical to the main means: today, half-block samples
// effectively the same source; tomorrow when the user drops a hand-tuned
// 128×128 variant into assets/carlos-portrait-small.png, half-block picks
// it up automatically.
func decodePortraitForRail() (image.Image, error) {
	if portraitHasSmall() {
		return decodePortraitSmall()
	}
	return decodePortrait()
}

// decodePortrait decodes the embedded main PNG. Cached so multiple
// renders don't re-decode.
var (
	decodeOnce sync.Once
	decodeImg  image.Image
	decodeErr  error

	decodeSmallOnce sync.Once
	decodeSmallImg  image.Image
	decodeSmallErr  error
)

func decodePortrait() (image.Image, error) {
	decodeOnce.Do(func() {
		decodeImg, decodeErr = png.Decode(bytes.NewReader(portraitPNG))
	})
	return decodeImg, decodeErr
}

func decodePortraitSmall() (image.Image, error) {
	decodeSmallOnce.Do(func() {
		decodeSmallImg, decodeSmallErr = png.Decode(bytes.NewReader(portraitSmallPNG))
	})
	return decodeSmallImg, decodeSmallErr
}

// portraitHasSmall reports whether the small variant is a real
// hand-tuned asset (bytes differ from the main portrait). False when
// it's the placeholder copy.
func portraitHasSmall() bool {
	if len(portraitSmallPNG) == 0 || len(portraitSmallPNG) == len(portraitPNG) {
		// Quick reject: empty, or same size as main (likely the
		// byte-identical placeholder).
		if len(portraitSmallPNG) == len(portraitPNG) {
			return !bytesEqual(portraitSmallPNG, portraitPNG)
		}
		return len(portraitSmallPNG) > 0
	}
	return true
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
