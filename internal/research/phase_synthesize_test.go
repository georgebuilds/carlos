package research_test

import (
	"context"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

func TestSynthesize_WritesBodyFromPassages(t *testing.T) {
	synthBody := "Safari now supports WebGPU in technology preview [p1] but stable release is pending [p2]."
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"Safari TP supports WebGPU as of 2025.","relevance":9}`,
		`{"text":"Stable release timing not announced.","relevance":7}`,
		synthBody,
	)
	fs := &fakeSearch{
		defaultResults: []tools.SearchResult{
			{Rank: 1, Title: "A", URL: "https://a.example.com"},
			{Rank: 2, Title: "B", URL: "https://b.example.com"},
		},
	}
	ff := &fakeFetcher{
		bodies: map[string]string{
			"https://a.example.com": "alpha",
			"https://b.example.com": "beta",
		},
	}
	eng := &research.Engine{
		Provider: prov, Model: "m", SourcesPerQuery: 5,
		Search: fs, Fetcher: ff,
	}
	report, err := eng.Run(context.Background(), "WebGPU in Safari?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.Synthesis != synthBody {
		t.Errorf("Synthesis = %q want %q", report.Synthesis, synthBody)
	}
	if !strings.Contains(report.Synthesis, "[p1]") {
		t.Errorf("synthesis should contain [p1] citation: %q", report.Synthesis)
	}
}

func TestSynthesize_EmptyBodyFails(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"good","relevance":7}`,
		"   ", // synthesis: whitespace only
	)
	fs := &fakeSearch{
		defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://a.example.com"}},
	}
	ff := &fakeFetcher{
		bodies: map[string]string{"https://a.example.com": "alpha"},
	}
	eng := &research.Engine{
		Provider: prov, Model: "m", SourcesPerQuery: 5,
		Search: fs, Fetcher: ff,
	}
	report, err := eng.Run(context.Background(), "q")
	if err == nil {
		t.Fatal("want error from empty synthesis")
	}
	if !strings.Contains(err.Error(), "synthesize") {
		t.Errorf("error %v should mention synthesize", err)
	}
	if report == nil || report.Synthesis != "" {
		t.Errorf("synthesis should be empty on failure; got %q", report.Synthesis)
	}
}
