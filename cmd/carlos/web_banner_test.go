package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/theme"
)

// testWebURL is a realistic launch URL: 29-char prefix + 64 hex token
// chars = 93 printable columns, the exact shape NewToken produces.
const testWebURL = "http://127.0.0.1:7777/#token=" +
	"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// colorPal builds a palette with colors on, immune to the test
// process's real NO_COLOR / COLORFGBG environment.
func colorPal() theme.Palette {
	return theme.Load(theme.Options{Env: func(string) string { return "" }})
}

// noColorPal builds the monochrome palette via the NO_COLOR branch.
func noColorPal() theme.Palette {
	return theme.Load(theme.Options{Env: func(k string) string {
		if k == "NO_COLOR" {
			return "1"
		}
		return ""
	}})
}

// lineContaining returns the single line of out that contains sub,
// failing the test when zero or multiple lines match.
func lineContaining(t *testing.T, out, sub string) string {
	t.Helper()
	var hit string
	n := 0
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, sub) {
			hit = ln
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly 1 line containing %q, got %d in:\n%s", sub, n, out)
	}
	return hit
}

func TestWebBannerPanelWideTTY(t *testing.T) {
	out := webBanner(testWebURL, "interactive", 120, true, colorPal())

	if !strings.Contains(out, "╭") || !strings.Contains(out, "╰") {
		t.Errorf("panel missing rounded border corners:\n%s", out)
	}
	if !strings.Contains(out, "🧢") {
		t.Errorf("panel missing the 🧢 cap mark:\n%s", out)
	}
	if !strings.Contains(out, webBannerHeadline) {
		t.Errorf("panel missing headline %q:\n%s", webBannerHeadline, out)
	}
	if !strings.Contains(out, "interactive") {
		t.Errorf("panel missing mode label:\n%s", out)
	}
	// The URL must survive intact on a single line (copyable in one
	// motion). The OSC 8 wrap puts the URL on that line twice (target
	// + visible text); both copies live on the same line.
	line := lineContaining(t, out, "\x1b]8;;"+testWebURL)
	if !strings.Contains(strings.TrimPrefix(line, "\x1b]8;;"+testWebURL), testWebURL) {
		t.Errorf("URL line missing the visible URL text:\n%q", line)
	}
	if !strings.Contains(out, "the token lives only in this URL") {
		t.Errorf("panel missing token-secrecy note:\n%s", out)
	}
	if !strings.Contains(out, "ctrl-c to stop") {
		t.Errorf("panel missing ctrl-c hint:\n%s", out)
	}
	// Border math holds: every rendered line is exactly boxW+2 = 100
	// columns (120 caps to 100, minus the 2-column margin, plus the
	// two border cells), proving the OSC 8 escapes were zero-width.
	for _, ln := range strings.Split(strings.TrimPrefix(out, "\n"), "\n") {
		if got := lipgloss.Width(ln); got != 100 {
			t.Errorf("line width %d, want 100:\n%q", got, ln)
		}
	}
}

func TestWebBannerPanelReadOnlyMode(t *testing.T) {
	out := webBanner(testWebURL, "read-only", 120, true, colorPal())
	if !strings.Contains(out, "read-only") {
		t.Errorf("panel missing read-only mode label:\n%s", out)
	}
}

func TestWebBannerNoColorPanel(t *testing.T) {
	out := webBanner(testWebURL, "interactive", 120, true, noColorPal())

	if !strings.Contains(out, "╭") {
		t.Errorf("NO_COLOR should keep the bordered panel:\n%s", out)
	}
	if strings.Contains(out, "\x1b]8;") {
		t.Errorf("NO_COLOR output must not contain OSC 8 hyperlinks:\n%q", out)
	}
	if strings.Contains(out, "38;2;") || strings.Contains(out, "38;5;") {
		t.Errorf("NO_COLOR output must not contain color SGR codes:\n%q", out)
	}
	lineContaining(t, out, testWebURL)
}

func TestWebBannerNonTTYPlain(t *testing.T) {
	out := webBanner(testWebURL, "interactive", 120, false, colorPal())
	want := "carlos web - localhost agent console (interactive)\n" +
		"  open: " + testWebURL + "\n" +
		"  the token lives only in this URL fragment; relaunch reprints it. ctrl-c to stop."
	if out != want {
		t.Errorf("non-TTY fallback drifted from the shipped plain form:\ngot:\n%q\nwant:\n%q", out, want)
	}
	// Greppable contract: the URL sits alone on its own line.
	lineContaining(t, out, "open: "+testWebURL)
}

func TestWebBannerNarrowTTYFallsBackToPlain(t *testing.T) {
	// 80 columns cannot hold the 93-column URL inside the box without
	// wrapping it mid-token; copyability wins over the border.
	out := webBanner(testWebURL, "interactive", 80, true, colorPal())
	if strings.Contains(out, "╭") {
		t.Errorf("narrow terminal should fall back to plain output:\n%s", out)
	}
	lineContaining(t, out, "open: "+testWebURL)
}

func TestWebBannerUnknownWidthFallsBackToPlain(t *testing.T) {
	// Width 0 (probe failed) assumes 90, still too narrow for a full
	// token URL: plain output, never a wrapped URL.
	out := webBanner(testWebURL, "interactive", 0, true, colorPal())
	if strings.Contains(out, "╭") {
		t.Errorf("unknown width should fall back to plain output:\n%s", out)
	}
}

func TestWebBannerShortURLNarrowPanel(t *testing.T) {
	// A short URL fits inside the floored 50-column box even on a tiny
	// terminal, so the panel renders (exercises the boxW floor).
	short := "http://127.0.0.1:7777/"
	out := webBanner(short, "interactive", 40, true, colorPal())
	if !strings.Contains(out, "╭") {
		t.Errorf("short URL on a narrow TTY should still get the panel:\n%s", out)
	}
	for _, ln := range strings.Split(strings.TrimPrefix(out, "\n"), "\n") {
		if got := lipgloss.Width(ln); got != 52 {
			t.Errorf("line width %d, want 52 (floored boxW 50 + borders):\n%q", got, ln)
		}
	}
}

func TestWebOSC8Shape(t *testing.T) {
	got := webOSC8("http://x/", "click")
	want := "\x1b]8;;http://x/\x1b\\click\x1b]8;;\x1b\\"
	if got != want {
		t.Errorf("webOSC8 = %q, want %q", got, want)
	}
}

func TestWebBannerPalette(t *testing.T) {
	// nil cfg: falls through to env-driven defaults without panicking.
	_ = webBannerPalette(nil)

	cfg := &config.Config{}
	cfg.Theme.Variant = "light"
	cfg.Theme.Accent = "#112233"
	pal := webBannerPalette(cfg)
	if pal.NoColor {
		// NO_COLOR in the test env would mask the override assertions.
		t.Skip("NO_COLOR set in test environment")
	}
	if pal.Variant != theme.Light {
		t.Errorf("Variant = %v, want Light", pal.Variant)
	}
	if pal.Accent != lipgloss.Color("#112233") {
		t.Errorf("Accent = %q, want #112233", pal.Accent)
	}
}
