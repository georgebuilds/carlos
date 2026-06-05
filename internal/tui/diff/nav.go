package diff

import "strings"

// HunkIndex describes one hunk's location in the rendered output. The
// manage focus pane uses these to implement `n` / `N` hunk navigation:
// the viewport scrolls to StartLine, and the operator gets a quick way
// to jump between unrelated edits in a multi-file diff.
//
// StartLine and EndLine are 0-indexed line numbers in the string that
// [Renderer.RenderWithIndex] returns. They are inclusive on both ends
// and always point at the hunk-header line (StartLine) and the last
// body line of that hunk (EndLine).
type HunkIndex struct {
	// File is the preferred display path for the hunk's owning file —
	// the new-side path when present, else the old-side path. The leading
	// `a/` / `b/` from git diff output is preserved.
	File string
	// StartLine is the 0-indexed line number of the `@@` header for this
	// hunk in the rendered output.
	StartLine int
	// EndLine is the 0-indexed line number of the last body line of this
	// hunk in the rendered output.
	EndLine int
	// NewLines is the count of `+` lines in the hunk body.
	NewLines int
	// DelLines is the count of `-` lines in the hunk body.
	DelLines int
}

// RenderWithIndex is Render plus a per-hunk index. Callers that want
// hunk navigation should use this; callers that just want a styled
// blob can call Render. The two share their implementation — Render
// is a thin wrapper that drops the index.
//
// Index entries appear in the order they show up in the rendered
// string, which is also document order in the underlying diff. Each
// StartLine / EndLine pair is a closed interval over rendered lines
// (split by "\n"); to scroll to a hunk, point the viewport at
// StartLine.
//
// On empty input we return the empty-message string and a nil index.
func (r Renderer) RenderWithIndex(unified []byte) (string, []HunkIndex) {
	if len(unified) == 0 {
		return r.styleEmpty(), nil
	}
	pd := parseUnified(unified)
	if len(pd.files) == 0 {
		return r.styleEmpty(), nil
	}

	var (
		allLines []string
		index    []HunkIndex
	)
	for fi, f := range pd.files {
		if fi > 0 {
			// Blank separator between files for visual grouping. Does
			// not get an index entry; navigation is per-hunk.
			allLines = append(allLines, "")
		}
		fileStart := len(allLines)
		fileLines, fileIdx := r.renderFile(f)
		allLines = append(allLines, fileLines...)
		// Offset hunk-relative indices to the absolute rendered output.
		for _, h := range fileIdx {
			h.StartLine += fileStart
			h.EndLine += fileStart
			index = append(index, h)
		}
	}
	return strings.Join(allLines, "\n"), index
}

// styleEmpty returns the "nothing to render" placeholder. Kept in
// nav.go because both Render and RenderWithIndex consult it, and it's
// closer to the navigation surface than the rendering core.
func (r Renderer) styleEmpty() string {
	return styleFileMuted.Render(emptyMsg)
}
