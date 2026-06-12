package chat

import (
	"os"
	"strings"
)

// OSC 8 hyperlink injection for the transcript (slice 9l).
//
// The shipped precedent is annotatePaths in usershell_render.go, which
// wraps Go-style file:line tokens in shell output. This file
// generalizes that idea to the conversational transcript: absolute
// file paths, ~/-prefixed paths, and http(s) URLs in sealed user /
// assistant turns become clickable in OSC 8-capable terminals (Kitty,
// iTerm2, Ghostty, WezTerm, Windows Terminal, VS Code, tmux >= 3.4).
// Unsupporting terminals show the visible text unchanged, so emission
// is unconditional - same call as annotatePaths made.
//
// Injection discipline:
//
//   - Assistant markdown is linkified AFTER glamour renders (glamour
//     re-wraps text and would split a pre-injected escape mid-sequence,
//     mangling it - verified empirically).
//   - The live streaming buffer is never linkified; only sealed turns
//     get links. Partial paths mid-stream would produce broken targets
//     and re-scanning per 33ms tick is wasted work.
//   - Post-glamour text is full of SGR escapes, so matching runs on a
//     scanner that walks escape sequences instead of a bare regex: we
//     never match inside an escape, and text already inside an OSC 8
//     link is copied verbatim (idempotence).

// osc8 wraps text in an OSC 8 hyperlink pointing at url. Sequence
// shape: ESC]8;;<url>ESC\<text>ESC]8;;ESC\. Zero-width under
// lipgloss.Width, so wrapped tokens don't disturb layout math.
func osc8(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// userHomeDir is an indirection over os.UserHomeDir so tests can pin
// the expansion of ~/-prefixed paths without touching the real env.
var userHomeDir = os.UserHomeDir

// fileURL builds the OSC 8 target for a detected path token.
//
//   - absolute paths get the canonical file://<path> form;
//   - ~/-paths expand against the user's home dir (falling back to the
//     literal when the home dir is unresolvable - the link degrades to
//     a no-op in most terminals, the text is untouched either way);
//   - relative paths keep annotatePaths' file://./<path> convention,
//     which most terminal click-handlers resolve against the cwd.
func fileURL(path string) string {
	switch {
	case strings.HasPrefix(path, "~/"):
		if home, err := userHomeDir(); err == nil && home != "" {
			return "file://" + home + path[1:]
		}
		return "file://" + path
	case strings.HasPrefix(path, "/"):
		return "file://" + path
	default:
		return "file://./" + path
	}
}
