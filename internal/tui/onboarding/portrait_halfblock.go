package onboarding

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"strings"

	"golang.org/x/image/draw"
)

// renderHalfBlock downsamples the source image into a unicode half-block
// approximation. Each character cell encodes two stacked pixels using the
// upper-half-block glyph "▀" with the upper pixel as foreground color and
// the lower pixel as background color. This doubles vertical resolution
// for free.
//
// Output dimensions: caller supplies (cols × rows) target cells. The
// sampled source rectangle is cols pixels wide × rows*2 pixels tall.
//
// Aspect math (load-bearing — easy to get wrong):
// terminal character cells are roughly 1:2 (width:height) in DISPLAY pixels.
// One half-block cell encodes 1 source-pixel-wide × 2 source-pixels-tall
// and is displayed at ~1 cell × ~2 display-pixels (W × H). So:
//
//	displayed-rectangle  = cols × rows*2 display pixels
//	sampled-rectangle    = cols × rows*2 source pixels
//
// Both have the same numeric W×H, so the rendered image preserves the
// source aspect IFF the cell grid satisfies cols == 2*rows*srcAspect,
// where srcAspect = srcW/srcH. For a square source (srcAspect = 1):
// cols should equal 2*rows. Caller is responsible for picking dims; this
// function just samples and emits.
//
// Color path: full truecolor by default. If the environment doesn't claim
// truecolor (COLORTERM != "truecolor"/"24bit"), we still emit 24-bit
// sequences — most modern terminals (Apple Terminal, gnome-terminal,
// xterm-256) accept them and degrade silently. The visible delta in 256-
// color mode is mild on a face this small.
func renderHalfBlock(src image.Image, cols, rows int) string {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	pxW := cols
	pxH := rows * 2

	dst := image.NewRGBA(image.Rect(0, 0, pxW, pxH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)

	truecolor := isTruecolor()
	var b strings.Builder

	for y := 0; y < rows; y++ {
		// Track previous-cell colors to elide redundant SGR sequences.
		// In practice the savings are small on a face-sized image
		// (~1KB out of ~3KB) but it makes the output noticeably less
		// noisy in screenshot pastes.
		var prevFg, prevBg color.RGBA
		var havePrev bool

		for x := 0; x < cols; x++ {
			top := rgbaAt(dst, x, y*2)
			bot := rgbaAt(dst, x, y*2+1)

			// Transparent pixels: treat as terminal default
			// background (reset both fg and bg, emit space). Falls
			// back to a half-block-with-only-fg when only one of
			// the two pixels is transparent.
			topAlpha := top.A >= 32
			botAlpha := bot.A >= 32

			switch {
			case !topAlpha && !botAlpha:
				if havePrev {
					b.WriteString("\x1b[0m")
					havePrev = false
				}
				b.WriteByte(' ')
			case topAlpha && !botAlpha:
				// Upper pixel only → "▀" with fg=top, bg
				// reset to default.
				writeSGR(&b, top, transparent, &prevFg, &prevBg, &havePrev, truecolor)
				b.WriteString("▀")
			case !topAlpha && botAlpha:
				// Lower pixel only → "▄" with fg=bot, bg
				// reset.
				writeSGR(&b, bot, transparent, &prevFg, &prevBg, &havePrev, truecolor)
				b.WriteString("▄")
			default:
				writeSGR(&b, top, bot, &prevFg, &prevBg, &havePrev, truecolor)
				b.WriteString("▀")
			}
		}
		// End of row: reset SGR so the next line starts clean, then
		// newline.
		b.WriteString("\x1b[0m\n")
	}
	return b.String()
}

// transparent is a sentinel used to signal "no background color, reset bg".
var transparent = color.RGBA{0, 0, 0, 0}

// rgbaAt extracts an 8-bit RGBA value from the image.
func rgbaAt(img image.Image, x, y int) color.RGBA {
	r, g, b, a := img.At(x, y).RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
}

// writeSGR appends an SGR escape that sets fg and bg, deduplicating against
// the previous cell where possible. For transparent bg we emit \x1b[49m
// (reset bg only).
func writeSGR(b *strings.Builder, fg, bg color.RGBA, prevFg, prevBg *color.RGBA, havePrev *bool, truecolor bool) {
	if *havePrev && *prevFg == fg && *prevBg == bg {
		return
	}
	if truecolor {
		// fg
		if !*havePrev || *prevFg != fg {
			fmt.Fprintf(b, "\x1b[38;2;%d;%d;%dm", fg.R, fg.G, fg.B)
		}
		// bg
		if bg.A == 0 {
			fmt.Fprint(b, "\x1b[49m")
		} else if !*havePrev || *prevBg != bg {
			fmt.Fprintf(b, "\x1b[48;2;%d;%d;%dm", bg.R, bg.G, bg.B)
		}
	} else {
		// 256-color fallback: 6×6×6 color cube starts at index 16.
		if !*havePrev || *prevFg != fg {
			fmt.Fprintf(b, "\x1b[38;5;%dm", rgbTo256(fg))
		}
		if bg.A == 0 {
			fmt.Fprint(b, "\x1b[49m")
		} else if !*havePrev || *prevBg != bg {
			fmt.Fprintf(b, "\x1b[48;5;%dm", rgbTo256(bg))
		}
	}
	*prevFg = fg
	*prevBg = bg
	*havePrev = true
}

// rgbTo256 quantizes an RGBA value into the xterm 256-color palette's
// 6×6×6 RGB cube (indices 16..231). Cheap and lossy; only used when the
// terminal doesn't accept truecolor.
func rgbTo256(c color.RGBA) int {
	r6 := int(c.R) * 5 / 255
	g6 := int(c.G) * 5 / 255
	b6 := int(c.B) * 5 / 255
	return 16 + 36*r6 + 6*g6 + b6
}

// isTruecolor checks the de-facto env-var advertisement for 24-bit color
// support. We don't probe — false negatives degrade gracefully, and the
// probing path adds raw-mode complexity that bubbletea would have to
// coordinate with.
func isTruecolor() bool {
	ct := os.Getenv("COLORTERM")
	return ct == "truecolor" || ct == "24bit"
}
