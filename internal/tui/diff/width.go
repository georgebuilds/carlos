package diff

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// clipANSI returns s truncated to at most `cells` visible cells. ANSI
// escape sequences (CSI ... `m`) are preserved verbatim and do not
// count toward the width. We rely on lipgloss.Width for the visible
// measurement so wide-rune handling (CJK, emoji) is consistent with
// the rest of the TUI.
//
// If the input already fits we return it unchanged. Otherwise we walk
// rune-by-rune, copying bytes through and tracking visible cells; when
// we hit the limit we close any open style with a reset (`\x1b[0m`)
// so trailing styles don't leak into whatever the viewport renders
// next.
func clipANSI(s string, cells int) string {
	if cells <= 0 || lipgloss.Width(s) <= cells {
		return s
	}
	var (
		b       strings.Builder
		visible int
		sawSGR  bool
	)
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			// Start of CSI sequence — copy through to the final byte
			// (0x40..0x7e). Always emit; never count toward width.
			j := i + 2
			for j < len(s) {
				cc := s[j]
				if cc >= 0x40 && cc <= 0x7e {
					j++
					break
				}
				j++
			}
			b.WriteString(s[i:j])
			if j > i+2 && s[j-1] == 'm' {
				sawSGR = true
			}
			i = j
			continue
		}
		// Regular rune.
		r, size := utf8.DecodeRuneInString(s[i:])
		w := lipgloss.Width(string(r))
		if visible+w > cells {
			break
		}
		b.WriteRune(r)
		visible += w
		i += size
	}
	if sawSGR {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// padOrClip clips s to exactly `cells` visible cells, right-padding
// with spaces if it's short. Used by side-by-side mode to keep both
// columns the same width regardless of content length.
func padOrClip(s string, cells int) string {
	w := lipgloss.Width(s)
	if w == cells {
		return s
	}
	if w > cells {
		return clipANSI(s, cells)
	}
	return s + strings.Repeat(" ", cells-w)
}
