// Package farewell renders carlos's post-exit panel.
//
// Why this exists: carlos has historically written end-of-session
// notes ("daemon not running", "migrated to per-frame layout") with
// plain fmt.Fprintln to stderr. Because the TUI runs in alt-screen,
// those lines surface only AFTER the alt-screen tears down — which
// means the user sees them at the same time as their shell prompt
// returns. The visual reads as a wall of plaintext warnings rather
// than the kind of polished sign-off carlos earns elsewhere.
//
// This package collects those notes into a deferred Panel and renders
// one rounded-border box on stderr at process exit, styled to match
// the in-session research-status box (RoundedBorder, accent edge,
// per-row emoji prefix, optional dim follow-up line). The result is a
// single, scannable post-session summary — the user gets all the
// state in one place + a friendly "later, <name>".
package farewell

import (
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/theme"
)

// Message is one row in the panel. Emoji is the leading glyph (always
// non-empty; the panel design assumes every row gets one so the column
// alignment is uniform). Text is the headline. Detail (optional) sits
// on the next line in dim color and reads as the follow-up — used for
// "here's why" or "here's how to fix it" elaborations.
type Message struct {
	Emoji  string
	Text   string
	Detail string
}

// Panel collects messages during a carlos session and renders one
// bordered box on demand. Safe for concurrent Add calls — the brew-
// update probe runs on a background goroutine and races the TUI
// shutdown.
type Panel struct {
	mu   sync.Mutex
	msgs []Message
}

// New returns an empty Panel.
func New() *Panel {
	return &Panel{}
}

// Add appends a message with no follow-up detail.
func (p *Panel) Add(emoji, text string) {
	p.AddWithDetail(emoji, text, "")
}

// AddWithDetail appends a message with an optional second-line detail.
// Empty detail means "single line". Both lines share the message's
// emoji column for visual continuity.
func (p *Panel) AddWithDetail(emoji, text, detail string) {
	if text == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs = append(p.msgs, Message{Emoji: emoji, Text: text, Detail: detail})
}

// Len returns the number of queued messages. Useful for callers that
// want to decide whether to render at all (we still no-op an empty
// panel inside Render, but Len lets callers branch earlier).
func (p *Panel) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.msgs)
}

// Messages returns a copy of the queued messages in insertion order.
// Useful for tests; callers in cmd/carlos prefer Render directly.
func (p *Panel) Messages() []Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Message, len(p.msgs))
	copy(out, p.msgs)
	return out
}

// Render returns the bordered panel as a single string ready for
// stderr. Returns "" when no messages are queued — callers can
// `os.Stderr.WriteString(p.Render(...))` unconditionally and a no-op
// panel produces no output.
//
// Width is the desired box width (lipgloss accounts for the border).
// We clamp to a sensible floor so a narrow terminal still renders a
// readable box.
func (p *Panel) Render(width int, pal theme.Palette) string {
	msgs := p.Messages()
	if len(msgs) == 0 {
		return ""
	}

	const minBoxW = 40
	if width < minBoxW {
		width = minBoxW
	}
	boxW := width - 2 // leave a column of breathing room on each side
	if boxW < minBoxW {
		boxW = minBoxW
	}

	textStyle := lipgloss.NewStyle().Foreground(pal.Agent)
	detailStyle := lipgloss.NewStyle().Foreground(pal.Subtle).Italic(true)

	rows := make([]string, 0, len(msgs)*2)
	for _, m := range msgs {
		rows = append(rows, m.Emoji+"  "+textStyle.Render(m.Text))
		if m.Detail != "" {
			// Detail line sits indented under the emoji column so the
			// eye reads it as a continuation of the headline above.
			rows = append(rows, "   "+detailStyle.Render(m.Detail))
		}
	}

	body := strings.Join(rows, "\n")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(pal.Accent).
		Padding(0, 1).
		Width(boxW).
		Render(body)
}
