package onboarding

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// renderITerm2 emits the iTerm2 inline-image escape at a fixed 20×11 cell
// box. Splash-style usage; onboarding's rail uses renderITerm2Cells.
//
// Protocol reference: https://iterm2.com/documentation-images.html
// Form: ESC ] 1337 ; File = [args] : <base64 data> BEL
func renderITerm2(png []byte) (string, error) {
	enc := base64.StdEncoding.EncodeToString(png)
	var b strings.Builder
	fmt.Fprintf(&b,
		"\x1b]1337;File=inline=1;preserveAspectRatio=1;size=%d;width=20;height=11:%s\x07\n",
		len(png), enc,
	)
	return b.String(), nil
}

// renderITerm2Cells displays the PNG sized to (cols × rows) cells.
//
// iTerm2's File= escape supports `width=N` and `height=N` for cell-count
// sizing. preserveAspectRatio=1 keeps the source aspect within the box
// (small letterboxing is acceptable; the alternative of stretching looks
// worse for a face).
//
// Unlike Kitty, iTerm2 has no "don't advance cursor" mode - the cursor
// moves to the line below the image after render. Layout reservation:
// emit the image, then `rows-1` blank lines of `cols` spaces. (The image
// already advanced the cursor by one row; the remaining rows-1 brings
// the cumulative visible footprint to `rows`.)
//
// In iTerm2 the image is part of the text layer (not above-text like
// Kitty), so we cannot overprint with spaces - the fill MUST come AFTER
// the image-advanced cursor, never where the image lives.
func renderITerm2Cells(png []byte, cols, rows int) (string, error) {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	enc := base64.StdEncoding.EncodeToString(png)
	var b strings.Builder
	fmt.Fprintf(&b,
		"\x1b]1337;File=inline=1;preserveAspectRatio=1;size=%d;width=%d;height=%d:%s\x07",
		len(png), cols, rows, enc,
	)
	for i := 0; i < rows-1; i++ {
		b.WriteByte('\n')
		b.WriteString(strings.Repeat(" ", cols))
	}
	return b.String(), nil
}
