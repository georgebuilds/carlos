package chat

import (
	"os"
	"regexp"
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

// linkifyToken matches one linkable token preceded by a boundary (start
// of text or a non-path, non-word rune - so "foo/bar/baz" never links
// its "/bar/baz" suffix and URLs don't re-match their path component).
// Group 1 is the token. Three alternatives, in match-priority order:
//
//	http(s) URLs       https://host/whatever
//	~/-prefixed paths  ~/notes.md, ~/a/b (one or more segments)
//	absolute paths     /Users/x/foo.go:42 (two or more segments - a
//	                   single segment like "/help" is far more likely
//	                   a slash command than a root-level file)
//
// Paths accept an optional :line[:col] suffix; the suffix stays in the
// display text but is stripped from the URL (matching annotatePaths).
// False positives are worse than missed positives, hence conservative
// segment classes (no spaces, no shell metacharacters).
var linkifyToken = regexp.MustCompile(
	`(?:^|[^\w@%+.~/\p{L}\p{N}-])(` +
		`https?://[^\s\x07\x1b"'<>\)\]]+` +
		"|~/[\\w.@%+\\p{L}\\p{N}-]+(?:/[\\w.@%+\\p{L}\\p{N}-]+)*(?::\\d+(?::\\d+)?)?" +
		"|/[\\w.@%+\\p{L}\\p{N}-]+(?:/[\\w.@%+\\p{L}\\p{N}-]+)+(?::\\d+(?::\\d+)?)?" +
		`)`)

// linkifyText injects OSC 8 hyperlinks into rendered transcript text.
// The input may contain ANSI escapes (SGR styling from glamour /
// lipgloss, or pre-existing OSC 8 links); the scanner walks them so
// the matcher only ever sees plain visible runs. Three guarantees:
//
//  1. no match inside an escape sequence (an OSC title or SGR params
//     containing a path-shaped string stays untouched);
//  2. text inside an existing OSC 8 link is copied verbatim, so
//     linkifyText(linkifyText(s)) == linkifyText(s);
//  3. injected sequences sit between existing SGR escapes, never
//     splitting one, so styling and links nest cleanly.
func linkifyText(s string) string {
	// Fast path: every linkable token contains a '/'; most transcript
	// lines don't.
	if !strings.Contains(s, "/") {
		return s
	}
	var b strings.Builder
	i, plain := 0, 0
	for i < len(s) {
		if s[i] != 0x1b {
			i++
			continue
		}
		b.WriteString(linkifyPlain(s[plain:i]))
		end := escapeEnd(s, i)
		if isLinkOpen(s[i:end]) {
			// Existing hyperlink: copy verbatim through its close so
			// already-linked text is never wrapped twice.
			end = linkCloseEnd(s, end)
		}
		b.WriteString(s[i:end])
		i = end
		plain = i
	}
	b.WriteString(linkifyPlain(s[plain:]))
	return b.String()
}

// linkifyPlain wraps linkable tokens in an escape-free run of text.
// Trailing sentence punctuation is peeled off the token before
// wrapping ("see /a/b.go." links /a/b.go, the period stays outside).
func linkifyPlain(t string) string {
	ms := linkifyToken.FindAllStringSubmatchIndex(t, -1)
	if len(ms) == 0 {
		return t
	}
	var b strings.Builder
	last := 0
	for _, m := range ms {
		start, end := m[2], m[3] // group 1: the token
		tok, trail := trimTrailingPunct(t[start:end])
		b.WriteString(t[last:start])
		if tok != "" {
			b.WriteString(linkToken(tok))
		}
		b.WriteString(trail)
		last = end
	}
	b.WriteString(t[last:])
	return b.String()
}

// linkToken wraps one trimmed token in its OSC 8 hyperlink. URLs link
// to themselves; paths link to file:// targets with any :line[:col]
// suffix stripped from the URL but kept in the display text (the
// annotatePaths convention - terminals open the bare file).
func linkToken(tok string) string {
	if strings.HasPrefix(tok, "http://") || strings.HasPrefix(tok, "https://") {
		return osc8(tok, tok)
	}
	core := tok
	if i := strings.Index(tok, ":"); i > 0 && isNumericTail(tok[i+1:]) {
		core = tok[:i]
	}
	return osc8(fileURL(core), tok)
}

// trimTrailingPunct splits sentence punctuation off the end of a
// matched token. ".,;:!?" are path/URL-legal characters that far more
// often belong to the surrounding prose ("see /a/b.go.") than to the
// target. A :line suffix is safe - it ends in a digit.
func trimTrailingPunct(tok string) (head, tail string) {
	end := len(tok)
	for end > 0 {
		switch tok[end-1] {
		case '.', ',', ';', ':', '!', '?':
			end--
		default:
			return tok[:end], tok[end:]
		}
	}
	return "", tok
}

// escapeEnd returns the index just past the ANSI escape sequence
// starting at s[i] (s[i] must be ESC). Handles CSI (ESC [ ... final
// byte 0x40-0x7e), OSC (ESC ] ... BEL or ST), and treats anything
// else as a two-byte escape (covers ST itself, charset selection).
// A truncated sequence consumes the rest of the string - never panics,
// never matches inside it.
func escapeEnd(s string, i int) int {
	j := i + 1
	if j >= len(s) {
		return len(s)
	}
	switch s[j] {
	case '[': // CSI
		for j++; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
		}
		return len(s)
	case ']': // OSC
		for j++; j < len(s); j++ {
			if s[j] == 0x07 { // BEL terminator
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' { // ST
				return j + 2
			}
		}
		return len(s)
	default:
		return j + 1
	}
}

// isLinkOpen reports whether seq (one full escape sequence as carved
// out by escapeEnd) is an OSC 8 hyperlink OPEN - i.e. carries a
// non-empty URI. The close sequence has an empty URI ("ESC]8;;ST").
func isLinkOpen(seq string) bool {
	if !strings.HasPrefix(seq, "\x1b]8;") {
		return false
	}
	body := strings.TrimSuffix(seq[4:], "\x1b\\")
	body = strings.TrimSuffix(body, "\x07")
	i := strings.Index(body, ";") // params;uri
	return i >= 0 && body[i+1:] != ""
}

// linkCloseEnd scans forward from `from` for the OSC 8 close that
// terminates an open hyperlink and returns the index just past it.
// An unterminated link (malformed input) consumes the rest of the
// string - copied verbatim, never re-matched.
func linkCloseEnd(s string, from int) int {
	for i := from; i < len(s); {
		if s[i] != 0x1b {
			i++
			continue
		}
		end := escapeEnd(s, i)
		seq := s[i:end]
		if strings.HasPrefix(seq, "\x1b]8;") && !isLinkOpen(seq) {
			return end
		}
		i = end
	}
	return len(s)
}
