package frame

import (
	"strings"
	"testing"
)

func TestAccentColor_KnownPalette(t *testing.T) {
	for _, name := range AccentPalette {
		if got := AccentColor(name); got == "" {
			t.Errorf("AccentColor(%q) returned empty; every palette entry must have a color", name)
		}
	}
}

func TestAccentColor_UnknownReturnsEmpty(t *testing.T) {
	cases := []string{"", "magenta", "RED", "Rust", "neon"}
	for _, c := range cases {
		if got := AccentColor(c); got != "" {
			t.Errorf("AccentColor(%q) = %q, want empty (caller fallback)", c, got)
		}
	}
}

func TestPill_NoColorFallsBackToGlyphPlusName(t *testing.T) {
	out := Pill("◉", "personal", "cream", true)
	if !strings.Contains(out, "◉") || !strings.Contains(out, "personal") {
		t.Errorf("Pill(noColor=true) missing glyph or name: %q", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("Pill(noColor=true) emitted ANSI escapes: %q", out)
	}
}

func TestPill_EmptyGlyphFallsBackToDefault(t *testing.T) {
	out := Pill("", "work", "rust", true)
	if !strings.Contains(out, DefaultGlyphFor("work")) {
		t.Errorf("Pill empty-glyph should default; got %q", out)
	}
}

func TestPill_UnknownAccentSkipsColorButKeepsLayout(t *testing.T) {
	out := Pill("◉", "personal", "neon-pink", false)
	if !strings.Contains(out, "personal") {
		t.Errorf("Pill missing name: %q", out)
	}
	if !strings.Contains(out, "◉") {
		t.Errorf("Pill missing glyph: %q", out)
	}
}
