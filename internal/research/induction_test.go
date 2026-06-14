package research_test

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/research"
)

// goodReport builds a Report that clears CanInduceFromResearch: long
// synthesis, two distinct sources, no fatal concerns.
func goodReport() *research.Report {
	return &research.Report{
		Question: "How widely is WebGPU supported across browsers in 2026?",
		Query: research.Query{
			Question: "How widely is WebGPU supported across browsers in 2026?",
			Sub: []string{
				"Which browsers ship WebGPU enabled by default?",
				"What versions first shipped stable support?",
			},
		},
		Sources: []research.Source{
			{ID: "s1", URL: "https://developer.mozilla.org/x", Title: "MDN"},
			{ID: "s2", URL: "https://en.wikipedia.org/wiki/WebGPU", Title: "Wikipedia"},
		},
		Synthesis: strings.Repeat("WebGPU reached broad availability across major browsers by 2026. ", 6),
	}
}

func TestCanInduceFromResearch_GoodReportPasses(t *testing.T) {
	if !research.CanInduceFromResearch(goodReport()) {
		t.Fatal("good report should pass the quality gate")
	}
}

func TestCanInduceFromResearch_RejectsBadReports(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*research.Report)
	}{
		{
			name:   "nil report",
			mutate: nil, // sentinel handled below
		},
		{
			name: "thin synthesis",
			mutate: func(r *research.Report) {
				r.Synthesis = "Too short to be a procedure."
			},
		},
		{
			name: "empty synthesis",
			mutate: func(r *research.Report) {
				r.Synthesis = ""
			},
		},
		{
			name: "single source",
			mutate: func(r *research.Report) {
				r.Sources = r.Sources[:1]
			},
		},
		{
			name: "duplicate sources collapse below threshold",
			mutate: func(r *research.Report) {
				r.Sources = []research.Source{
					{ID: "s1", URL: "https://same.example/x"},
					{ID: "s2", URL: "https://same.example/x"},
				}
			},
		},
		{
			name: "sources with empty URLs do not count",
			mutate: func(r *research.Report) {
				r.Sources = []research.Source{
					{ID: "s1", URL: ""},
					{ID: "s2", URL: "   "},
				}
			},
		},
		{
			name: "fatal concern: phase failed",
			mutate: func(r *research.Report) {
				r.Concerns = []string{"phase=search failed: backend timeout"}
			},
		},
		{
			name: "fatal concern: budget exhausted",
			mutate: func(r *research.Report) {
				r.Concerns = []string{"provider-call budget exhausted at 20 calls"}
			},
		},
		{
			name: "fatal concern: aborted",
			mutate: func(r *research.Report) {
				r.Concerns = []string{"phase=fetch aborted: context deadline exceeded"}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.mutate == nil {
				if research.CanInduceFromResearch(nil) {
					t.Fatal("nil report should be rejected")
				}
				return
			}
			r := goodReport()
			tc.mutate(r)
			if research.CanInduceFromResearch(r) {
				t.Fatalf("%s: expected gate to reject", tc.name)
			}
		})
	}
}

func TestCanInduceFromResearch_AdvisoryConcernStillPasses(t *testing.T) {
	r := goodReport()
	// A non-fatal, advisory concern (no fatal marker) must not veto.
	r.Concerns = []string{"truncated 1 over-cap result from backend brave"}
	if !research.CanInduceFromResearch(r) {
		t.Fatal("advisory concern should not block induction")
	}
}

func TestSummarizeResearchSession_Shape(t *testing.T) {
	r := goodReport()
	r.Sources = append(r.Sources,
		research.Source{ID: "s3", URL: "https://arxiv.org/abs/1234"},
		research.Source{ID: "s4", URL: "https://news.ycombinator.com/item?id=1"},
		research.Source{ID: "s5", URL: "https://example.com/blog"},
	)
	out := research.SummarizeResearchSession(r)

	wantContains := []string{
		"Research session summary",
		"Question: How widely is WebGPU supported",
		"Decomposed along these sub-queries:",
		"Which browsers ship WebGPU enabled by default?",
		"Source tiers used:",
		"trusted:", // mdn + wikipedia + arxiv
		"medium:",  // hacker news + plain blog
		"Synthesis digest:",
	}
	for _, w := range wantContains {
		if !strings.Contains(out, w) {
			t.Errorf("summary missing %q\n---\n%s", w, out)
		}
	}
	if strings.Contains(out, "—") {
		t.Error("summary must not contain em-dashes")
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("summary should end with a single newline")
	}
}

func TestSummarizeResearchSession_NilAndEmpty(t *testing.T) {
	if research.SummarizeResearchSession(nil) != "" {
		t.Error("nil report should summarize to empty string")
	}
	// Report with only a Query.Question (no top-level Question) still
	// surfaces the question via the fallback.
	r := &research.Report{Query: research.Query{Question: "fallback question?"}}
	out := research.SummarizeResearchSession(r)
	if !strings.Contains(out, "fallback question?") {
		t.Errorf("fallback question not surfaced: %q", out)
	}
}

func TestSummarizeResearchSession_TruncatesLongSynthesis(t *testing.T) {
	r := goodReport()
	r.Synthesis = strings.Repeat("x", 5000)
	out := research.SummarizeResearchSession(r)
	if !strings.Contains(out, "[...]") {
		t.Error("long synthesis should be truncated with an ellipsis marker")
	}
	if strings.Count(out, "x") >= 5000 {
		t.Error("synthesis digest should not include the entire body")
	}
}

func TestSummarizeResearchSession_SkipsEmptySubQueries(t *testing.T) {
	r := goodReport()
	r.Query.Sub = []string{"  ", "real sub-query"}
	out := research.SummarizeResearchSession(r)
	if !strings.Contains(out, "real sub-query") {
		t.Error("non-empty sub-query should appear")
	}
	// The blank entry should not render a bare bullet.
	if strings.Contains(out, "  - \n") {
		t.Error("blank sub-query should be skipped, not rendered as an empty bullet")
	}
}

func TestSummarizeResearchSession_TierClassification(t *testing.T) {
	// Each entry maps a source URL to the reputation tier label we expect
	// in the rendered "Source tiers used" block. The breakdown now reuses
	// the canonical reputation classifier, so the vocabulary is
	// trusted/medium/low. Covers whitelisted hosts (incl. .mil), plain
	// https, and the unparseable fallthrough.
	cases := []struct {
		url  string
		tier string
	}{
		{"https://www.whitehouse.gov/x", "trusted"},
		{"https://army.mil/x", "trusted"},
		{"https://mit.edu/x", "trusted"},
		{"https://arxiv.org/abs/1", "trusted"},
		{"https://en.wikipedia.org/wiki/X", "trusted"},
		{"https://developer.mozilla.org/x", "trusted"},
		{"https://www.reddit.com/r/x", "medium"},
		{"https://stackoverflow.com/q/1", "medium"},
		{"https://example.com/page", "medium"},
		{"", "low"},
		{"://bad-url", "low"},
	}
	for _, c := range cases {
		r := goodReport()
		r.Sources = []research.Source{{ID: "s1", URL: c.url}}
		out := research.SummarizeResearchSession(r)
		if !strings.Contains(out, c.tier+":") {
			t.Errorf("url %q: expected tier %q in summary, got:\n%s", c.url, c.tier, out)
		}
	}
}

func TestSummarizeResearchSession_TrustedAndLowTiers(t *testing.T) {
	r := goodReport()
	r.Sources = []research.Source{
		{ID: "s1", URL: "https://www.nasa.gov/report"},
		{ID: "s2", URL: "not a url ::::"},
	}
	out := research.SummarizeResearchSession(r)
	if !strings.Contains(out, "trusted:") {
		t.Errorf(".gov host should classify as trusted tier: %q", out)
	}
	if !strings.Contains(out, "low:") {
		t.Errorf("unparseable URL should fall into low tier: %q", out)
	}
}
