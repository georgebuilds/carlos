package chat

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestWordWrap_BasicAndEmptyLines(t *testing.T) {
	lines := wordWrap("one two three", 100)
	if len(lines) != 1 || lines[0] != "one two three" {
		t.Errorf("short text should stay one line; got %v", lines)
	}
	// Explicit newlines start fresh paragraphs (including blank ones).
	lines = wordWrap("a\n\nb", 100)
	if len(lines) != 3 || lines[1] != "" {
		t.Errorf("blank paragraph should survive; got %v", lines)
	}
}

func TestWordWrap_WrapsAtWidth(t *testing.T) {
	lines := wordWrap("alpha beta gamma delta epsilon", 12)
	if len(lines) < 2 {
		t.Fatalf("text should wrap into multiple lines; got %v", lines)
	}
	for _, ln := range lines {
		if lipgloss.Width(ln) > 12 {
			t.Errorf("line exceeds width 12: %q (%d)", ln, lipgloss.Width(ln))
		}
	}
}

func TestWordWrap_HardBreaksLongWord(t *testing.T) {
	// A single word longer than the width is hard-broken.
	long := "supercalifragilisticexpialidocious"
	lines := wordWrap(long, 10)
	if len(lines) < 2 {
		t.Fatalf("over-width word should hard-break; got %v", lines)
	}
	for _, ln := range lines {
		if lipgloss.Width(ln) > 10 {
			t.Errorf("hard-break line exceeds width: %q", ln)
		}
	}
}

func TestWordWrap_LongWordMidLineFlushesFirst(t *testing.T) {
	// A normal word then an over-width word: the first line flushes,
	// then the long word hard-breaks (covers the default-case break).
	lines := wordWrap("hi superlongwordthatwontfit", 8)
	if len(lines) < 2 {
		t.Fatalf("expected flush + hard-break; got %v", lines)
	}
	if lines[0] != "hi" {
		t.Errorf("first line should flush 'hi'; got %q", lines[0])
	}
}

func TestWordWrap_ZeroWidthFloorsToOne(t *testing.T) {
	lines := wordWrap("ab", 0)
	// width floors to 1, so "ab" hard-breaks to two single-char rows.
	if len(lines) != 2 {
		t.Errorf("width 0 should floor to 1 and split; got %v", lines)
	}
}
