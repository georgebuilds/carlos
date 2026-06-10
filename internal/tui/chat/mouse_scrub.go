package chat

import "regexp"

// SGR mouse-report sequence: "<button;col;row[Mm]" optionally
// preceded by the literal "\x1b[" or just "[" — both forms have
// been observed leaking into the textarea when the terminal
// flushes a buffered mouse event during a bubbletea mouse-mode
// transition (the alt+m toggle path is the most common trigger,
// but a fast trackpad gesture during alt-screen teardown can do
// it too). The visible form ends with capital M for press / m
// for release.
//
// Pattern shape:
//
//	(?:\x1b)?\[<<digits>;<digits>;<digits>[Mm]
//
// Strict on numeric segments so we don't over-match user-typed
// brackets — the legitimate composer can contain "[" + arbitrary
// text, but real SGR reports always carry three semicolon-
// separated numeric coordinates.
var sgrMouseReportRE = regexp.MustCompile(`(?:\x1b)?\[<\d+;\d+;\d+[Mm]`)

// scrubMouseReportEscapes removes any SGR mouse-report sequences
// from s. Called after every textarea update so the user never
// sees those leaks as text they have to backspace out by hand.
// Idempotent and safe to call on every keystroke — the regex's
// FindAll path is a no-op when no leak is present, so the cost
// on the common path is one ASCII scan.
func scrubMouseReportEscapes(s string) string {
	return sgrMouseReportRE.ReplaceAllString(s, "")
}
