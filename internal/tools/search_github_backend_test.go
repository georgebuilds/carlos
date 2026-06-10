package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// runBackend is a tiny helper that wires a fakeGHRunner into a
// GHSearchBackend and returns the recorded args alongside the search
// results. Keeps each test focused on one assertion path.
func runBackend(t *testing.T, b *GHSearchBackend, query string, max int) ([]SearchResult, []string, error) {
	t.Helper()
	results, err := b.Search(context.Background(), query, max)
	var lastArgs []string
	if r, ok := b.Tool.Runner.(*fakeGHRunner); ok {
		lastArgs = r.LastArgs
	}
	return results, lastArgs, err
}

// --- happy path -------------------------------------------------------------

func TestGHBackend_HappyPath_ThreeResults(t *testing.T) {
	const canned = `[
		{
			"path": "kernel/sched/core.c",
			"url": "https://github.com/torvalds/linux/blob/master/kernel/sched/core.c",
			"repository": {"nameWithOwner": "torvalds/linux"},
			"textMatches": [{"fragment": "void schedule(void)"}]
		},
		{
			"path": "fs/read_write.c",
			"url": "https://github.com/torvalds/linux/blob/master/fs/read_write.c",
			"repository": {"nameWithOwner": "torvalds/linux"},
			"textMatches": [{"fragment": "ssize_t read(int fd"}]
		},
		{
			"path": "mm/slab.c",
			"url": "https://github.com/torvalds/linux/blob/master/mm/slab.c",
			"repository": {"nameWithOwner": "torvalds/linux"},
			"textMatches": [{"fragment": "static struct kmem_cache *"}]
		}
	]`
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		return []byte(canned), nil
	}}
	b := &GHSearchBackend{Tool: &GHSearchTool{Runner: runner}}

	results, _, err := runBackend(t, b, "schedule", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	want := []struct {
		rank    int
		title   string
		url     string
		snippet string
	}{
		{1, "torvalds/linux: kernel/sched/core.c", "https://github.com/torvalds/linux/blob/master/kernel/sched/core.c", "void schedule(void)"},
		{2, "torvalds/linux: fs/read_write.c", "https://github.com/torvalds/linux/blob/master/fs/read_write.c", "ssize_t read(int fd"},
		{3, "torvalds/linux: mm/slab.c", "https://github.com/torvalds/linux/blob/master/mm/slab.c", "static struct kmem_cache *"},
	}
	for i, w := range want {
		got := results[i]
		if got.Rank != w.rank {
			t.Errorf("[%d] rank = %d, want %d", i, got.Rank, w.rank)
		}
		if got.Title != w.title {
			t.Errorf("[%d] title = %q, want %q", i, got.Title, w.title)
		}
		if got.URL != w.url {
			t.Errorf("[%d] url = %q, want %q", i, got.URL, w.url)
		}
		if got.Snippet != w.snippet {
			t.Errorf("[%d] snippet = %q, want %q", i, got.Snippet, w.snippet)
		}
		if got.Source != "github" {
			t.Errorf("[%d] source = %q, want github", i, got.Source)
		}
		if got.PublishedAt != "" {
			t.Errorf("[%d] published_at = %q, want empty", i, got.PublishedAt)
		}
	}
}

// --- empty results ----------------------------------------------------------

func TestGHBackend_EmptyResults(t *testing.T) {
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		return []byte("[]"), nil
	}}
	b := &GHSearchBackend{Tool: &GHSearchTool{Runner: runner}}

	results, _, err := runBackend(t, b, "nothing matches this", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
	// The slice must be non-nil so a caller can range over it without
	// the usual "nil vs empty" pitfall.
	if results == nil {
		t.Error("results is nil; want empty slice")
	}
}

// --- error propagation ------------------------------------------------------

func TestGHBackend_UnderlyingErrorBubbles(t *testing.T) {
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		return nil, errors.New("gh: exit status 1 (stderr: rate limit exceeded)")
	}}
	b := &GHSearchBackend{Tool: &GHSearchTool{Runner: runner}}

	_, err := b.Search(context.Background(), "x", 5)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "github:") {
		t.Errorf("error = %v, want substring 'github:'", err)
	}
	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Errorf("error = %v, want substring 'rate limit exceeded'", err)
	}
}

// --- limit handling ---------------------------------------------------------

func TestGHBackend_LimitRespected_NormalMax(t *testing.T) {
	var captured int
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		idx := argIndex(args, "--limit")
		if idx >= 0 && idx+1 < len(args) {
			// Parse the limit out of argv to make sure we passed it
			// through, clamped, to the underlying tool.
			var n int
			for _, c := range args[idx+1] {
				n = n*10 + int(c-'0')
			}
			captured = n
		}
		return []byte("[]"), nil
	}}
	b := &GHSearchBackend{Tool: &GHSearchTool{Runner: runner}}

	if _, err := b.Search(context.Background(), "x", 2); err != nil {
		t.Fatal(err)
	}
	if captured != 2 {
		t.Errorf("argv --limit = %d, want 2", captured)
	}
}

func TestGHBackend_LimitClampedAtMax(t *testing.T) {
	var capturedArgs []string
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		capturedArgs = args
		return []byte("[]"), nil
	}}
	b := &GHSearchBackend{Tool: &GHSearchTool{Runner: runner}}

	// Pass an absurd max; the underlying tool clamps at 30 (or our
	// pre-clamp does first — either way the recorded argv must be 30).
	if _, err := b.Search(context.Background(), "x", 9999); err != nil {
		t.Fatal(err)
	}
	idx := argIndex(capturedArgs, "--limit")
	if idx < 0 || idx+1 >= len(capturedArgs) {
		t.Fatalf("--limit missing from args: %v", capturedArgs)
	}
	if capturedArgs[idx+1] != "30" {
		t.Errorf("--limit value = %q, want 30", capturedArgs[idx+1])
	}
}

func TestGHBackend_LimitZeroDefaults(t *testing.T) {
	var capturedArgs []string
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		capturedArgs = args
		return []byte("[]"), nil
	}}
	b := &GHSearchBackend{Tool: &GHSearchTool{Runner: runner}}

	// max=0 should drop to ghSearchDefaultLimit (10).
	if _, err := b.Search(context.Background(), "x", 0); err != nil {
		t.Fatal(err)
	}
	idx := argIndex(capturedArgs, "--limit")
	if idx < 0 || idx+1 >= len(capturedArgs) {
		t.Fatalf("--limit missing from args: %v", capturedArgs)
	}
	if capturedArgs[idx+1] != "10" {
		t.Errorf("--limit value = %q, want 10 (default)", capturedArgs[idx+1])
	}
}

// --- language passthrough ---------------------------------------------------

func TestGHBackend_LanguageFilterPassedThrough(t *testing.T) {
	var capturedArgs []string
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		capturedArgs = args
		return []byte("[]"), nil
	}}
	b := &GHSearchBackend{
		Tool:     &GHSearchTool{Runner: runner},
		Language: "go",
	}
	if _, err := b.Search(context.Background(), "x", 5); err != nil {
		t.Fatal(err)
	}
	idx := argIndex(capturedArgs, "--language")
	if idx < 0 || idx+1 >= len(capturedArgs) {
		t.Fatalf("--language missing from args: %v", capturedArgs)
	}
	if capturedArgs[idx+1] != "go" {
		t.Errorf("--language value = %q, want go", capturedArgs[idx+1])
	}
}

func TestGHBackend_LanguageEmptyOmitsFlag(t *testing.T) {
	var capturedArgs []string
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		capturedArgs = args
		return []byte("[]"), nil
	}}
	b := &GHSearchBackend{Tool: &GHSearchTool{Runner: runner}}
	if _, err := b.Search(context.Background(), "x", 5); err != nil {
		t.Fatal(err)
	}
	if argIndex(capturedArgs, "--language") >= 0 {
		t.Errorf("--language should be absent when Language is empty; args = %v", capturedArgs)
	}
}

// --- kind specialization ----------------------------------------------------

func TestGHBackend_AlwaysQueriesKindCode(t *testing.T) {
	var capturedArgs []string
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		capturedArgs = args
		return []byte("[]"), nil
	}}
	b := &GHSearchBackend{Tool: &GHSearchTool{Runner: runner}}
	if _, err := b.Search(context.Background(), "anything", 5); err != nil {
		t.Fatal(err)
	}
	// argv shape: search code <query> --json ... --limit N [...]
	if len(capturedArgs) < 2 || capturedArgs[0] != "search" || capturedArgs[1] != "code" {
		t.Errorf("argv head = %v, want [search code ...]", capturedArgs)
	}
}

// --- repo-empty handling ----------------------------------------------------

func TestGHBackend_RepoEmpty_TitleIsBareTitle(t *testing.T) {
	// When the underlying envelope has no repo, the wrapper must NOT
	// prepend ": ". The title is just the path.
	const canned = `[
		{
			"path": "lonely/path.go",
			"url": "https://example.com/lonely",
			"repository": {"nameWithOwner": ""},
			"textMatches": [{"fragment": "x"}]
		}
	]`
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		return []byte(canned), nil
	}}
	b := &GHSearchBackend{Tool: &GHSearchTool{Runner: runner}}
	results, err := b.Search(context.Background(), "x", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Title != "lonely/path.go" {
		t.Errorf("title = %q, want %q (bare path when repo missing)", results[0].Title, "lonely/path.go")
	}
}

// --- nil tool ---------------------------------------------------------------

func TestGHBackend_NilToolReturnsError(t *testing.T) {
	b := &GHSearchBackend{Tool: nil}
	results, err := b.Search(context.Background(), "x", 5)
	if err == nil {
		t.Fatal("expected error when Tool is nil, got nil")
	}
	if !strings.Contains(err.Error(), "github") {
		t.Errorf("error = %v, want substring 'github'", err)
	}
	// Must return an empty slice, not nil, per the contract documented
	// in the file header.
	if results == nil {
		t.Error("results is nil; want empty slice")
	}
}

// --- context cancellation ---------------------------------------------------

// ctxAwareRunner blocks until ctx is done, then returns ctx.Err. The
// wrapper must let that error propagate through.
type ctxAwareRunner struct{}

func (ctxAwareRunner) Run(ctx context.Context, args []string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestGHBackend_ContextCancellation(t *testing.T) {
	b := &GHSearchBackend{Tool: &GHSearchTool{Runner: ctxAwareRunner{}, Timeout: time.Hour}}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel before we even start to make the test deterministic and
	// fast.
	cancel()

	_, err := b.Search(ctx, "x", 5)
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	// The wrapper prefixes errors with "github:" but the underlying
	// "context canceled" string must survive.
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("error = %v, want substring 'context canceled'", err)
	}
}

// --- name / constructor -----------------------------------------------------

func TestGHBackend_NameIsGithub(t *testing.T) {
	if (&GHSearchBackend{}).Name() != "github" {
		t.Errorf("Name() = %q, want github", (&GHSearchBackend{}).Name())
	}
}

func TestGHBackend_NewGHSearchBackendWiresTool(t *testing.T) {
	b := NewGHSearchBackend()
	if b == nil {
		t.Fatal("NewGHSearchBackend returned nil")
	}
	if b.Tool == nil {
		t.Fatal("NewGHSearchBackend left Tool nil")
	}
	if b.Tool.Runner == nil {
		t.Error("NewGHSearchBackend left Tool.Runner nil; expected realGHRunner")
	}
	if b.Language != "" {
		t.Errorf("Language = %q, want empty default", b.Language)
	}
}

// --- envelope malformed -----------------------------------------------------

// This case is defensive: if the underlying tool somehow produces an
// envelope the wrapper can't decode, the error must surface with a
// "github:" prefix.
//
// In practice we can't break the envelope through the public surface
// (the tool always marshals a well-formed struct). To exercise the
// decode error path we'd need to stub the entire Execute call —
// overkill for a defensive branch. We instead assert the wrapper's
// Source tag on every result via a second pass, which is the more
// observable contract.
func TestGHBackend_AllResultsTaggedSourceGithub(t *testing.T) {
	const canned = `[
		{"path": "a.go", "url": "u1", "repository": {"nameWithOwner": "o/r"}, "textMatches": [{"fragment": "x"}]},
		{"path": "b.go", "url": "u2", "repository": {"nameWithOwner": "o/r"}, "textMatches": [{"fragment": "y"}]}
	]`
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		return []byte(canned), nil
	}}
	b := &GHSearchBackend{Tool: &GHSearchTool{Runner: runner}}
	results, err := b.Search(context.Background(), "x", 5)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range results {
		if r.Source != "github" {
			t.Errorf("[%d] source = %q, want github", i, r.Source)
		}
	}
}

// --- output round-trips through JSON ---------------------------------------

func TestGHBackend_ResultsMarshalJSONCleanly(t *testing.T) {
	const canned = `[
		{"path": "a.go", "url": "u", "repository": {"nameWithOwner": "o/r"}, "textMatches": [{"fragment": "x"}]}
	]`
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		return []byte(canned), nil
	}}
	b := &GHSearchBackend{Tool: &GHSearchTool{Runner: runner}}
	results, err := b.Search(context.Background(), "x", 5)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(results)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back []SearchResult
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back) != 1 || back[0].Title != "o/r: a.go" {
		t.Errorf("round-trip failed: %+v", back)
	}
}
