package research_test

import (
	"context"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

func TestVerify_CitationAuditAlwaysRuns(t *testing.T) {
	// Build a pipeline whose synthesis is rich enough for the
	// auditor to find both citations and claims. The auditor is
	// pure-Go and runs even without a Judge.
	synth := `The first study found 42 affected devices [p1]. According to the second source, the rollout begins in Q2 2026 [p2].`
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"42 devices.","relevance":9}`,
		`{"text":"Rollout Q2 2026.","relevance":8}`,
		synth,
	)
	fs := &fakeSearch{
		defaultResults: []tools.SearchResult{
			{Rank: 1, URL: "https://a.example.com"},
			{Rank: 2, URL: "https://b.example.com"},
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
	report, err := eng.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.Citations == nil {
		t.Fatal("Citations should be populated even without a Judge")
	}
	if report.Citations.ClaimCount == 0 {
		t.Errorf("expected claims to be detected; got %+v", report.Citations)
	}
	// No Judge configured → Verification stays nil + a concern is logged.
	if report.Verification != nil {
		t.Errorf("Verification should be nil without Judge; got %+v", report.Verification)
	}
	sawNoJudgeConcern := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "no separate-provider judge configured") {
			sawNoJudgeConcern = true
		}
	}
	if !sawNoJudgeConcern {
		t.Errorf("expected no-judge concern; got %v", report.Concerns)
	}
}

func TestVerify_PassageValidationAllValid(t *testing.T) {
	// Synthesis cites only p1/p2, both of which the read phase
	// extracts. Expect a populated CitationValidation with no unknowns
	// and no hallucination concern.
	synth := `Devices affected [p1]. Rollout Q2 2026 [p2].`
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"42 devices.","relevance":9}`,
		`{"text":"Rollout Q2 2026.","relevance":8}`,
		synth,
	)
	fs := &fakeSearch{
		defaultResults: []tools.SearchResult{
			{Rank: 1, URL: "https://a.example.com"},
			{Rank: 2, URL: "https://b.example.com"},
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
	report, err := eng.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.CitationValidation == nil {
		t.Fatal("CitationValidation should be populated after verify")
	}
	cv := report.CitationValidation
	if len(cv.Unknown) != 0 {
		t.Errorf("expected no unknown IDs; got %v", cv.Unknown)
	}
	if !cv.OK() {
		t.Errorf("expected OK validation; got %+v", cv)
	}
	for _, c := range report.Concerns {
		if strings.Contains(c, "unknown passage ID") {
			t.Errorf("unexpected hallucination concern: %q", c)
		}
	}
}

func TestVerify_PassageValidationHallucinatedIDConcern(t *testing.T) {
	// Only p1 gets extracted, but the synthesis cites [p99]. Expect the
	// validator to flag p99 as unknown and append a concern naming it.
	synth := `Real claim [p1]. Hallucinated claim [p99].`
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"x","relevance":7}`,
		synth,
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://a.example.com"}}}
	ff := &fakeFetcher{bodies: map[string]string{"https://a.example.com": "alpha"}}
	eng := &research.Engine{
		Provider: prov, Model: "m", SourcesPerQuery: 5,
		Search: fs, Fetcher: ff,
	}
	report, err := eng.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.CitationValidation == nil {
		t.Fatal("CitationValidation should be populated after verify")
	}
	cv := report.CitationValidation
	if cv.OK() {
		t.Fatalf("expected validation to flag hallucinated ID; got %+v", cv)
	}
	wantUnknown := []string{"p99"}
	if len(cv.Unknown) != 1 || cv.Unknown[0] != "p99" {
		t.Errorf("Unknown = %v, want %v", cv.Unknown, wantUnknown)
	}
	found := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "unknown passage ID") && strings.Contains(c, "p99") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected hallucination concern naming p99; got %v", report.Concerns)
	}
}

func TestVerify_JudgePopulatesReport(t *testing.T) {
	synth := `Devices affected [p1]. Rollout Q2 2026 [p2].`
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"42 devices.","relevance":9}`,
		`{"text":"Rollout Q2 2026.","relevance":8}`,
		synth,
	)
	fs := &fakeSearch{
		defaultResults: []tools.SearchResult{
			{Rank: 1, URL: "https://a.example.com"},
			{Rank: 2, URL: "https://b.example.com"},
		},
	}
	ff := &fakeFetcher{
		bodies: map[string]string{
			"https://a.example.com": "alpha",
			"https://b.example.com": "beta",
		},
	}
	judge := staticJudge("openai", `{"score": 8, "concerns": [], "decision": "accept"}`)
	eng := &research.Engine{
		Provider: prov, Model: "m", SourcesPerQuery: 5,
		Search: fs, Fetcher: ff, Judge: judge,
	}
	report, err := eng.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.Verification == nil {
		t.Fatal("Verification should be populated with Judge configured")
	}
	if report.Verification.Decision != agent.VerificationAccept {
		t.Errorf("Decision = %q want accept", report.Verification.Decision)
	}
	if report.Verification.Score != 8 {
		t.Errorf("Score = %d want 8", report.Verification.Score)
	}
}

func TestVerify_JudgeNonAcceptIsConcerned(t *testing.T) {
	synth := `Claim [p1].`
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"x","relevance":7}`,
		synth,
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://a.example.com"}}}
	ff := &fakeFetcher{bodies: map[string]string{"https://a.example.com": "alpha"}}
	judge := staticJudge("openai", `{"score": 4, "concerns": ["needs more sources"], "decision": "needs_revision"}`)
	eng := &research.Engine{
		Provider: prov, Model: "m", SourcesPerQuery: 5,
		Search: fs, Fetcher: ff, Judge: judge,
	}
	report, err := eng.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.Verification == nil || report.Verification.Decision != agent.VerificationNeedsRevision {
		t.Errorf("want needs_revision, got %+v", report.Verification)
	}
	found := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "judge decision=needs_revision") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected judge-decision concern; got %v", report.Concerns)
	}
}
