package research

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/georgebuilds/carlos/internal/tools"
)

// =============================================================================
// Search-phase routing-consumption helpers
// =============================================================================

// recordingBackend remembers every (query, max) it was called with.
// Used by the multi-backend search-consumption tests to assert each
// backend received the tailored query and quota the route phase
// planned.
type recordingBackend struct {
	name    string
	mu      sync.Mutex
	queries []string
	maxes   []int
	results []tools.SearchResult
	err     error
}

func (r *recordingBackend) Name() string { return r.name }
func (r *recordingBackend) Search(ctx context.Context, q string, max int) ([]tools.SearchResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = append(r.queries, q)
	r.maxes = append(r.maxes, max)
	if r.err != nil {
		return nil, r.err
	}
	// Stamp Source for the multi-backend pathway; harmless for single.
	out := make([]tools.SearchResult, len(r.results))
	copy(out, r.results)
	for i := range out {
		out[i].Source = r.name
	}
	return out, nil
}

// noOpFetcher returns an error for any URL. Search-phase tests don't
// care about the fetch phase; we trip the engine into Run() to drive
// the full pipeline but we don't need a real fetcher.
type noOpFetcher struct{}

func (noOpFetcher) Fetch(ctx context.Context, url string) (Source, error) {
	return Source{}, errors.New("not implemented")
}

// =============================================================================
// Tests — search phase consuming report.Routing
// =============================================================================

func TestRunSearch_ConsumesRouting_MultiBackend(t *testing.T) {
	brave := &recordingBackend{
		name: "brave",
		results: []tools.SearchResult{
			{Rank: 1, Title: "Brave A", URL: "https://example.com/brave-a"},
		},
	}
	arxiv := &recordingBackend{
		name: "arxiv",
		results: []tools.SearchResult{
			{Rank: 1, Title: "Arxiv A", URL: "https://example.com/arxiv-a"},
		},
	}
	multi := tools.NewMultiBackend(brave, arxiv)

	eng := &Engine{
		Search:          multi,
		SourcesPerQuery: 3,
	}
	report := &Report{
		Query: Query{Sub: []string{"alpha", "beta"}},
		Routing: []SubQueryRoute{
			{SubQuery: "alpha", Searches: []PlannedSearch{
				{Backend: "brave", Query: "alpha web", MaxResults: 2},
			}},
			{SubQuery: "beta", Searches: []PlannedSearch{
				{Backend: "arxiv", Query: "beta papers", MaxResults: 1},
			}},
		},
	}
	if err := eng.runSearch(context.Background(), report); err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if len(brave.queries) != 1 || brave.queries[0] != "alpha web" {
		t.Errorf("brave should see tailored 'alpha web'; got %v", brave.queries)
	}
	if brave.maxes[0] != 2 {
		t.Errorf("brave should see max=2; got %d", brave.maxes[0])
	}
	if len(arxiv.queries) != 1 || arxiv.queries[0] != "beta papers" {
		t.Errorf("arxiv should see tailored 'beta papers'; got %v", arxiv.queries)
	}
	if arxiv.maxes[0] != 1 {
		t.Errorf("arxiv should see max=1; got %d", arxiv.maxes[0])
	}
	if len(report.Sources) != 2 {
		t.Errorf("expected 2 sources; got %d", len(report.Sources))
	}
}

func TestRunSearch_RoutingDispatchesToCorrectBackendOnly(t *testing.T) {
	// Three backends in the multi; only brave is in the plan. arxiv and
	// wikipedia must NOT receive a Search call.
	brave := &recordingBackend{name: "brave", results: []tools.SearchResult{
		{Rank: 1, URL: "https://example.com/b"},
	}}
	arxiv := &recordingBackend{name: "arxiv"}
	wikipedia := &recordingBackend{name: "wikipedia"}
	multi := tools.NewMultiBackend(brave, arxiv, wikipedia)

	eng := &Engine{Search: multi, SourcesPerQuery: 3}
	report := &Report{
		Query: Query{Sub: []string{"alpha"}},
		Routing: []SubQueryRoute{
			{SubQuery: "alpha", Searches: []PlannedSearch{
				{Backend: "brave", Query: "alpha tailored", MaxResults: 2},
			}},
		},
	}
	if err := eng.runSearch(context.Background(), report); err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if len(brave.queries) != 1 {
		t.Errorf("brave should be called once; got %d", len(brave.queries))
	}
	if len(arxiv.queries) != 0 {
		t.Errorf("arxiv should NOT be called; got %v", arxiv.queries)
	}
	if len(wikipedia.queries) != 0 {
		t.Errorf("wikipedia should NOT be called; got %v", wikipedia.queries)
	}
}

func TestRunSearch_SingleBackend_RoutingTailorsQuery(t *testing.T) {
	// Single (non-multi) backend. The route phase still tailored the
	// query; the search phase ignores the backend name (only one is
	// possible) but does use the tailored query.
	solo := &recordingBackend{
		name: "solo",
		results: []tools.SearchResult{
			{Rank: 1, Title: "T", URL: "https://example.com/s"},
		},
	}
	eng := &Engine{Search: solo, SourcesPerQuery: 3}
	report := &Report{
		Query: Query{Sub: []string{"alpha"}},
		Routing: []SubQueryRoute{
			{SubQuery: "alpha", Searches: []PlannedSearch{
				{Backend: "ignored-anyway", Query: "tailored alpha", MaxResults: 2},
			}},
		},
	}
	if err := eng.runSearch(context.Background(), report); err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if len(solo.queries) != 1 || solo.queries[0] != "tailored alpha" {
		t.Errorf("expected tailored query reached solo backend; got %v", solo.queries)
	}
}

func TestRunSearch_OneBackendErrors_OthersStillRun_ConcernRecorded(t *testing.T) {
	brave := &recordingBackend{name: "brave", err: errors.New("rate limited")}
	arxiv := &recordingBackend{name: "arxiv", results: []tools.SearchResult{
		{Rank: 1, URL: "https://example.com/a"},
	}}
	multi := tools.NewMultiBackend(brave, arxiv)

	eng := &Engine{Search: multi, SourcesPerQuery: 3}
	report := &Report{
		Query: Query{Sub: []string{"alpha"}},
		Routing: []SubQueryRoute{
			{SubQuery: "alpha", Searches: []PlannedSearch{
				{Backend: "brave", Query: "alpha", MaxResults: 2},
				{Backend: "arxiv", Query: "alpha", MaxResults: 2},
			}},
		},
	}
	if err := eng.runSearch(context.Background(), report); err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if len(report.Sources) != 1 {
		t.Errorf("expected arxiv source through; got %d", len(report.Sources))
	}
	// A backend-failure concern from the per-PlannedSearch dispatch
	// should be present.
	found := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "backend=brave") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected per-backend concern naming brave; got %v", report.Concerns)
	}
}

func TestRunSearch_DedupAcrossPlannedSearches(t *testing.T) {
	// Two backends both return the same URL → second occurrence
	// dropped, only one source recorded.
	brave := &recordingBackend{name: "brave", results: []tools.SearchResult{
		{Rank: 1, URL: "https://example.com/shared"},
	}}
	arxiv := &recordingBackend{name: "arxiv", results: []tools.SearchResult{
		{Rank: 1, URL: "https://example.com/shared"},
		{Rank: 2, URL: "https://example.com/unique"},
	}}
	multi := tools.NewMultiBackend(brave, arxiv)

	eng := &Engine{Search: multi, SourcesPerQuery: 3}
	report := &Report{
		Query: Query{Sub: []string{"alpha"}},
		Routing: []SubQueryRoute{
			{SubQuery: "alpha", Searches: []PlannedSearch{
				{Backend: "brave", Query: "alpha", MaxResults: 2},
				{Backend: "arxiv", Query: "alpha", MaxResults: 2},
			}},
		},
	}
	if err := eng.runSearch(context.Background(), report); err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	urls := map[string]int{}
	for _, s := range report.Sources {
		urls[s.URL]++
	}
	if urls["https://example.com/shared"] != 1 {
		t.Errorf("shared URL should appear exactly once; got %d", urls["https://example.com/shared"])
	}
}

func TestRunSearch_EmptyRouting_LegacyFallback(t *testing.T) {
	// Routing not populated → legacy default: one Search per sub-query
	// with the verbatim query string and SourcesPerQuery max.
	solo := &recordingBackend{
		name:    "solo",
		results: []tools.SearchResult{{Rank: 1, URL: "https://example.com/s"}},
	}
	eng := &Engine{Search: solo, SourcesPerQuery: 4}
	report := &Report{Query: Query{Sub: []string{"alpha", "beta"}}}

	if err := eng.runSearch(context.Background(), report); err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if len(solo.queries) != 2 {
		t.Errorf("expected 2 backend calls (one per sub-query); got %d", len(solo.queries))
	}
	for i, want := range []string{"alpha", "beta"} {
		if solo.queries[i] != want {
			t.Errorf("call %d query = %q want %q", i, solo.queries[i], want)
		}
		if solo.maxes[i] != 4 {
			t.Errorf("call %d max = %d want 4", i, solo.maxes[i])
		}
	}
	// Routing got populated with the legacy plan as a side-effect.
	if len(report.Routing) != 2 {
		t.Errorf("legacy fallback should populate Routing; got %+v", report.Routing)
	}
}

func TestRunSearch_NoSubQueries_NoRouting_ReturnsError(t *testing.T) {
	eng := &Engine{Search: &recordingBackend{name: "solo"}, SourcesPerQuery: 3}
	report := &Report{Query: Query{Sub: nil}} // empty
	err := eng.runSearch(context.Background(), report)
	if err == nil {
		t.Fatalf("expected error when neither Routing nor Sub populated")
	}
}

func TestRunSearch_AllPlannedFail_NoSourcesError(t *testing.T) {
	brave := &recordingBackend{name: "brave", err: errors.New("dead")}
	multi := tools.NewMultiBackend(brave)
	eng := &Engine{Search: multi, SourcesPerQuery: 3}
	report := &Report{
		Query: Query{Sub: []string{"alpha"}},
		Routing: []SubQueryRoute{
			{SubQuery: "alpha", Searches: []PlannedSearch{
				{Backend: "brave", Query: "alpha", MaxResults: 1},
			}},
		},
	}
	if err := eng.runSearch(context.Background(), report); err == nil {
		t.Fatalf("expected 'no sources' error")
	}
	if !hasConcernPrefixR(report.Concerns, "search: no sources found") {
		t.Errorf("expected 'no sources found' concern; got %v", report.Concerns)
	}
}

func TestRunSearch_ZeroMaxResultsFallsBackToSourcesPerQuery(t *testing.T) {
	solo := &recordingBackend{
		name:    "solo",
		results: []tools.SearchResult{{Rank: 1, URL: "https://example.com/s"}},
	}
	eng := &Engine{Search: solo, SourcesPerQuery: 7}
	report := &Report{
		Query: Query{Sub: []string{"alpha"}},
		Routing: []SubQueryRoute{
			{SubQuery: "alpha", Searches: []PlannedSearch{
				{Backend: "solo", Query: "alpha", MaxResults: 0},
			}},
		},
	}
	if err := eng.runSearch(context.Background(), report); err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if solo.maxes[0] != 7 {
		t.Errorf("expected max=7 fallback; got %d", solo.maxes[0])
	}
}

func TestRunSearch_CtxCancelledBetweenPlannedSearches(t *testing.T) {
	// First PlannedSearch succeeds; second sees cancelled ctx.
	first := &recordingBackend{
		name:    "first",
		results: []tools.SearchResult{{Rank: 1, URL: "https://example.com/x"}},
	}
	second := &recordingBackend{name: "second"}
	multi := tools.NewMultiBackend(first, second)

	eng := &Engine{Search: multi, SourcesPerQuery: 3}
	report := &Report{
		Query: Query{Sub: []string{"alpha"}},
		Routing: []SubQueryRoute{
			{SubQuery: "alpha", Searches: []PlannedSearch{
				{Backend: "first", Query: "alpha", MaxResults: 1},
				{Backend: "second", Query: "alpha", MaxResults: 1},
			}},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())

	// Replace first backend with one that cancels mid-call so the
	// second PlannedSearch sees the cancelled ctx.
	first.results = nil
	first.err = nil
	cancellingMulti := tools.NewMultiBackend(
		&cancellingBackend{name: "first", cancel: cancel,
			results: []tools.SearchResult{{Rank: 1, URL: "https://example.com/x"}}},
		second,
	)
	eng.Search = cancellingMulti
	err := eng.runSearch(ctx, report)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected ctx.Canceled; got %v", err)
	}
	if len(second.queries) != 0 {
		t.Errorf("second backend should not be called after cancel; got %v", second.queries)
	}
}

func TestSearchBackendName_NilFallsBackToUnknown(t *testing.T) {
	eng := &Engine{}
	if got := eng.searchBackendName(); got != "(unknown)" {
		t.Errorf("nil Search should return '(unknown)'; got %q", got)
	}
}

func TestLegacyRouting_ZeroPerQueryDefaults(t *testing.T) {
	out := legacyRouting([]string{"alpha"}, "solo", 0)
	if len(out) != 1 || len(out[0].Searches) != 1 {
		t.Fatalf("unexpected shape: %+v", out)
	}
	if out[0].Searches[0].MaxResults != DefaultSourcesPerQuery {
		t.Errorf("zero perQuery should default; got %d", out[0].Searches[0].MaxResults)
	}
}

// cancellingBackend invokes its cancel func after recording the call.
// Lets the search loop see ctx.Err() before the next iteration.
type cancellingBackend struct {
	name    string
	cancel  context.CancelFunc
	results []tools.SearchResult
}

func (c *cancellingBackend) Name() string { return c.name }
func (c *cancellingBackend) Search(ctx context.Context, q string, max int) ([]tools.SearchResult, error) {
	c.cancel()
	return c.results, nil
}
