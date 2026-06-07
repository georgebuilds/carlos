// Package diff renders unified-diff text into a lipgloss-styled string
// for the manage focus pane. The package is standalone - no manage- or
// chat-specific imports - so the focus-pane integrator (a follow-up
// slice) can wire it up without bleeding TUI state into here.
//
// Public surface is just [Renderer], [Mode], [Renderer.Render], and
// [Renderer.RenderWithIndex] + [HunkIndex]. Everything in this file is
// internal scaffolding for the parser.
package diff

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// parsedDiff is the intermediate representation produced by [parseUnified]
// and consumed by the renderer. Kept deliberately minimal - we don't try
// to reproduce go-diff's full type fidelity, just enough to colorize and
// index the output.
type parsedDiff struct {
	files []parsedFile
}

type parsedFile struct {
	// header lines verbatim - everything before the first @@ hunk header
	// for a file (the `diff --git`, `index`, `---`, `+++`, plus any
	// `new file mode`/`deleted file mode`/`Binary files differ` lines).
	header []string
	// oldPath / newPath extracted from `--- a/<path>` / `+++ b/<path>`.
	// One of them may be "/dev/null" for added / deleted files.
	oldPath string
	newPath string
	hunks   []parsedHunk
}

type parsedHunk struct {
	// header is the raw `@@ -x,y +z,w @@ optional context` line.
	header string
	// oldStart, newStart are 1-indexed source line numbers from the
	// @@ header. We parse them so the gutter (when enabled) can show
	// original line numbers; the renderer does not currently use them
	// for placement but they're free to compute.
	oldStart int
	newStart int
	// body lines, each retaining its prefix byte (' ', '+', '-', '\').
	lines []string
}

// parseUnified is a tiny hand-rolled unified-diff parser. The format is
// well-specified (POSIX diff plus the git-flavored file headers) and the
// shape we care about is:
//
//	diff --git a/foo b/foo            ← file header start (git only)
//	index abcdef..123456 100644       ← optional
//	--- a/foo                         ← old-side path
//	+++ b/foo                         ← new-side path
//	@@ -1,3 +1,4 @@                   ← hunk header
//	 context line
//	-removed line
//	+added line
//	\ No newline at end of file       ← optional metadata
//
// We tolerate diffs that lack the `diff --git` line (e.g. raw `diff -u`
// output): a `--- ` line outside of a hunk also starts a new file.
//
// The parser does NOT validate hunk line counts against the @@ header -
// if a producer emits a malformed hunk the renderer will still print
// every line it sees, just without any guarantee that the +/- counts
// match what the header advertised. This is intentional: rendering
// best-effort beats refusing to render.
func parseUnified(data []byte) parsedDiff {
	var pd parsedDiff
	if len(data) == 0 {
		return pd
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Default token size is 64 KiB; some diffs (large generated files)
	// exceed that on a single line. 1 MiB is generous and still capped.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		cur     *parsedFile
		curHunk *parsedHunk
	)

	flushFile := func() {
		if cur != nil {
			pd.files = append(pd.files, *cur)
		}
		cur = nil
		curHunk = nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushFile()
			cur = &parsedFile{header: []string{line}}
		case strings.HasPrefix(line, "--- ") && curHunk == nil:
			// A `---` outside of a hunk starts a new file (handles both
			// git-style, where we already opened cur via `diff --git`,
			// and bare unified diffs that have no `diff --git` line).
			if cur == nil {
				cur = &parsedFile{}
			}
			cur.header = append(cur.header, line)
			cur.oldPath = strings.TrimPrefix(line, "--- ")
		case strings.HasPrefix(line, "+++ ") && curHunk == nil:
			if cur == nil {
				cur = &parsedFile{}
			}
			cur.header = append(cur.header, line)
			cur.newPath = strings.TrimPrefix(line, "+++ ")
		case strings.HasPrefix(line, "@@"):
			if cur == nil {
				// hunk before any file header - synthesize an anonymous
				// file so we don't drop the lines on the floor.
				cur = &parsedFile{}
			}
			h := parsedHunk{header: line}
			h.oldStart, h.newStart = parseHunkRange(line)
			cur.hunks = append(cur.hunks, h)
			curHunk = &cur.hunks[len(cur.hunks)-1]
		default:
			if curHunk != nil && len(line) > 0 {
				switch line[0] {
				case ' ', '+', '-', '\\':
					curHunk.lines = append(curHunk.lines, line)
					continue
				}
				// Anything else inside a hunk (blank line, junk) → treat
				// as a context line so we don't drop content. Real git
				// diffs use a leading space for blank context lines so
				// this branch mostly triggers on hand-edited diffs.
				curHunk.lines = append(curHunk.lines, " "+line)
				continue
			}
			// Outside a hunk: append to the current file's header so
			// `index ...`, `new file mode`, `deleted file mode`,
			// `Binary files ... differ`, etc. are preserved verbatim.
			if cur != nil {
				cur.header = append(cur.header, line)
			}
		}
	}
	flushFile()
	return pd
}

// parseHunkRange extracts the two 1-indexed start lines from a hunk
// header `@@ -oldStart[,oldCount] +newStart[,newCount] @@ ...`. Returns
// (0, 0) if the header is malformed; the renderer treats that as
// "unknown start" and still prints the hunk.
func parseHunkRange(header string) (int, int) {
	// Format: "@@ -A,B +C,D @@ ..."  or "@@ -A +C @@ ..." (count=1)
	// We scan twice - first '-' then '+' - and grab the integer up to
	// either ',' or ' '. strconv.Atoi handles the leading-zero edge.
	parse := func(prefix byte) int {
		i := strings.IndexByte(header, prefix)
		if i < 0 {
			return 0
		}
		j := i + 1
		k := j
		for k < len(header) && header[k] != ',' && header[k] != ' ' {
			k++
		}
		n, err := strconv.Atoi(header[j:k])
		if err != nil {
			return 0
		}
		return n
	}
	return parse('-'), parse('+')
}
