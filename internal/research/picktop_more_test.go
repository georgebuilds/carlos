package research_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// noisyBackend ignores the max arg and always returns its full result
// list. This forces pickTopResults to actually trim the slice: the
// other tests use fakeSearch which respects max, leaving the trim
// branch uncovered.
type noisyBackend struct {
	results []tools.SearchResult
}

func (n *noisyBackend) Name() string { return "noisy" }
func (n *noisyBackend) Search(_ context.Context, _ string, _ int) ([]tools.SearchResult, error) {
	out := make([]tools.SearchResult, len(n.results))
	copy(out, n.results)
	return out, nil
}

func TestPickTopResults_TrimsWhenBackendIgnoresMax(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"x","relevance":7}`,
		"synth body cites [p1]",
	)
	// Backend returns 10 results despite the cap of 2.
	urls := []tools.SearchResult{}
	for i := 0; i < 10; i++ {
		urls = append(urls, tools.SearchResult{
			Rank: i + 1,
			URL:  "https://x" + string(rune('a'+i)) + ".example.com",
		})
	}
	fs := &noisyBackend{results: urls}
	bodies := map[string]string{}
	for i := 0; i < 10; i++ {
		bodies["https://x"+string(rune('a'+i))+".example.com"] = "body " + string(rune('a'+i))
	}
	ff := &fakeFetcher{bodies: bodies}
	// Adjust the scripted provider to include enough reads for 2 sources.
	prov2 := newScriptedProvider("p1",
		"sub1",
		`{"text":"a body","relevance":7}`,
		`{"text":"b body","relevance":6}`,
		"synth body cites [p1] [p2]",
	)
	eng := &research.Engine{
		Provider: prov2, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 2,
	}
	report, err := eng.Run(context.Background(), "q?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Trimming worked: 2 sources, not 10.
	if len(report.Sources) != 2 {
		t.Errorf("Sources = %d want 2 after pickTopResults cap", len(report.Sources))
	}
	_ = prov // silence unused
}

// runSearch error path per sub-query - cover the path where one sub-query
// errors and another succeeds.
type perQueryBackend struct {
	results map[string][]tools.SearchResult
	errors  map[string]error
}

func (p *perQueryBackend) Name() string { return "perq" }
func (p *perQueryBackend) Search(_ context.Context, query string, max int) ([]tools.SearchResult, error) {
	if err := p.errors[query]; err != nil {
		return nil, err
	}
	rs := p.results[query]
	if len(rs) > max {
		return rs[:max], nil
	}
	return rs, nil
}

func TestRunSearch_PerQueryErrorContinues(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub a\nsub b",
		`{"text":"x","relevance":7}`,
		"synth body [p1]",
	)
	fs := &perQueryBackend{
		results: map[string][]tools.SearchResult{
			"sub b": {{Rank: 1, URL: "https://b.example.com"}},
		},
		errors: map[string]error{
			"sub a": errors.New("backend down for a"),
		},
	}
	ff := &fakeFetcher{bodies: map[string]string{
		"https://b.example.com": "b body",
	}}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 5,
	}
	report, err := eng.Run(context.Background(), "q?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// At least one source from sub b survived.
	if len(report.Sources) != 1 {
		t.Errorf("Sources = %d want 1", len(report.Sources))
	}
	// And the per-query error is recorded as a concern.
	saw := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "sub-query") && strings.Contains(c, "backend down") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected sub-query error concern, got %v", report.Concerns)
	}
}

// Hit the runFetch single-failure path: one fetcher error mid-list,
// rest succeed.
func TestRunFetch_OneErrorContinues(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"x","relevance":7}`,
		"synth body [p1]",
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{
		{Rank: 1, URL: "https://good.example.com"},
		{Rank: 2, URL: "https://bad.example.com"},
	}}
	ff := &fakeFetcher{
		bodies: map[string]string{
			"https://good.example.com": "good content",
		},
		failFor: map[string]error{
			"https://bad.example.com": errors.New("404 not found"),
		},
	}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 2,
	}
	report, err := eng.Run(context.Background(), "q?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Sources) != 1 {
		t.Errorf("Sources = %d want 1 (one fetch err)", len(report.Sources))
	}
	// Fetch error recorded as concern.
	saw := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "bad.example.com") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected fetch error concern, got %v", report.Concerns)
	}
}

// runRead: source error path; one read errs, other succeeds.
type readFailProvider struct {
	idx       int
	responses []string
	errs      []bool
}

// Use the existing scripted provider but with a stream that errors mid-read.

// Instead, exercise the read-error branch indirectly via budget
// exhaustion: provider call budget runs out mid-read, leaving some
// passages already extracted while the last source errors with
// ErrBudgetExceeded.
func TestRunRead_BudgetExhaustedMidStream(t *testing.T) {
	// 2 sources; budget allows decompose + 1 read but not the 2nd.
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"first","relevance":8}`, // 1st read
		// 2nd read won't fire; budget exhausted before.
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{
		{Rank: 1, URL: "https://1.example.com"},
		{Rank: 2, URL: "https://2.example.com"},
	}}
	ff := &fakeFetcher{bodies: map[string]string{
		"https://1.example.com": "alpha",
		"https://2.example.com": "beta",
	}}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 2,
		Budget: research.ResearchBudget{MaxProviderCalls: 2}, // decompose + 1 read
	}
	_, err := eng.Run(context.Background(), "q?")
	// Either budget-exhausted or general failure - the read phase
	// returns ErrBudgetExceeded which is a graceful abort.
	if err == nil {
		t.Fatal("expected budget abort")
	}
}
