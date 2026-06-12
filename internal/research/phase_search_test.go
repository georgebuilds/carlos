package research_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

func TestSearch_DedupesURLsAcrossSubQueries(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1\nsub2",
		// read s1, read s2, synthesis — so we can let the pipeline
		// run through and inspect Source IDs that fetch assigned.
		`{"text":"x","relevance":7}`,
		`{"text":"y","relevance":6}`,
		"Synthesis [p1]",
	)
	fs := &fakeSearch{
		results: map[string][]tools.SearchResult{
			"sub1": {{Rank: 1, Title: "A", URL: "https://example.com/a"}},
			"sub2": {{Rank: 1, Title: "A again", URL: "https://example.com/a"},
				{Rank: 2, Title: "B", URL: "https://example.com/b"}},
		},
	}
	ff := &fakeFetcher{
		bodies: map[string]string{
			"https://example.com/a": "alpha",
			"https://example.com/b": "beta",
		},
	}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search:          fs,
		Fetcher:         ff,
		SourcesPerQuery: 5,
	}
	report, err := eng.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := len(report.Sources), 2; got != want {
		t.Fatalf("source count = %d want %d (sources=%+v)", got, want, report.Sources)
	}
	// URLs must be unique.
	seen := map[string]bool{}
	for _, s := range report.Sources {
		if seen[s.URL] {
			t.Errorf("duplicate URL %s in sources", s.URL)
		}
		seen[s.URL] = true
	}
}

func TestSearch_PerSubQueryErrorIsConcernNotFatal(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1\nsub2",
		`{"text":"x","relevance":7}`,
		"Synthesis [p1]",
	)
	fs := &errorThenOKSearch{
		errFor: "sub1",
		ok:     []tools.SearchResult{{Rank: 1, Title: "B", URL: "https://example.com/b"}},
	}
	ff := &fakeFetcher{bodies: map[string]string{"https://example.com/b": "beta"}}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search:          fs,
		Fetcher:         ff,
		SourcesPerQuery: 5,
	}
	report, err := eng.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(report.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(report.Sources))
	}
	concernedAboutSub1 := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "search:") && strings.Contains(c, "sub1") {
			concernedAboutSub1 = true
		}
	}
	if !concernedAboutSub1 {
		t.Errorf("expected per-sub-query search concern, got %v", report.Concerns)
	}
}

func TestSearch_NoSourcesAcrossAnySubQueryFails(t *testing.T) {
	prov := newScriptedProvider("p1", "sub1\nsub2")
	fs := &fakeSearch{} // empty everywhere
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search:  fs,
		Fetcher: &fakeFetcher{bodies: map[string]string{}},
	}
	report, err := eng.Run(context.Background(), "q")
	if err == nil {
		t.Fatalf("expected error when no sources found")
	}
	if report == nil {
		t.Fatal("report should be non-nil even on phase failure")
	}
	if !strings.Contains(err.Error(), "search") {
		t.Errorf("error %v should mention search", err)
	}
}

func TestSearch_RespectsSourcesPerQuery(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"a","relevance":7}`,
		`{"text":"b","relevance":7}`,
		"Synthesis [p1]",
	)
	fs := &fakeSearch{
		results: map[string][]tools.SearchResult{
			"sub1": {
				{Rank: 1, Title: "A", URL: "https://a.example.com"},
				{Rank: 2, Title: "B", URL: "https://b.example.com"},
				{Rank: 3, Title: "C", URL: "https://c.example.com"},
				{Rank: 4, Title: "D", URL: "https://d.example.com"},
				{Rank: 5, Title: "E", URL: "https://e.example.com"},
			},
		},
	}
	ff := &fakeFetcher{
		bodies: map[string]string{
			"https://a.example.com": "alpha",
			"https://b.example.com": "beta",
		},
	}
	eng := &research.Engine{
		Provider: prov, Model: "m", SourcesPerQuery: 2,
		Search:  fs,
		Fetcher: ff,
	}
	report, err := eng.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := len(report.Sources); got != 2 {
		t.Errorf("source count = %d want 2; sources=%+v", got, report.Sources)
	}
}

// errorThenOKSearch returns an error for queries matching errFor and
// `ok` results otherwise. Lets a test exercise the "one sub-query
// errors, others succeed" mixed path.
type errorThenOKSearch struct {
	errFor string
	ok     []tools.SearchResult
}

func (*errorThenOKSearch) Name() string { return "fake-mixed" }
func (e *errorThenOKSearch) Search(ctx context.Context, q string, max int) ([]tools.SearchResult, error) {
	if q == e.errFor {
		return nil, errors.New("simulated backend failure")
	}
	if len(e.ok) > max {
		return e.ok[:max], nil
	}
	return e.ok, nil
}
