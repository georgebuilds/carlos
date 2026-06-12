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
//	🧢: Here is the answer.
//
//	    • bullet one
//	    • bullet two
//
//	    ┌────────────┐
//	    │ code       │
//	    └────────────┘
//
// The avatar sits inline with the first line of glamour's output,
// matching the `👤: <text>` shape user messages use so a turn-pair
// reads as one conversational beat. Glamour pads each line with 2
// leading spaces (its default left margin); we strip those and
// re-pad continuation rows under the avatar gutter so the body
// aligns under itself instead of double-indenting.
//
// Stripping trailing whitespace per line removes the background-fill
// padding glamour adds, which would otherwise extend rows past the
// visible content and break mouse copy-paste.
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
	body = strings.TrimLeft(body, "\n") // glamour leads with a blank row
	if body == "" {
		return renderAvatarBlockPlain(text, width)
	}
	// OSC 8 hyperlinks (slice 9l) go in AFTER glamour: glamour re-wraps
	// text and splits a pre-injected escape mid-sequence (verified).
	// Post-render the line layout is final, so links never wrap.
	body = linkifyText(body)
	return foldAvatarOntoMarkdown(body)
}

// foldAvatarOntoMarkdown rewrites glamour's body so the first line
// carries the avatar prefix and the continuation rows sit under the
// avatar gutter (4 cells = avatar width + ": ") instead of glamour's
// default 2-space margin.
//
// Margin handling: glamour emits each row as
// `\x1b[…m\x1b[…m  <content>` — the visible 2-space left margin sits
// BETWEEN ANSI escape codes, not at the start of the raw string. A
// naive strings.TrimPrefix(ln, "  ") therefore matches nothing, and
// the prepended pad stacks ON TOP of glamour's untrimmed 2 spaces.
// That used to land the body at column 6 — exactly where the
// bordered tool-card emojis sit (sideMargin 4 + border 1 + padding
// 1) — which is why user reports surfaced "carlos's responses
// align with the tool-call emojis, not with my messages." The
// stripVisibleLeadingMargin helper walks past leading ANSI escapes
// and removes the visible margin in cell terms.
//
// Lines whose visible content doesn't start with a 2-space margin
// (rare; mostly happens inside fenced code blocks that glamour styles
// differently) keep their existing leading whitespace.
func foldAvatarOntoMarkdown(body string) string {
	const avatarPrefix = "🧢: "
	const continuationPad = "    " // 4 cells, same width as avatar+": "

	avatarStyle := lipgloss.NewStyle().Foreground(colorAgent)
	colonStyle := lipgloss.NewStyle().Foreground(colorMuted)
	styledHead := avatarStyle.Render("🧢") + colonStyle.Render(":") + " "

	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	headPlaced := false
	for _, ln := range lines {
		trimmed := stripVisibleLeadingMargin(ln, 2)
		// Avatar lands on the first non-blank row; earlier blank
		// rows (rare after our TrimLeft above, but defensive) stay
		// blank to preserve glamour's vertical rhythm.
		if !headPlaced && strings.TrimSpace(trimmed) == "" {
			out = append(out, "")
			continue
		}
		if !headPlaced {
			out = append(out, styledHead+trimmed)
			headPlaced = true
			continue
		}
		out = append(out, continuationPad+trimmed)
	}
	if !headPlaced {
		// Body was entirely blank after trimming; render just the
		// avatar so the entry isn't silently dropped.
		out = append(out, styledHead)
	}
	_ = avatarPrefix // referenced in the doc comment above for clarity
	return strings.Join(out, "\n")
}

// stripVisibleLeadingMargin removes up to maxCells of visible-leading
// space cells from a glamour-styled line. Walks past any leading
// CSI escape sequences (\x1b[...m) without consuming their bytes,
// then drops the next maxCells space bytes if present. Stops as
// soon as a non-space, non-escape byte is encountered so any
// content beyond the margin is preserved verbatim.
//
// Tab and other whitespace are NOT consumed — glamour's margin is
// always spaces, and we don't want to silently eat indentation
// inside code blocks. Returns the input unchanged when no margin
// is present.
func stripVisibleLeadingMargin(line string, maxCells int) string {
	i := 0
	cells := 0
	for i < len(line) && cells < maxCells {
		switch {
		case line[i] == 0x1b:
			j := i + 1
			if j >= len(line) || line[j] != '[' {
				// Not a CSI escape we know how to skip; bail.
				return line[:i] + line[i:]
			}
			for j < len(line) && line[j] != 'm' {
				j++
			}
			if j >= len(line) {
				// Malformed/truncated escape; bail without trimming.
				return line
			}
			i = j + 1
		case line[i] == ' ':
			// Drop this visible space; advance past it WITHOUT
			// copying.
			cells++
			line = line[:i] + line[i+1:]
		default:
			// First non-space, non-escape byte — margin done.
			return line
		}
	}
	return line
}

// renderAvatarBlockPlain is the fallback used when glamour is
// unavailable or the markdown rendered to nothing. It mirrors the
// pre-glamour avatar layout exactly so a glamour failure degrades
// invisibly — same shape, no fancy styling. Linkified after the wrap
// (slice 9l) so wordWrap's byte-level hard-break for overlong words
// can never split an injected escape.
func renderAvatarBlockPlain(text string, width int) string {
	colon := lipgloss.NewStyle().Foreground(colorMuted).Render(":")
	return linkifyText(renderAvatarBlock("🧢", colon, text, colorAgent, width))
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
// the current viewport. The style name is pinned from carlos's
// boot-time theme variant (see glamourStyle / glamourStyleFor in
// chat.go) so glamour never invokes termenv's WithAutoStyle, which
// fires an OSC 11 background-color query against the terminal. In
// tabbed Ghostty the query response arrived after the alt-screen
// was already up and was read as text input by the textarea ("weird
// characters appear after a long thinking pause" in field reports);
// pinning the style cuts the query off at the source.
//
// WithEmoji enables :+1: shorthand; the word-wrap width leaves 2
// cells of right breathing room so the last column doesn't kiss
// the viewport edge.
//
// Returns (nil, err) on any setup error — callers fall back to
// plain rendering rather than panicking. Cheap enough to cache; we
// rebuild on width change in rerenderViewport.
func newMarkdownRenderer(width int) (*glamour.TermRenderer, error) {
	if width < mdMinWidth {
		width = mdMinWidth
	}
	return glamour.NewTermRenderer(
		glamour.WithStandardStyle(glamourStyle),
		glamour.WithWordWrap(width-2),
		glamour.WithEmoji(),
		glamour.WithPreservedNewLines(),
	)
}
