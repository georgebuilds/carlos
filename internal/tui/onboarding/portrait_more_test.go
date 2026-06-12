package onboarding

import (
	"image/color"
	"strings"
	"testing"
)

// TestBytesEqual covers the small helper used by portraitHasSmall.
func TestBytesEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b []byte
		want bool
	}{
		{"both nil", nil, nil, true},
		{"empty equal", []byte{}, []byte{}, true},
		{"different length", []byte{1, 2}, []byte{1, 2, 3}, false},
		{"same content", []byte{1, 2, 3}, []byte{1, 2, 3}, true},
		{"different content", []byte{1, 2, 3}, []byte{1, 2, 4}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bytesEqual(c.a, c.b); got != c.want {
				t.Errorf("bytesEqual(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestPortraitHasSmall pins that a real tuned small variant ships (slice
// 9i installed one: area-averaged + sharpened + alpha-hardened for the
// 18x18 half-block sample grid). If this regresses to false, the small
// asset was overwritten with a byte-identical copy of the main portrait
// and the rail fallback silently loses the tuned rendering.
func TestPortraitHasSmall(t *testing.T) {
	if !portraitHasSmall() {
		t.Error("portraitHasSmall() = false; expected the tuned small variant (bytes must differ from carlos-portrait.png)")
	}
}

// TestDecodePortraitForRail exercises the cached decode path. Returns a
// valid image for both the small-variant-present and not-present cases
// (today: present, so this takes the small-variant branch).
func TestDecodePortraitForRail(t *testing.T) {
	img, err := decodePortraitForRail()
	if err != nil {
		t.Fatalf("decodePortraitForRail: %v", err)
	}
	if img == nil {
		t.Fatal("decodePortraitForRail returned nil image without error")
	}
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		t.Errorf("decoded image has zero size: %v", b)
	}
}

// TestDecodePortraitSmall directly exercises the small-variant decoder.
func TestDecodePortraitSmall(t *testing.T) {
	img, err := decodePortraitSmall()
	if err != nil {
		t.Fatalf("decodePortraitSmall: %v", err)
	}
	if img == nil {
		t.Fatal("decodePortraitSmall returned nil image")
	}
}

// TestRgbTo256 pins the xterm 6x6x6 cube quantization.
func TestRgbTo256(t *testing.T) {
	cases := []struct {
		name string
		in   color.RGBA
		want int
	}{
		{"black", color.RGBA{R: 0, G: 0, B: 0}, 16},
		{"white", color.RGBA{R: 255, G: 255, B: 255}, 16 + 36*5 + 6*5 + 5},
		{"pure red max", color.RGBA{R: 255, G: 0, B: 0}, 16 + 36*5},
		{"pure green max", color.RGBA{R: 0, G: 255, B: 0}, 16 + 6*5},
		{"pure blue max", color.RGBA{R: 0, G: 0, B: 255}, 16 + 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rgbTo256(c.in); got != c.want {
				t.Errorf("rgbTo256(%+v) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestRgbTo256_RangeBounds verifies the output always lands in the
// xterm 256-color cube range [16, 231].
func TestRgbTo256_RangeBounds(t *testing.T) {
	for r := 0; r <= 255; r += 51 {
		for g := 0; g <= 255; g += 51 {
			for b := 0; b <= 255; b += 51 {
				c := color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b)}
				got := rgbTo256(c)
				if got < 16 || got > 231 {
					t.Errorf("rgbTo256(%+v) = %d, out of [16,231]", c, got)
				}
			}
		}
	}
}

// TestRenderITerm2Cells_OutputShape verifies the iterm2 cell-size escape
// wraps the PNG with the OSC 1337 prefix and BEL terminator, declares
// the right cell width/height, and appends rows-1 padding lines.
func TestRenderITerm2Cells_OutputShape(t *testing.T) {
	const cols, rows = 18, 9
	s, err := renderITerm2Cells(portraitPNG, cols, rows)
	if err != nil {
		t.Fatalf("renderITerm2Cells: %v", err)
	}
	if !strings.HasPrefix(s, "\x1b]1337;File=") {
		t.Errorf("missing OSC 1337 prefix: %q", s[:min(20, len(s))])
	}
	if !strings.Contains(s, "\x07") {
		t.Error("missing BEL terminator")
	}
	if !strings.Contains(s, "width=18") || !strings.Contains(s, "height=9") {
		t.Errorf("missing cell-size declaration; output prefix:\n%s", s[:min(120, len(s))])
	}
	// Layout reservation: rows-1 newlines after the escape (the image
	// already advanced the cursor by one row).
	// We just confirm there are newlines in there.
	if !strings.Contains(s, "\n") {
		t.Error("expected layout-reservation newlines after the image escape")
	}
}

// TestRenderITerm2Cells_ClampsToMinimum verifies cols/rows < 1 are
// clamped (no divide-by-zero, no panic).
func TestRenderITerm2Cells_ClampsToMinimum(t *testing.T) {
	cases := []struct{ cols, rows int }{
		{0, 0},
		{-1, -1},
		{0, 5},
		{5, 0},
	}
	for _, c := range cases {
		s, err := renderITerm2Cells(portraitPNG, c.cols, c.rows)
		if err != nil {
			t.Errorf("renderITerm2Cells(%d,%d): %v", c.cols, c.rows, err)
		}
		if s == "" {
			t.Errorf("renderITerm2Cells(%d,%d): empty output", c.cols, c.rows)
		}
	}
}
