package chat

import (
	"fmt"
	"testing"
)

// The render cache is the fix for the long-conversation resume stall:
// renderInner re-runs composeTranscript on EVERY View() frame, so without a
// cache a long transcript re-glamours every sealed message ~30x/sec, pegging
// the CPU and starving the input reader (stray "[" leaks, stalled keystrokes,
// churning scroll). These tests pin that the per-frame recompose is cheap.

// TestComposeTranscript_CachesAssistantMarkdownAcrossFrames is the headline
// regression guard: re-composing the same transcript at the same width (what
// renderInner does every frame) must not re-render any markdown.
func TestComposeTranscript_CachesAssistantMarkdownAcrossFrames(t *testing.T) {
	resetMarkdownCache()
	md, err := newMarkdownRenderer(80)
	if err != nil {
		t.Fatalf("newMarkdownRenderer: %v", err)
	}
	const n = 25
	entries := make([]transcriptEntry, 0, n)
	for i := 0; i < n; i++ {
		entries = append(entries, transcriptEntry{
			kind: entryAssistantMessage,
			text: fmt.Sprintf("**reply %d** with some `code` and a point.", i),
		})
	}

	// First frame: every assistant message is a cold cache miss.
	_ = composeTranscript(entries, "", "", md, nil, 80, false)
	if mdMisses != n {
		t.Fatalf("first compose: glamour misses = %d, want %d", mdMisses, n)
	}

	// Subsequent frames over the unchanged transcript must be pure cache
	// hits - no glamour, the whole point of the fix.
	for f := 0; f < 8; f++ {
		_ = composeTranscript(entries, "", "", md, nil, 80, false)
	}
	if mdMisses != n {
		t.Errorf("re-composing re-rendered markdown: misses %d, want %d (every-frame glamour is back)", mdMisses, n)
	}
}

// TestRenderAssistantMarkdownCached_KeyedByWidthAndText checks correctness
// (cached output equals the uncached render) plus the hit/miss keying.
func TestRenderAssistantMarkdownCached_KeyedByWidthAndText(t *testing.T) {
	resetMarkdownCache()
	md, err := newMarkdownRenderer(80)
	if err != nil {
		t.Fatalf("newMarkdownRenderer: %v", err)
	}
	const text = "a **bold** claim with `code`"

	got := renderAssistantMarkdownCached(text, 80, md)
	if want := renderAssistantMarkdown(text, 80, md); got != want {
		t.Errorf("cached output differs from direct render:\n got=%q\nwant=%q", got, want)
	}
	if mdMisses != 1 {
		t.Fatalf("after first render misses = %d, want 1", mdMisses)
	}

	// Same (text,width) is a hit.
	if again := renderAssistantMarkdownCached(text, 80, md); again != got {
		t.Errorf("cache hit returned different output")
	}
	if mdMisses != 1 {
		t.Errorf("same (text,width) re-rendered: misses = %d, want 1", mdMisses)
	}

	// A different width is a distinct key, so a miss.
	md2, err := newMarkdownRenderer(100)
	if err != nil {
		t.Fatalf("newMarkdownRenderer: %v", err)
	}
	_ = renderAssistantMarkdownCached(text, 100, md2)
	if mdMisses != 2 {
		t.Errorf("different width should miss: misses = %d, want 2", mdMisses)
	}
}

// TestMarkdownCache_OverflowResets pins the unbounded-growth guard: past the
// cap the cache is dropped wholesale rather than growing forever.
func TestMarkdownCache_OverflowResets(t *testing.T) {
	resetMarkdownCache()
	md, err := newMarkdownRenderer(80)
	if err != nil {
		t.Fatalf("newMarkdownRenderer: %v", err)
	}
	for i := 0; i < mdCacheMax+50; i++ {
		renderAssistantMarkdownCached(fmt.Sprintf("message number %d", i), 80, md)
	}
	mdCacheMu.Lock()
	size := len(mdCache)
	mdCacheMu.Unlock()
	if size > mdCacheMax {
		t.Errorf("cache grew past the cap: size = %d, max = %d", size, mdCacheMax)
	}
}
