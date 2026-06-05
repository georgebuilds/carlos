package research_test

import (
	"context"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// readEngine plumbs a decompose→search→fetch pipeline whose only
// surprise is the read-phase response we want to test.
func readEngine(readBody, secondReadBody string) *research.Engine {
	prov := newScriptedProvider("p1",
		"sub1",     // decompose
		readBody,   // read for s1
		secondReadBody, // read for s2 (used if there are 2 sources)
		"Synthesis. [p1]", // synthesis (only called if read succeeds)
	)
	fs := &fakeSearch{
		defaultResults: []tools.SearchResult{
			{Rank: 1, Title: "A", URL: "https://a.example.com"},
			{Rank: 2, Title: "B", URL: "https://b.example.com"},
		},
	}
	ff := &fakeFetcher{
		bodies: map[string]string{
			"https://a.example.com": "alpha body content",
			"https://b.example.com": "beta body content",
		},
	}
	return &research.Engine{
		Provider: prov, Model: "m", SourcesPerQuery: 5,
		Search: fs, Fetcher: ff,
	}
}

func TestRead_ParsesJSONLPassages(t *testing.T) {
	readBody := `{"text":"first passage","relevance":8}
{"text":"second passage","relevance":5}`
	eng := readEngine(readBody, `{"text":"third","relevance":4}`)
	report, _ := eng.Run(context.Background(), "q")
	if len(report.Passages) < 3 {
		t.Fatalf("want >=3 passages, got %d (%+v)", len(report.Passages), report.Passages)
	}
	// IDs assigned in extraction order across all sources.
	if report.Passages[0].ID != "p1" || report.Passages[1].ID != "p2" {
		t.Errorf("passage IDs not in order: %+v", report.Passages)
	}
	if report.Passages[0].Text != "first passage" {
		t.Errorf("Text[0] = %q", report.Passages[0].Text)
	}
	if report.Passages[0].SourceID != "s1" {
		t.Errorf("SourceID[0] = %q want s1", report.Passages[0].SourceID)
	}
	if report.Passages[2].SourceID != "s2" {
		t.Errorf("SourceID[2] = %q want s2", report.Passages[2].SourceID)
	}
}

func TestRead_SkipsMalformedLines(t *testing.T) {
	readBody := `{"text":"good","relevance":7}
not json at all
{"text":"also good","relevance":6}
{this is broken
{"text":"third","relevance":9}`
	eng := readEngine(readBody, ``) // empty body for second source
	report, _ := eng.Run(context.Background(), "q")
	// Three good lines from source 1, zero from source 2 (empty body).
	good := 0
	for _, p := range report.Passages {
		if p.SourceID == "s1" {
			good++
		}
	}
	if good != 3 {
		t.Errorf("want 3 parsed passages from s1, got %d (passages=%+v)", good, report.Passages)
	}
}

func TestRead_ClampsRelevanceTo1to10(t *testing.T) {
	readBody := `{"text":"low","relevance":-5}
{"text":"high","relevance":42}
{"text":"normal","relevance":7}`
	eng := readEngine(readBody, ``)
	report, _ := eng.Run(context.Background(), "q")
	relByText := map[string]int{}
	for _, p := range report.Passages {
		relByText[p.Text] = p.Relevance
	}
	if relByText["low"] != 1 {
		t.Errorf("low relevance clamp = %d want 1", relByText["low"])
	}
	if relByText["high"] != 10 {
		t.Errorf("high relevance clamp = %d want 10", relByText["high"])
	}
	if relByText["normal"] != 7 {
		t.Errorf("normal relevance = %d want 7", relByText["normal"])
	}
}

func TestRead_DropsEmptyText(t *testing.T) {
	readBody := `{"text":"","relevance":9}
{"text":"   ","relevance":8}
{"text":"actual","relevance":6}`
	eng := readEngine(readBody, ``)
	report, _ := eng.Run(context.Background(), "q")
	if len(report.Passages) != 1 || report.Passages[0].Text != "actual" {
		t.Errorf("want one passage 'actual', got %+v", report.Passages)
	}
}

func TestRead_NoPassagesAcrossAllSourcesFails(t *testing.T) {
	eng := readEngine("", "")
	report, err := eng.Run(context.Background(), "q")
	if err == nil {
		t.Fatalf("expected failure when no passages extracted")
	}
	if report == nil {
		t.Fatal("report should still be non-nil")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("error %v should mention read", err)
	}
}
