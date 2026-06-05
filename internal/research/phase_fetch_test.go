package research_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// fetchEngine constructs an Engine pre-loaded with a one-sub-query
// decompose and one search result, so tests can focus on fetch.
func fetchEngine(bodies map[string]string, failFor map[string]error) *research.Engine {
	prov := newScriptedProvider("p1",
		"sub-q-one",
		// Read phase responses (one per source). Pre-fill enough that
		// runs which proceed past fetch don't starve the provider.
		`{"text":"ex","relevance":7}`,
		`{"text":"ex2","relevance":6}`,
		// Synthesis response.
		"A synthesis citing [p1].",
	)
	urls := make([]tools.SearchResult, 0, len(bodies))
	for u := range bodies {
		urls = append(urls, tools.SearchResult{Rank: len(urls) + 1, Title: "T", URL: u})
	}
	for u := range failFor {
		urls = append(urls, tools.SearchResult{Rank: len(urls) + 1, Title: "F", URL: u})
	}
	fs := &fakeSearch{defaultResults: urls}
	ff := &fakeFetcher{bodies: bodies, titles: map[string]string{}, failFor: failFor}
	return &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff,
		SourcesPerQuery: 10,
	}
}

func TestFetch_AssignsStableIDsInOrder(t *testing.T) {
	bodies := map[string]string{
		"https://a.example.com": "alpha body",
		"https://b.example.com": "beta body",
	}
	eng := fetchEngine(bodies, nil)
	report, _ := eng.Run(context.Background(), "q")
	if len(report.Sources) < 2 {
		t.Fatalf("want 2 sources fetched, got %d", len(report.Sources))
	}
	// IDs are s1, s2, … in fetch order.
	for i, s := range report.Sources {
		want := "s" + strings.TrimSpace(itoa(i+1))
		if s.ID != want {
			t.Errorf("Sources[%d].ID = %q want %q", i, s.ID, want)
		}
	}
}

func TestFetch_FailedSourceIsConcernNotFatal(t *testing.T) {
	bodies := map[string]string{
		"https://ok.example.com": "ok body",
	}
	failFor := map[string]error{
		"https://bad.example.com": errors.New("simulated 500"),
	}
	eng := fetchEngine(bodies, failFor)
	report, _ := eng.Run(context.Background(), "q")
	if len(report.Sources) != 1 {
		t.Fatalf("want 1 fetched source, got %d", len(report.Sources))
	}
	if report.Sources[0].ID != "s1" {
		t.Errorf("surviving source should have id s1, got %q", report.Sources[0].ID)
	}
	hasFetchConcern := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "fetch:") && strings.Contains(c, "bad.example.com") {
			hasFetchConcern = true
		}
	}
	if !hasFetchConcern {
		t.Errorf("expected fetch concern about bad URL; concerns=%v", report.Concerns)
	}
}

func TestFetch_TracksByteBudget(t *testing.T) {
	bodies := map[string]string{
		"https://a.example.com": "hello world",
	}
	eng := fetchEngine(bodies, nil)
	report, _ := eng.Run(context.Background(), "q")
	if got, want := report.Budget.FetchedBytes, int64(len("hello world")); got != want {
		t.Errorf("FetchedBytes = %d want %d", got, want)
	}
}

// itoa is a minimal int→string for the test helper above so we avoid
// pulling strconv into a tight test file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
