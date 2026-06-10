// Tests for MultiBackend fan-out aggregator. Network-free: every test
// uses a synthetic fakeMulti that returns canned results or errors.

package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeMulti is a configurable SearchBackend used only in tests. Each
// field controls one observable behavior of Search.
type fakeMulti struct {
	name    string
	results []SearchResult
	err     error
	// delay sleeps before returning. Use to provoke per-backend timeouts.
	delay time.Duration
	// calls counts how many times Search was invoked. Atomic so concurrent
	// fan-out is safe.
	calls int32
	// observedMax captures the max value the most recent call received.
	// Used to verify perBackendMax propagation.
	observedMax atomic.Int32
}

func (f *fakeMulti) Name() string { return f.name }

func (f *fakeMulti) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	atomic.AddInt32(&f.calls, 1)
	f.observedMax.Store(int32(max))
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	// Return a defensive copy so the test can't be confused by callers
	// mutating results in place.
	out := make([]SearchResult, len(f.results))
	copy(out, f.results)
	return out, nil
}

// mkResult is a constructor that keeps the test tables readable.
func mkResult(rank int, url, title string) SearchResult {
	return SearchResult{Rank: rank, URL: url, Title: title, Snippet: title + " snippet"}
}

func TestMultiBackend_TwoSucceed_InterleaveAndDedup(t *testing.T) {
	t.Parallel()
	a := &fakeMulti{
		name: "a",
		results: []SearchResult{
			mkResult(1, "https://example.com/a1", "A1"),
			mkResult(2, "https://example.com/shared", "A-shared"),
			mkResult(3, "https://example.com/a3", "A3"),
		},
	}
	b := &fakeMulti{
		name: "b",
		results: []SearchResult{
			mkResult(1, "https://example.com/b1", "B1"),
			mkResult(2, "https://example.com/shared", "B-shared"),
			mkResult(3, "https://example.com/b3", "B3"),
		},
	}
	m := NewMultiBackend(a, b)
	got, perBackend, err := m.SearchSubset(context.Background(), "q", 10, nil, 0)
	if err != nil {
		t.Fatalf("SearchSubset returned err: %v", err)
	}
	// Interleave-by-rank order: a1, b1, (rank2: a-shared takes the
	// shared URL; b's rank2 is dropped as dup), a3, b3.
	wantURLs := []string{
		"https://example.com/a1",
		"https://example.com/b1",
		"https://example.com/shared",
		"https://example.com/a3",
		"https://example.com/b3",
	}
	if len(got) != len(wantURLs) {
		t.Fatalf("len(got)=%d want %d (results=%+v)", len(got), len(wantURLs), got)
	}
	for i, want := range wantURLs {
		if got[i].URL != want {
			t.Errorf("got[%d].URL = %q, want %q", i, got[i].URL, want)
		}
	}
	// Source stamping: every result must carry its source backend.
	for i, r := range got {
		if r.Source == "" {
			t.Errorf("got[%d] missing Source", i)
		}
	}
	// The shared row came from 'a' because 'a' is Primary.
	if got[2].Source != "a" {
		t.Errorf("shared row Source = %q, want a", got[2].Source)
	}
	// No errors recorded for successful runs.
	if len(perBackend) != 0 {
		t.Errorf("perBackend = %v, want empty", perBackend)
	}
}

func TestMultiBackend_OneErrors_OthersSucceed(t *testing.T) {
	t.Parallel()
	a := &fakeMulti{
		name:    "a",
		results: []SearchResult{mkResult(1, "https://a/", "A1")},
	}
	bad := &fakeMulti{name: "bad", err: errors.New("kaboom")}
	c := &fakeMulti{
		name:    "c",
		results: []SearchResult{mkResult(1, "https://c/", "C1")},
	}
	m := NewMultiBackend(a, bad, c)
	got, perBackend, err := m.SearchSubset(context.Background(), "q", 10, nil, 0)
	if err != nil {
		t.Fatalf("SearchSubset returned err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got)=%d want 2 (results=%+v)", len(got), got)
	}
	if len(perBackend) != 1 {
		t.Fatalf("perBackend = %v, want one entry", perBackend)
	}
	if perBackend["bad"] == nil {
		t.Errorf("perBackend missing 'bad' key: %v", perBackend)
	}
}

func TestMultiBackend_SlowBackend_TimesOut(t *testing.T) {
	t.Parallel()
	fast := &fakeMulti{
		name:    "fast",
		results: []SearchResult{mkResult(1, "https://fast/", "F1")},
	}
	slow := &fakeMulti{
		name:    "slow",
		results: []SearchResult{mkResult(1, "https://slow/", "S1")},
		delay:   200 * time.Millisecond,
	}
	other := &fakeMulti{
		name:    "other",
		results: []SearchResult{mkResult(1, "https://other/", "O1")},
	}
	m := NewMultiBackend(fast, slow, other)
	m.PerBackendTimeout = 30 * time.Millisecond
	got, perBackend, err := m.SearchSubset(context.Background(), "q", 10, nil, 0)
	if err != nil {
		t.Fatalf("SearchSubset returned err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got)=%d want 2; results=%+v", len(got), got)
	}
	if perBackend["slow"] == nil {
		t.Fatalf("perBackend[slow] is nil; got %v", perBackend)
	}
	// Should be a context-deadline-style error.
	if !errors.Is(perBackend["slow"], context.DeadlineExceeded) {
		t.Errorf("perBackend[slow] = %v, want DeadlineExceeded", perBackend["slow"])
	}
}

func TestMultiBackend_AllFail(t *testing.T) {
	t.Parallel()
	a := &fakeMulti{name: "a", err: errors.New("a-down")}
	b := &fakeMulti{name: "b", err: errors.New("b-down")}
	m := NewMultiBackend(a, b)
	got, perBackend, err := m.SearchSubset(context.Background(), "q", 10, nil, 0)
	if err == nil {
		t.Fatalf("expected error when all backends fail; got results=%+v", got)
	}
	if got != nil {
		t.Errorf("expected nil results; got %+v", got)
	}
	// Wrapped error mentions both backend names.
	msg := err.Error()
	if !strings.Contains(msg, "a-down") || !strings.Contains(msg, "b-down") {
		t.Errorf("error %q missing backend details", msg)
	}
	if len(perBackend) != 2 {
		t.Errorf("perBackend len=%d want 2 (%v)", len(perBackend), perBackend)
	}
}

func TestMultiBackend_ParentCtxCancelled(t *testing.T) {
	t.Parallel()
	slow1 := &fakeMulti{
		name:    "slow1",
		results: []SearchResult{mkResult(1, "https://slow1/", "S1")},
		delay:   500 * time.Millisecond,
	}
	slow2 := &fakeMulti{
		name:    "slow2",
		results: []SearchResult{mkResult(1, "https://slow2/", "S2")},
		delay:   500 * time.Millisecond,
	}
	m := NewMultiBackend(slow1, slow2)
	m.PerBackendTimeout = 1 * time.Second // longer than the test ctx
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	got, perBackend, err := m.SearchSubset(ctx, "q", 10, nil, 0)
	if err == nil {
		t.Fatalf("expected ctx error; got results=%+v", got)
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("expected ctx error; got %v", err)
	}
	// perBackend should reflect at least one backend's state. We don't
	// assert exact contents because there's a small race between
	// goroutine ctx-cancellation and the parent timer.
	if len(perBackend) == 0 {
		t.Errorf("perBackend empty after parent cancel")
	}
}

func TestMultiBackend_MaxZero(t *testing.T) {
	t.Parallel()
	a := &fakeMulti{
		name:    "a",
		results: []SearchResult{mkResult(1, "https://a/", "A1")},
	}
	m := NewMultiBackend(a)
	got, err := m.Search(context.Background(), "q", 0)
	if err != nil {
		t.Fatalf("Search returned err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len(got)=%d want 0", len(got))
	}
	if atomic.LoadInt32(&a.calls) != 0 {
		t.Errorf("backend was called %d times; expected 0 (max=0)", a.calls)
	}
}

func TestMultiBackend_SearchSubset_PicksByName(t *testing.T) {
	t.Parallel()
	arxiv := &fakeMulti{
		name:    "arxiv",
		results: []SearchResult{mkResult(1, "https://arxiv/", "X")},
	}
	brave := &fakeMulti{
		name:    "brave",
		results: []SearchResult{mkResult(1, "https://brave/", "B")},
	}
	ddg := &fakeMulti{
		name:    "duckduckgo",
		results: []SearchResult{mkResult(1, "https://ddg/", "D")},
	}
	m := NewMultiBackend(arxiv, brave, ddg)
	got, _, err := m.SearchSubset(context.Background(), "q", 10, []string{"arxiv", "brave"}, 0)
	if err != nil {
		t.Fatalf("SearchSubset err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got)=%d want 2; got=%+v", len(got), got)
	}
	if atomic.LoadInt32(&ddg.calls) != 0 {
		t.Errorf("ddg was called %d times; expected 0", ddg.calls)
	}
	if atomic.LoadInt32(&arxiv.calls) != 1 || atomic.LoadInt32(&brave.calls) != 1 {
		t.Errorf("expected one call each; arxiv=%d brave=%d", arxiv.calls, brave.calls)
	}
}

func TestMultiBackend_SearchSubset_UnknownNamesDropped(t *testing.T) {
	t.Parallel()
	a := &fakeMulti{
		name:    "a",
		results: []SearchResult{mkResult(1, "https://a/", "A1")},
	}
	m := NewMultiBackend(a)
	got, perBackend, err := m.SearchSubset(context.Background(), "q", 10, []string{"a", "nope", "alsono"}, 0)
	if err != nil {
		t.Fatalf("SearchSubset err: %v", err)
	}
	if len(got) != 1 || got[0].URL != "https://a/" {
		t.Errorf("got=%+v want single 'a' result", got)
	}
	// Unknown names must NOT show up in perBackend - they're silently dropped.
	if len(perBackend) != 0 {
		t.Errorf("perBackend=%v want empty", perBackend)
	}
}

func TestMultiBackend_SearchSubset_MatchesNothing(t *testing.T) {
	t.Parallel()
	a := &fakeMulti{
		name:    "a",
		results: []SearchResult{mkResult(1, "https://a/", "A1")},
	}
	m := NewMultiBackend(a)
	got, perBackend, err := m.SearchSubset(context.Background(), "q", 10, []string{"nope"}, 0)
	if err != nil {
		t.Fatalf("expected nil err; got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty results; got %+v", got)
	}
	if perBackend["multi"] == nil {
		t.Fatalf("perBackend missing 'multi' sentinel: %v", perBackend)
	}
	if !strings.Contains(perBackend["multi"].Error(), "subset matched no backends") {
		t.Errorf("'multi' err = %q, want sentinel text", perBackend["multi"])
	}
	if atomic.LoadInt32(&a.calls) != 0 {
		t.Errorf("a was called %d times; expected 0", a.calls)
	}
}

func TestMultiBackend_SearchSubset_PerBackendMax(t *testing.T) {
	t.Parallel()
	a := &fakeMulti{
		name:    "a",
		results: []SearchResult{mkResult(1, "https://a/", "A1")},
	}
	b := &fakeMulti{
		name:    "b",
		results: []SearchResult{mkResult(1, "https://b/", "B1")},
	}
	m := NewMultiBackend(a, b)
	_, _, err := m.SearchSubset(context.Background(), "q", 10, nil, 2)
	if err != nil {
		t.Fatalf("SearchSubset err: %v", err)
	}
	if a.observedMax.Load() != 2 {
		t.Errorf("a observed max=%d want 2", a.observedMax.Load())
	}
	if b.observedMax.Load() != 2 {
		t.Errorf("b observed max=%d want 2", b.observedMax.Load())
	}
}

func TestMultiBackend_SearchSubset_PerBackendMaxZeroDefaultsToMax(t *testing.T) {
	t.Parallel()
	a := &fakeMulti{
		name:    "a",
		results: []SearchResult{mkResult(1, "https://a/", "A1")},
	}
	m := NewMultiBackend(a)
	_, _, err := m.SearchSubset(context.Background(), "q", 7, nil, 0)
	if err != nil {
		t.Fatalf("SearchSubset err: %v", err)
	}
	if a.observedMax.Load() != 7 {
		t.Errorf("a observed max=%d want 7 (perBackendMax=0 should fall back to max)", a.observedMax.Load())
	}
}

func TestMultiBackend_PrimaryOnly(t *testing.T) {
	t.Parallel()
	a := &fakeMulti{
		name: "a",
		results: []SearchResult{
			mkResult(1, "https://a/1", "A1"),
			mkResult(2, "https://a/2", "A2"),
		},
	}
	m := NewMultiBackend(a)
	got, err := m.Search(context.Background(), "q", 10)
	if err != nil {
		t.Fatalf("Search err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got)=%d want 2; got=%+v", len(got), got)
	}
	if got[0].URL != "https://a/1" || got[1].URL != "https://a/2" {
		t.Errorf("primary-only ordering broke; got=%+v", got)
	}
	if got[0].Source != "a" {
		t.Errorf("Source=%q want a", got[0].Source)
	}
}

func TestMultiBackend_Names(t *testing.T) {
	t.Parallel()
	a := &fakeMulti{name: "a"}
	b := &fakeMulti{name: "b"}
	c := &fakeMulti{name: "c"}
	m := NewMultiBackend(a, b, c)
	got := m.Names()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("Names()=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q want %q", i, got[i], want[i])
		}
	}
}

func TestMultiBackend_Backends(t *testing.T) {
	t.Parallel()
	a := &fakeMulti{name: "a"}
	b := &fakeMulti{name: "b"}
	m := NewMultiBackend(a, b)
	got := m.Backends()
	if len(got) != 2 {
		t.Fatalf("Backends len=%d want 2", len(got))
	}
	if got[0] != a || got[1] != b {
		t.Errorf("Backends order broken: %v", got)
	}
}

func TestMultiBackend_DedupNormalisation(t *testing.T) {
	t.Parallel()
	// Two backends produce the "same" URL with trailing-slash + casing
	// differences. The dedup pass should collapse them into one entry.
	a := &fakeMulti{
		name: "a",
		results: []SearchResult{
			mkResult(1, "https://Example.com/foo/", "A"),
		},
	}
	b := &fakeMulti{
		name: "b",
		results: []SearchResult{
			mkResult(1, "https://example.com/foo", "B"),
		},
	}
	m := NewMultiBackend(a, b)
	got, err := m.Search(context.Background(), "q", 10)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected dedup to 1, got %d: %+v", len(got), got)
	}
}

// TestMultiBackend_RankReset verifies that result.Rank is preserved from
// the source backend (not overwritten with merge index). This matters
// because the model interprets Rank as "this URL placed Nth on the SERP".
func TestMultiBackend_RankReset(t *testing.T) {
	t.Parallel()
	a := &fakeMulti{
		name: "a",
		results: []SearchResult{
			mkResult(2, "https://a/2", "A2"), // intentionally not 1
		},
	}
	b := &fakeMulti{
		name: "b",
		results: []SearchResult{
			mkResult(7, "https://b/7", "B7"),
		},
	}
	m := NewMultiBackend(a, b)
	got, err := m.Search(context.Background(), "q", 10)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	// Both results should be present; original ranks preserved.
	if len(got) != 2 {
		t.Fatalf("len=%d want 2; %+v", len(got), got)
	}
	rankByURL := map[string]int{}
	for _, r := range got {
		rankByURL[r.URL] = r.Rank
	}
	if rankByURL["https://a/2"] != 2 {
		t.Errorf("a/2 Rank=%d want 2", rankByURL["https://a/2"])
	}
	if rankByURL["https://b/7"] != 7 {
		t.Errorf("b/7 Rank=%d want 7", rankByURL["https://b/7"])
	}
}

// TestMultiBackend_TrimToMax verifies the post-merge trim. A single backend
// returns more results than max; only the first 'max' should survive.
func TestMultiBackend_TrimToMax(t *testing.T) {
	t.Parallel()
	results := make([]SearchResult, 0, 8)
	for i := 1; i <= 8; i++ {
		results = append(results, mkResult(i, fmt.Sprintf("https://a/%d", i), fmt.Sprintf("A%d", i)))
	}
	a := &fakeMulti{name: "a", results: results}
	m := NewMultiBackend(a)
	got, err := m.Search(context.Background(), "q", 3)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if len(got) != 3 {
		t.Errorf("len=%d want 3", len(got))
	}
}

// TestMultiBackend_ConcurrentSearch_RaceDetectorClean spins up several
// goroutines that all hammer the same *MultiBackend with concurrent
// SearchSubset calls. Run with `-race`: any write/write race on the
// (formerly process-shared) per-backend error map would trip the
// detector. The receiver carries no mutable state, so this must stay
// race-free.
func TestMultiBackend_ConcurrentSearch_RaceDetectorClean(t *testing.T) {
	t.Parallel()
	// Two backends: one always succeeds, one always fails. Concurrent
	// callers should each get their own error map without colliding.
	good := &fakeMulti{
		name:    "good",
		results: []SearchResult{mkResult(1, "https://good/", "G")},
	}
	bad := &fakeMulti{name: "bad", err: errors.New("kaboom")}
	m := NewMultiBackend(good, bad)

	const goroutines = 8
	const callsPerGoroutine = 5
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < callsPerGoroutine; j++ {
				got, perBackend, err := m.SearchSubset(context.Background(), "q", 10, nil, 0)
				if err != nil {
					t.Errorf("SearchSubset err: %v", err)
					return
				}
				if len(got) != 1 {
					t.Errorf("len(got)=%d want 1", len(got))
				}
				// Every call must see 'bad' as a failed backend; the map
				// is per-call so no other goroutine can race it.
				if perBackend["bad"] == nil {
					t.Errorf("perBackend missing 'bad': %v", perBackend)
				}
				if perBackend["good"] != nil {
					t.Errorf("perBackend should not flag 'good': %v", perBackend["good"])
				}
			}
		}()
	}
	wg.Wait()
}

// TestMultiBackend_PerCallIsolation_NoLeak verifies that a 429 from one
// backend on call A does NOT leak into call B's error envelope. Call A
// runs against a misbehaving 'arxiv' that returns rate-limited; call B
// runs against the same instance after A returns and must NOT see the
// 429 in its perBackend map (the backend that misbehaved only does so
// on call A — call B sees a clean run).
func TestMultiBackend_PerCallIsolation_NoLeak(t *testing.T) {
	t.Parallel()
	var callCount atomic.Int32
	flaky := &errorOnceBackend{
		name:    "arxiv",
		failErr: errors.New("HTTP 429: rate limited"),
		count:   &callCount,
		results: []SearchResult{mkResult(1, "https://arxiv/ok/", "OK")},
	}
	clean := &fakeMulti{
		name:    "brave",
		results: []SearchResult{mkResult(1, "https://brave/", "B")},
	}
	m := NewMultiBackend(flaky, clean)

	// Call A: flaky returns 429.
	_, perBackendA, errA := m.SearchSubset(context.Background(), "q", 10, nil, 0)
	if errA != nil {
		t.Fatalf("call A err: %v", errA)
	}
	if perBackendA["arxiv"] == nil {
		t.Fatalf("call A: perBackend missing 'arxiv' 429: %v", perBackendA)
	}
	if !strings.Contains(perBackendA["arxiv"].Error(), "429") {
		t.Errorf("call A: arxiv err = %q, want substring '429'", perBackendA["arxiv"])
	}

	// Call B: flaky now succeeds. perBackend must NOT carry the stale
	// 429 from call A.
	_, perBackendB, errB := m.SearchSubset(context.Background(), "q", 10, nil, 0)
	if errB != nil {
		t.Fatalf("call B err: %v", errB)
	}
	if perBackendB["arxiv"] != nil {
		t.Errorf("call B: arxiv error leaked from call A: %v (got %d total calls)", perBackendB["arxiv"], callCount.Load())
	}
	// Sanity: call A's perBackend map is untouched by call B (the map
	// was returned by value, so the caller owns it).
	if perBackendA["arxiv"] == nil {
		t.Errorf("call A's perBackend was mutated by call B")
	}
}

// errorOnceBackend is a SearchBackend that returns failErr on its first
// invocation and succeeds (returning results) on every subsequent one.
// Used by TestMultiBackend_PerCallIsolation_NoLeak.
type errorOnceBackend struct {
	name    string
	failErr error
	count   *atomic.Int32
	results []SearchResult
}

func (e *errorOnceBackend) Name() string { return e.name }
func (e *errorOnceBackend) Search(_ context.Context, _ string, _ int) ([]SearchResult, error) {
	n := e.count.Add(1)
	if n == 1 {
		return nil, e.failErr
	}
	out := make([]SearchResult, len(e.results))
	copy(out, e.results)
	return out, nil
}
