package onboarding

import (
	"strings"
	"testing"
)

// TestRenderAllProtocolsProduceOutput is the cascade-safety net: every
// protocol must produce SOMETHING. Failing this means the welcome screen
// can ship with no face, which violates the SPEC.
func TestRenderAllProtocolsProduceOutput(t *testing.T) {
	protos := []RenderProtocol{
		ProtoKitty, ProtoITerm2, ProtoWezTerm,
		ProtoSixel, ProtoUnicodeHalfBlock, ProtoASCII,
	}
	for _, p := range protos {
		t.Run(p.String(), func(t *testing.T) {
			s, err := Render(p)
			if err != nil {
				t.Fatalf("Render(%s): %v", p, err)
			}
			if s == "" {
				t.Errorf("Render(%s): empty output", p)
			}
		})
	}
}

// TestASCIIFallbackEmbedded sanity-checks the embed actually loaded the
// pre-baked ASCII art. If this is empty, the binary will run but a
// fully-degraded terminal will see a blank welcome screen.
func TestASCIIFallbackEmbedded(t *testing.T) {
	if portraitASCII == "" {
		t.Fatal("portraitASCII embed is empty — assets/carlos-portrait-ascii.txt missing or empty")
	}
	lines := strings.Split(strings.TrimRight(portraitASCII, "\n"), "\n")
	if len(lines) < 15 || len(lines) > 30 {
		t.Errorf("ascii portrait: expected ~22 lines, got %d", len(lines))
	}
}

// TestPNGEmbedded mirrors the above for the PNG.
func TestPNGEmbedded(t *testing.T) {
	if len(portraitPNG) < 1000 {
		t.Fatalf("portraitPNG embed is too small: %d bytes", len(portraitPNG))
	}
	// PNG signature: 89 50 4E 47.
	if portraitPNG[0] != 0x89 || string(portraitPNG[1:4]) != "PNG" {
		t.Errorf("portraitPNG doesn't start with PNG signature")
	}
}

// TestDetectProtocolOverride verifies the env-var escape hatch users
// (and tests) can use to force a specific protocol.
func TestDetectProtocolOverride(t *testing.T) {
	t.Setenv("CARLOS_PORTRAIT_PROTOCOL", "ascii")
	if got := DetectProtocol(); got != ProtoASCII {
		t.Errorf("forced ascii: got %s", got)
	}
	t.Setenv("CARLOS_PORTRAIT_PROTOCOL", "kitty")
	if got := DetectProtocol(); got != ProtoKitty {
		t.Errorf("forced kitty: got %s", got)
	}
}

// TestDetectProtocol_Ghostty pins the env-var detection that routes
// Ghostty through the Kitty graphics protocol. Without this, Ghostty
// falls through to the half-block sampler and the portrait looks
// pixelated even though the terminal can render PNGs natively.
func TestDetectProtocol_Ghostty(t *testing.T) {
	t.Setenv("CARLOS_PORTRAIT_PROTOCOL", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("WEZTERM_PANE", "")

	// Each Ghostty env signal alone should be enough.
	for _, kv := range []struct{ key, val string }{
		{"TERM_PROGRAM", "ghostty"},
		{"TERM", "xterm-ghostty"},
		{"GHOSTTY_RESOURCES_DIR", "/Applications/Ghostty.app/Contents/Resources"},
	} {
		t.Run(kv.key, func(t *testing.T) {
			t.Setenv("TERM_PROGRAM", "")
			t.Setenv("TERM", "")
			t.Setenv("GHOSTTY_RESOURCES_DIR", "")
			t.Setenv(kv.key, kv.val)
			if got := DetectProtocol(); got != ProtoKitty {
				t.Errorf("Ghostty signal %s=%q: got %s, want kitty", kv.key, kv.val, got)
			}
		})
	}
}

// TestKittyOutputShape verifies the kitty escape envelope is well-formed
// enough that a terminal would at least try to parse it.
func TestKittyOutputShape(t *testing.T) {
	s, err := Render(ProtoKitty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, "\x1b_G") {
		t.Errorf("kitty output missing APC + G prefix: %q", s[:min(20, len(s))])
	}
	if !strings.Contains(s, "\x1b\\") {
		t.Error("kitty output missing String Terminator (ESC \\)")
	}
}

// TestITerm2OutputShape verifies the iTerm2 OSC envelope.
func TestITerm2OutputShape(t *testing.T) {
	s, err := Render(ProtoITerm2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, "\x1b]1337;File=") {
		t.Errorf("iterm2 output missing OSC 1337 prefix")
	}
	if !strings.Contains(s, "\x07") {
		t.Error("iterm2 output missing BEL terminator")
	}
}

// TestHalfBlockShape verifies the half-block output has the requested row
// count and uses the upper-half-block glyph. Pass cols == 2*rows for a
// square source to get correct on-screen aspect (cells are ~1:2 W:H).
func TestHalfBlockShape(t *testing.T) {
	img, err := decodePortrait()
	if err != nil {
		t.Fatal(err)
	}
	const cols, rows = 36, 18
	s := renderHalfBlock(img, cols, rows)
	if !strings.Contains(s, "▀") {
		t.Error("half-block output missing ▀ glyph")
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) != rows {
		t.Errorf("half-block rows: want %d got %d", rows, len(lines))
	}
}

// TestRenderRailAspect verifies the rail helper produces the requested
// number of cell rows for a 2:1 cols:rows aspect (the correct ratio for
// our square source image).
func TestRenderRailAspect(t *testing.T) {
	s := RenderRail(36, 18)
	if s == "" {
		t.Fatal("RenderRail returned empty string")
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) != 18 {
		t.Errorf("rail rows: want 18 got %d", len(lines))
	}
}
