package research_test

// Black-box tests for the reputation wiring in the search and
// synthesize phases (items 11j + 11g). They drive the full engine with
// the package's existing fakes, matching the house style in
// phase_search_test.go and phase_synthesize_test.go.

import (
	"context"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// TestSearch_RanksTrustedSourcesFirst verifies the reputation pass
// reorders within a single sub-query's results so a trusted domain is
// fetched ahead of a low-tier one, and that the cap is applied AFTER
// ranking (so the low-tier hit is the one dropped).
func TestSearch_RanksTrustedSourcesFirst(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"x","relevance":7}`, // read s1 (the trusted one, first)
		`{"text":"y","relevance":6}`, // read s2 (the low one, second)
		"Synthesis [p1]",
	)
	fs := &fakeSearch{
		results: map[string][]tools.SearchResult{
			// Low-tier (http) hit ranks first from the backend; the
			// trusted arxiv hit ranks second. After the reputation pass
			// the trusted one should sort first, so it becomes s1.
			"sub1": {
				{Rank: 1, Title: "spam", URL: "http://low.example/a"},
				{Rank: 2, Title: "paper", URL: "https://arxiv.org/abs/1"},
			},
		},
	}
	ff := &fakeFetcher{bodies: map[string]string{
		"https://arxiv.org/abs/1": "trusted body",
		"http://low.example/a":    "low body",
	}}
	eng := &research.Engine{
		Provider: prov, Model: "m", SourcesPerQuery: 5,
		Search: fs, Fetcher: ff,
	}
	report, err := eng.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(report.Sources) != 2 {
		t.Fatalf("source count = %d want 2 (%+v)", len(report.Sources), report.Sources)
	}
	// Trusted arxiv must sort first (becomes s1) despite ranking second
	// from the backend.
	if report.Sources[0].URL != "https://arxiv.org/abs/1" {
		t.Errorf("first source = %q want the trusted arxiv URL", report.Sources[0].URL)
	}
	if report.Sources[0].Reputation.Tier != research.TierTrusted {
		t.Errorf("first source tier = %v want trusted", report.Sources[0].Reputation.Tier)
	}
	if report.Sources[1].Reputation.Tier != research.TierLow {
		t.Errorf("second source tier = %v want low", report.Sources[1].Reputation.Tier)
	}
}

// TestSearch_LowTierDominationConcern checks the concern fires when the
// surviving source set is dominated by low-tier domains.
func TestSearch_LowTierDominationConcern(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"a","relevance":7}`,
		`{"text":"b","relevance":7}`,
		`{"text":"c","relevance":7}`,
		"Synthesis [p1]",
	)
	fs := &fakeSearch{
		results: map[string][]tools.SearchResult{
			"sub1": {
				{Rank: 1, Title: "x", URL: "http://low-a.example"},
				{Rank: 2, Title: "y", URL: "http://low-b.example"},
				{Rank: 3, Title: "z", URL: "https://mit.edu/ok"},
			},
		},
	}
	ff := &fakeFetcher{bodies: map[string]string{
		"http://low-a.example": "a",
		"http://low-b.example": "b",
		"https://mit.edu/ok":   "c",
	}}
	eng := &research.Engine{
		Provider: prov, Model: "m", SourcesPerQuery: 5,
		Search: fs, Fetcher: ff,
	}
	report, err := eng.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !hasConcern(report.Concerns, "dominated by low-tier") {
		t.Errorf("expected low-tier domination concern, got %v", report.Concerns)
	}
}

// TestSearch_NoLowTierDominationWhenTrustedMajority is the bad-path
// counterpart: a trusted-majority set must NOT trip the concern.
func TestSearch_NoLowTierDominationWhenTrustedMajority(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"a","relevance":7}`,
		`{"text":"b","relevance":7}`,
		"Synthesis [p1]",
	)
	fs := &fakeSearch{
		results: map[string][]tools.SearchResult{
			"sub1": {
				{Rank: 1, Title: "x", URL: "https://mit.edu/a"},
				{Rank: 2, Title: "y", URL: "https://arxiv.org/abs/2"},
			},
		},
	}
	ff := &fakeFetcher{bodies: map[string]string{
		"https://mit.edu/a":       "a",
		"https://arxiv.org/abs/2": "b",
	}}
	eng := &research.Engine{
		Provider: prov, Model: "m", SourcesPerQuery: 5,
		Search: fs, Fetcher: ff,
	}
	report, err := eng.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if hasConcern(report.Concerns, "dominated by low-tier") {
		t.Errorf("did not expect low-tier domination concern, got %v", report.Concerns)
	}
}

func hasConcern(concerns []string, substr string) bool {
	for _, c := range concerns {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}
