package diff

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Mode selects the visual layout for [Renderer.Render]. Inline is the
// classic single-column patch view; SideBySide splits each hunk into
// old | new columns with a brand-accent separator.
type Mode int

const (
	// ModeInline renders each hunk as a single column with `+`/`-`
	// prefixes preserved. This is the default and the right choice for
	// narrow terminals.
	ModeInline Mode = iota
	// ModeSideBySide renders two parallel columns per hunk; old lines
	// on the left, new lines on the right, separated by an accent bar.
	// Needs roughly 2× the width of inline to stay legible.
	ModeSideBySide
)

// Renderer is the public entry point. Zero value is a usable inline
// renderer with no width clamp and no syntax highlighting; set the
// fields you need.
//
// Renderer is value-type, immutable per call, and safe to share across
// goroutines as long as callers don't mutate the fields concurrently
// with Render.
type Renderer struct {
	// Width clamps total output cells. 0 disables clamping - useful when
	// the consumer is wrapping in a viewport that already enforces a
	// width.
	Width int
	// Mode picks inline (default) vs side-by-side layout.
	Mode Mode
	// Highlight enables chroma syntax highlighting per +/- line. Off by
	// default because highlighting costs ~milliseconds per kilobyte of
	// source; the diff stays readable without it. Build with `-tags
	// nochroma` to compile chroma out entirely (Highlight becomes a
	// no-op in that build).
	Highlight bool
	// Gutter prepends right-aligned source-line numbers (old | new) to
	// each body line. Off by default - most call sites have a viewport
	// chrome that already gives a sense of position.
	Gutter bool
}

// Brand palette. Duplicated from internal/tui/manage/styles.go (which
// in turn duplicates internal/tui/onboarding) per the project's
// "siblings don't import siblings" rule for scope-disciplined TUI
// packages.
var (
	colorAccent  = lipgloss.Color("#4a6bd6") // brand accent - borders, file headers
	colorMuted   = lipgloss.Color("240")     // dimmed metadata
	colorSubtle  = lipgloss.Color("244")     // hunk header bg-hint via foreground
	colorAdded   = lipgloss.Color("#22863a") // + lines (GitHub-style green)
	colorRemoved = lipgloss.Color("#cb2431") // - lines (GitHub-style red)
	colorGutter  = lipgloss.Color("238")     // gutter line numbers
)

var (
	styleFileBold   = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleFileMuted  = lipgloss.NewStyle().Foreground(colorMuted)
	styleFilePath   = lipgloss.NewStyle().Foreground(colorAccent)
	styleHunkHeader = lipgloss.NewStyle().Foreground(colorSubtle).Bold(true)
	styleAdded      = lipgloss.NewStyle().Foreground(colorAdded)
	styleRemoved    = lipgloss.NewStyle().Foreground(colorRemoved)
	styleContext    = lipgloss.NewStyle()
	styleNoNewline  = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	styleGutter     = lipgloss.NewStyle().Foreground(colorGutter)
	styleSeparator  = lipgloss.NewStyle().Foreground(colorAccent)
)

// emptyMsg is what Render returns for an empty / whitespace-only input.
// The string is plain (no color) so callers can detect "nothing to show"
// by length without parsing ANSI.
const emptyMsg = "no changes"

// Render parses unified-diff bytes and returns a styled string ready
// for a viewport. See package doc for the layout decisions.
//
// Implementation note: this is a thin wrapper around
// [Renderer.RenderWithIndex] that drops the index. Callers that want
// hunk-navigation should use RenderWithIndex directly.
func (r Renderer) Render(unified []byte) string {
	s, _ := r.RenderWithIndex(unified)
	return s
}

// renderFile emits the styled lines for one parsed file and returns
// (lines, hunkIndices). The hunk indices are RELATIVE to the start of
// this file's output - the caller offsets them by the file's
// startLine in the overall rendered string.
func (r Renderer) renderFile(f parsedFile) ([]string, []HunkIndex) {
	out := make([]string, 0, len(f.header)+len(f.hunks)*8)
	// Header: every line gets a style hint based on its prefix.
	for _, h := range f.header {
		out = append(out, r.styleHeaderLine(h))
	}

	indices := make([]HunkIndex, 0, len(f.hunks))
	for _, h := range f.hunks {
		startLine := len(out)
		out = append(out, styleHunkHeader.Render(h.header))

		var newLines, delLines int
		if r.Mode == ModeSideBySide {
			lns, nl, dl := r.renderHunkSideBySide(h)
			out = append(out, lns...)
			newLines, delLines = nl, dl
		} else {
			lns, nl, dl := r.renderHunkInline(h, f.newPath)
			out = append(out, lns...)
			newLines, delLines = nl, dl
		}

		indices = append(indices, HunkIndex{
			File:      preferredPath(f),
			StartLine: startLine,
			EndLine:   len(out) - 1,
			NewLines:  newLines,
			DelLines:  delLines,
		})
	}
	return out, indices
}

// styleHeaderLine picks a lipgloss style for a single header line based
// on its prefix. Anything we don't recognize falls through as muted -
// safer than rendering it bare and surprising the operator with
// uncolored debris in the middle of a colored block.
func (r Renderer) styleHeaderLine(line string) string {
	switch {
	case strings.HasPrefix(line, "diff --git "):
		return styleFileBold.Render(line)
	case strings.HasPrefix(line, "index "):
		return styleFileMuted.Render(line)
	case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
		return styleFilePath.Render(line)
	case strings.HasPrefix(line, "new file mode "),
		strings.HasPrefix(line, "deleted file mode "),
		strings.HasPrefix(line, "old mode "),
		strings.HasPrefix(line, "new mode "),
		strings.HasPrefix(line, "rename "),
		strings.HasPrefix(line, "similarity "),
		strings.HasPrefix(line, "dissimilarity "),
		strings.HasPrefix(line, "copy "),
		strings.HasPrefix(line, "Binary files "):
		return styleFileMuted.Render(line)
	default:
		// Empty or unknown - leave it as-is so we don't accidentally
		// inject ANSI into a blank separator.
		if strings.TrimSpace(line) == "" {
			return line
		}
		return styleFileMuted.Render(line)
	}
}

// renderHunkInline emits each body line of a hunk with +/- coloring.
// Returns the styled lines plus the +/- counts (for the HunkIndex).
//
// Line numbers in the gutter (when enabled) advance per real source
// line: removed lines bump the old counter, added lines bump the new
// counter, context lines bump both. "\ No newline at end of file"
// does not bump either - it annotates the previous line.
func (r Renderer) renderHunkInline(h parsedHunk, newPath string) ([]string, int, int) {
	out := make([]string, 0, len(h.lines))
	oldLine, newLine := h.oldStart, h.newStart
	var newCount, delCount int
	for _, l := range h.lines {
		if l == "" {
			out = append(out, "")
			continue
		}
		prefix := l[0]
		body := l[1:]
		var styled, gutter string
		switch prefix {
		case '+':
			newCount++
			gutter = r.gutterCells(0, newLine)
			styled = styleAdded.Render("+") + r.styleBodyLine(body, styleAdded, newPath)
			newLine++
		case '-':
			delCount++
			gutter = r.gutterCells(oldLine, 0)
			styled = styleRemoved.Render("-") + r.styleBodyLine(body, styleRemoved, newPath)
			oldLine++
		case ' ':
			gutter = r.gutterCells(oldLine, newLine)
			styled = " " + styleContext.Render(body)
			oldLine++
			newLine++
		case '\\':
			gutter = r.gutterCells(0, 0)
			styled = styleNoNewline.Render(l)
		default:
			gutter = r.gutterCells(0, 0)
			styled = l
		}
		if r.Gutter {
			styled = gutter + " " + styled
		}
		out = append(out, r.clampWidth(styled))
	}
	return out, newCount, delCount
}

// renderHunkSideBySide pairs old and new lines into two columns. The
// pairing rule is simple: walk the hunk, accumulating runs of '-' and
// '+' lines, and emit them aligned (extra lines on either side become
// blanks on the other). Context lines flush the current run and emit
// a paired row on both sides.
//
// This is the same heuristic GitHub's split view uses; it isn't
// LCS-optimal but it produces sensible output for the small hunks the
// approval pane will see.
func (r Renderer) renderHunkSideBySide(h parsedHunk) ([]string, int, int) {
	half := r.halfWidth()
	sep := styleSeparator.Render("│")

	var (
		out              []string
		oldBuf, newBuf   []string
		newCount, delCnt int
	)
	flush := func() {
		n := max(len(oldBuf), len(newBuf))
		for i := 0; i < n; i++ {
			var l, r string
			if i < len(oldBuf) {
				l = padOrClip(styleRemoved.Render("- "+oldBuf[i]), half)
			} else {
				l = padOrClip("", half)
			}
			if i < len(newBuf) {
				r = padOrClip(styleAdded.Render("+ "+newBuf[i]), half)
			} else {
				r = padOrClip("", half)
			}
			out = append(out, l+sep+r)
		}
		oldBuf = oldBuf[:0]
		newBuf = newBuf[:0]
	}
	for _, l := range h.lines {
		if l == "" {
			continue
		}
		prefix := l[0]
		body := l[1:]
		switch prefix {
		case '-':
			oldBuf = append(oldBuf, body)
			delCnt++
		case '+':
			newBuf = append(newBuf, body)
			newCount++
		case ' ':
			flush()
			ctx := padOrClip(styleContext.Render("  "+body), half)
			out = append(out, ctx+sep+ctx)
		case '\\':
			flush()
			note := padOrClip(styleNoNewline.Render(l), half)
			out = append(out, note+sep+note)
		}
	}
	flush()
	return out, newCount, delCnt
}

// halfWidth returns the per-column width for side-by-side mode, leaving
// one cell for the center separator. Returns a sane minimum (8) so the
// renderer never produces zero-width columns even on absurd inputs.
func (r Renderer) halfWidth() int {
	if r.Width <= 4 {
		return 8
	}
	half := (r.Width - 1) / 2
	if half < 8 {
		return 8
	}
	return half
}

// clampWidth trims a styled string to r.Width visible cells. ANSI
// escapes are passed through unchanged so colors survive; only printable
// runes count toward the width. If r.Width is 0 (default) this is a
// no-op.
func (r Renderer) clampWidth(s string) string {
	if r.Width <= 0 {
		return s
	}
	return clipANSI(s, r.Width)
}

// gutterCells formats the two line-number cells. A 0 in either position
// renders as a blank-aligned cell so + / - rows stay aligned with
// context rows.
func (r Renderer) gutterCells(oldNum, newNum int) string {
	if !r.Gutter {
		return ""
	}
	width := 4 // four digits handles most real-world files; truncates beyond
	cell := func(n int) string {
		if n == 0 {
			return strings.Repeat(" ", width)
		}
		s := itoaPad(n, width)
		return styleGutter.Render(s)
	}
	return cell(oldNum) + " " + cell(newNum)
}

// styleBodyLine applies the diff-color tint to a body line, optionally
// running it through the syntax highlighter first. The +/- prefix is
// added by the caller; this returns just the body fragment.
//
// When Highlight is on and a language is detected, the highlighter
// colors tokens and we then layer the diff foreground on top - chroma
// uses 256-color ANSI which composes cleanly with our truecolor
// foregrounds because terminals honor the most-recent SGR.
func (r Renderer) styleBodyLine(body string, base lipgloss.Style, path string) string {
	if r.Highlight {
		if lang := detectLanguage(path); lang != "" {
			if hl := highlightLine(lang, body); hl != "" {
				// Wrap in the diff color so the line still reads as +/-.
				return base.Render(hl)
			}
		}
	}
	return base.Render(body)
}

// detectLanguage maps a file path to a chroma lexer alias. Returns ""
// for unknown extensions; the caller skips highlighting in that case.
//
// The list is conservative on purpose - we'd rather skip highlighting
// than guess wrong and produce confusing output. Add extensions as the
// approval pane surfaces them in real use.
func detectLanguage(path string) string {
	if path == "" || path == "/dev/null" {
		return ""
	}
	// Strip leading a/ or b/ that git diff puts in front of paths.
	p := strings.TrimPrefix(path, "a/")
	p = strings.TrimPrefix(p, "b/")
	switch strings.ToLower(filepath.Ext(p)) {
	case ".go":
		return "go"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".js":
		return "javascript"
	case ".jsx":
		return "jsx"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".rb":
		return "ruby"
	case ".java":
		return "java"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp":
		return "cpp"
	case ".sh", ".bash":
		return "bash"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	case ".md":
		return "markdown"
	case ".html":
		return "html"
	case ".css":
		return "css"
	case ".sql":
		return "sql"
	}
	return ""
}

// preferredPath returns the new-side path if it isn't /dev/null,
// otherwise the old-side path. Used for HunkIndex.File so deletions
// still surface the deleted path rather than the literal "/dev/null".
func preferredPath(f parsedFile) string {
	if f.newPath != "" && f.newPath != "/dev/null" {
		return f.newPath
	}
	return f.oldPath
}

// itoaPad is a minimal right-aligned integer formatter. We don't pull
// fmt for this because it shows up in the inner render loop and a
// dedicated formatter is cheaper. strconv handles the int → ascii
// conversion; we just left-pad with spaces.
func itoaPad(n, width int) string {
	s := strconv.Itoa(n)
	if len(s) >= width {
		return s
	}
	return strings.Repeat(" ", width-len(s)) + s
}
