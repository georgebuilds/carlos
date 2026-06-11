// Package termscrub strips terminal escape-sequence remnants that leak
// into TUI text-input fields.
//
// Background: when the terminal flushes a buffered control sequence at the
// wrong moment (a mouse-mode transition, an alt-screen teardown, a fast
// trackpad gesture), the bytes can land in a focused bubbles textarea as if
// the user had typed them. The textarea's own sanitizer strips the leading
// ESC (0x1b) and other control runes at insertion time, so what actually
// reaches the buffer is only the PRINTABLE TAIL of the sequence. For example
// an SGR mouse report "\x1b[<64;96;7M" shows up as just "[<64;96;7M".
//
// Every pattern below therefore tolerates an OPTIONAL leading ESC: the leak
// can arrive with the ESC intact (when it reaches us before the textarea
// sanitizes it) or already stripped down to its tail.
package termscrub

import (
	"regexp"

	tea "github.com/charmbracelet/bubbletea"
)

// leakRE matches the printable form (with or without a leading ESC) of every
// terminal reply / report sequence known to leak into the composer. It is a
// single alternation compiled once so Scrub stays a single-pass regexp scan;
// each alternative is strict on its numeric/structural segments so we do not
// over-match legitimately typed brackets.
//
// Alternatives, in order:
//
//	(?:\x1b)?\[<\d+;\d+;\d+[Mm]          SGR mouse report ("<button;col;row" + M/m)
//	(?:\x1b)?\[M[\x20-\x7f]{3}           X11 mouse report (CSI M + 3 printable bytes)
//	(?:\x1b)?\[\d+;\d+R                  DSR cursor-position reply ("row;colR")
//	(?:\x1b)?\[\?[\d;]+c                 Device-attributes reply ("?...c")
//	(?:\x1b)?\]1[01];[^\a\x1b]*(?:\a|\x1b\\)?  OSC 10/11 color reply (BEL- or ST-terminated)
//	(?:\x1b)?\[20[01]~                   Bracketed-paste markers ("200~" / "201~")
//
// NOTE: we deliberately do NOT scrub bare focus-event sequences ("[I" / "[O").
// They are only two chars and collide with legitimately typed text like
// "array[I]"; carlos never enables focus-report mode (1004), so they cannot
// occur. Do not "helpfully" add them here.
var leakRE = regexp.MustCompile(
	`(?:\x1b)?\[<\d+;\d+;\d+[Mm]` +
		`|(?:\x1b)?\[M[\x20-\x7f]{3}` +
		`|(?:\x1b)?\[\d+;\d+R` +
		`|(?:\x1b)?\[\?[\d;]+c` +
		`|(?:\x1b)?\]1[01];[^\a\x1b]*(?:\a|\x1b\\)?` +
		`|(?:\x1b)?\[20[01]~`,
)

// Scrub removes every terminal escape-sequence remnant from s in a single
// pass. It is idempotent and safe to call on every keystroke: when no leak is
// present the regexp's scan is a no-op over the input and the original string
// is returned unchanged.
func Scrub(s string) string {
	return leakRE.ReplaceAllString(s, "")
}

// FilterTerminalLeaks is a tea.WithFilter callback that intercepts leaked
// terminal sequences before they reach the model.
//
// Gating rationale: real keyboard input arrives as one rune per KeyMsg, so a
// non-paste KeyRunes message carrying more than one rune is the signature of a
// buffered escape sequence being delivered as a single burst. Pastes
// (m.Paste == true) must pass untouched so users can paste text that legitimately
// contains brackets and other characters our patterns key on.
func FilterTerminalLeaks(_ tea.Model, msg tea.Msg) tea.Msg {
	if m, ok := msg.(tea.KeyMsg); ok && m.Type == tea.KeyRunes && !m.Paste && len(m.Runes) > 1 {
		cleaned := Scrub(string(m.Runes))
		switch {
		case cleaned == "":
			// The whole burst was a leak; drop it entirely.
			return nil
		case cleaned != string(m.Runes):
			// Mixed real text + leak; forward only the real text.
			return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(cleaned)}
		default:
			return msg
		}
	}
	return msg
}
