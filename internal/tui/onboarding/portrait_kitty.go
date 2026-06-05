package onboarding

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// renderKitty emits the Kitty graphics protocol escape sequence to display
// the PNG inline at the source's natural size. Splash-style usage only;
// onboarding's persistent left rail uses renderKittyCells instead.
//
// Protocol reference: https://sw.kovidgoyal.net/kitty/graphics-protocol/
//
// action=T (transmit + display), format=100 (PNG), chunked (m=1 with a
// terminator m=0) because Kitty refuses payloads larger than 4096 base64
// bytes per chunk.
func renderKitty(png []byte) (string, error) {
	enc := base64.StdEncoding.EncodeToString(png)
	const chunkSize = 4096

	var b strings.Builder
	if len(enc) <= chunkSize {
		b.WriteString("\x1b_Gf=100,a=T;")
		b.WriteString(enc)
		b.WriteString("\x1b\\")
		b.WriteString("\n")
		return b.String(), nil
	}

	for i := 0; i < len(enc); i += chunkSize {
		end := i + chunkSize
		if end > len(enc) {
			end = len(enc)
		}
		chunk := enc[i:end]
		more := 1
		if end == len(enc) {
			more = 0
		}
		if i == 0 {
			b.WriteString("\x1b_Gf=100,a=T,m=")
		} else {
			b.WriteString("\x1b_Gm=")
		}
		if more == 1 {
			b.WriteString("1;")
		} else {
			b.WriteString("0;")
		}
		b.WriteString(chunk)
		b.WriteString("\x1b\\")
	}
	b.WriteString("\n")
	return b.String(), nil
}

// renderKittyCells displays the PNG into an exact (cols × rows) cell box.
// This is the rail-friendly variant the onboarding flow uses.
//
// Key parameters:
//   - c=<cols>, r=<rows>: Kitty resamples the image to fit this cell box.
//   - C=1: do NOT advance the cursor after display. Lets us emit the
//     layout-reservation spaces beneath the image without the cursor
//     drifting into the wrong row.
//   - q=2: silence Kitty's OK/ERROR responses (would otherwise pollute
//     bubbletea's input stream).
//
// Layout reservation: after the escape we emit `rows` lines of `cols`
// spaces. Kitty renders images ABOVE the text layer by default, so the
// spaces beneath don't erase the image. lipgloss measures the spaces as
// visible content (rows × cols cells) so the surrounding layout
// composes correctly. This is the trick that makes Kitty composable
// with per-frame bubbletea redraws.
func renderKittyCells(png []byte, cols, rows int) (string, error) {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	enc := base64.StdEncoding.EncodeToString(png)
	const chunkSize = 4096

	var b strings.Builder
	if len(enc) <= chunkSize {
		fmt.Fprintf(&b, "\x1b_Gf=100,a=T,c=%d,r=%d,C=1,q=2;%s\x1b\\",
			cols, rows, enc)
	} else {
		for i := 0; i < len(enc); i += chunkSize {
			end := i + chunkSize
			if end > len(enc) {
				end = len(enc)
			}
			chunk := enc[i:end]
			more := 1
			if end == len(enc) {
				more = 0
			}
			if i == 0 {
				fmt.Fprintf(&b, "\x1b_Gf=100,a=T,c=%d,r=%d,C=1,q=2,m=%d;%s\x1b\\",
					cols, rows, more, chunk)
			} else {
				fmt.Fprintf(&b, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
			}
		}
	}
	// Reserve `rows` lines of `cols` spaces so lipgloss sees a
	// proper-sized block. Kitty's image layer is above text, so the
	// spaces don't erase the image.
	for i := 0; i < rows; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat(" ", cols))
	}
	return b.String(), nil
}
