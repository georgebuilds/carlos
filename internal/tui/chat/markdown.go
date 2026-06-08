package chat

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// Markdown rendering for assistant messages.
//
// Models almost always reply in markdown — bold, inline code, fenced
// code blocks, bullet lists. Rendering it as raw text shows `**bold**`
// and triple-backtick fences in the transcript, which reads as
// "carlos can't even format its own output". Glamour is the
// charmbracelet-family terminal markdown renderer and slots into the
// existing lipgloss stack without further plumbing.
//
// Scope discipline:
//   - Only entryAssistantMessage flows through glamour. User
//     messages, tool cards, system notes stay plaintext — they don't
//     have markdown to lose.
//   - The streaming buffer (renderAssistantLive) also stays plain.
//     Partial markdown ("**bo" mid-stream) makes glamour flicker
//     between rendered/unrendered styles each tick. The seal-then-
//     render swap on EvtAssistantMessage is the cleaner UX.
//
// Renderer instances are cached on the Model (one per width) because
// glamour.NewTermRenderer parses a style sheet — non-trivial cost on
// every render frame at 30 Hz.

// mdMinWidth is the floor for glamour's word-wrap. Below this the
// output looks claustrophobic; we fall back to plain text rendering
// to spare narrow terminals an ugly layout.
const mdMinWidth = 24

// renderAssistantMarkdown paints an assistant turn through glamour.
// Layout:
//
//	🧢
//	  Here is the answer.
//
//	  • bullet one
//	  • bullet two
//
//	  ┌────────────┐
//	  │ code       │
//	  └────────────┘
//
// The 🧢 avatar sits on its own row at column 0 so glamour owns the
// full body indentation — fighting glamour's 2-space margin produces
// double-indented bullets and code blocks. Stripping trailing
// whitespace per line removes the background-fill padding glamour
// adds, which would otherwise extend rows past the visible content.
//
// Falls back to renderAvatarBlock on glamour errors (which can happen
// with malformed markdown) so a broken markdown reply never vanishes.
func renderAssistantMarkdown(text string, width int, md *glamour.TermRenderer) string {
	if md == nil || width < mdMinWidth {
		return renderAvatarBlockPlain(text, width)
	}
	body, err := md.Render(text)
	if err != nil || strings.TrimSpace(body) == "" {
		return renderAvatarBlockPlain(text, width)
	}
	body = trimPerLineRight(body)
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return renderAvatarBlockPlain(text, width)
	}
	avatar := lipgloss.NewStyle().Foreground(colorAgent).Render("🧢")
	return avatar + "\n" + body
}

// renderAvatarBlockPlain is the fallback used when glamour is
// unavailable or the markdown rendered to nothing. It mirrors the
// pre-glamour avatar layout exactly so a glamour failure degrades
// invisibly — same shape, no fancy styling.
func renderAvatarBlockPlain(text string, width int) string {
	colon := lipgloss.NewStyle().Foreground(colorMuted).Render(":")
	return renderAvatarBlock("🧢", colon, text, colorAgent, width)
}

// trimPerLineRight removes trailing spaces/tabs from each line
// of s. Glamour pads its output with background spaces so a wrapped
// row's "fill color" reaches the right margin; in our viewport that
// just produces giant rows of whitespace that mess with mouse
// selection + copy-paste.
func trimPerLineRight(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	return strings.Join(lines, "\n")
}

// newMarkdownRenderer constructs a glamour TermRenderer sized for
// the current viewport. WithAutoStyle picks dark/light/notty based
// on terminal capability; WithEmoji enables :+1: shorthand; the
// word-wrap width leaves 2 cells of right breathing room so the
// last column doesn't kiss the viewport edge.
//
// Returns (nil, err) on any setup error — callers fall back to
// plain rendering rather than panicking. Cheap enough to cache; we
// rebuild on width change in rerenderViewport.
func newMarkdownRenderer(width int) (*glamour.TermRenderer, error) {
	if width < mdMinWidth {
		width = mdMinWidth
	}
	return glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width-2),
		glamour.WithEmoji(),
		glamour.WithPreservedNewLines(),
	)
}
