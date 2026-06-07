//go:build nochroma

// highlight_stub.go is the no-op build of the syntax-highlighting
// surface. It's selected when the `nochroma` build tag is set, which
// compiles chroma out of the binary entirely. Renderer.Highlight = true
// in this build silently degrades to "no highlighting" - diffs still
// render correctly, just without per-token coloring.

package diff

// highlightLine is the no-chroma stub. Returns "" so the caller falls
// back to the un-highlighted body line.
func highlightLine(_, _ string) string { return "" }
