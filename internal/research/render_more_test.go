package research_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// RenderMarkdown branches: covers verifier section, concerns section,
// nil citations/verification, and the empty-title "(untitled)" fallback.

func TestRenderMarkdown_AllSections(t *testing.T) {
	r := &research.Report{
		Question: "Q?",
		Query:    research.Query{Sub: []string{"a", "b"}},
		Sources: []research.Source{
			{ID: "s1", URL: "https://x.test", Title: ""}, // untitled
		},
		Passages: []research.Passage{
			{ID: "p1", SourceID: "s1", Text: "passage one", Relevance: 8},
		},
		Synthesis: "synthesis [p1]",
		Citations: &agent.Audit{ClaimCount: 3, Score: 0.66, Unsupported: []string{"u1"}},
		Verification: &agent.VerificationReport{
			Decision:   agent.VerificationNeedsRevision,
			Score:      5,
			JudgeModel: "openai:gpt-5",
			Concerns:   []string{"weak", "shaky"},
		},
		Concerns: []string{"engine concern A", "engine concern B"},
	}
	out := research.RenderMarkdown(r)
	for _, want := range []string{
		"# Research report: Q?",
		"## Sub-queries", "- a", "- b",
		"## Synthesis", "synthesis [p1]",
		"## Sources", "(untitled)",
		"## Passages", "**[p1]**", "relevance 8",
		"## Citation audit", "claims: 3", "coverage score: 0.66", "unsupported: 1",
		"## Verifier", "decision: needs_revision", "score: 5", "judge: openai:gpt-5",
		"- concerns:", "- weak", "- shaky",
		"## Engine concerns", "engine concern A", "engine concern B",
		"## Budget",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// pickTopResults > n branch: feed 5 results with SourcesPerQuery=3 so the
// slice cap fires.
func TestRunSearch_TrimsExcessResults(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"x","relevance":7}`,
		`{"text":"y","relevance":6}`,
		`{"text":"z","relevance":5}`,
		"synth body [p1] [p2] [p3]",
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{
		{Rank: 1, URL: "https://1.example.com"},
		{Rank: 2, URL: "https://2.example.com"},
		{Rank: 3, URL: "https://3.example.com"},
		{Rank: 4, URL: "https://4.example.com"},
		{Rank: 5, URL: "https://5.example.com"},
	}}
	ff := &fakeFetcher{bodies: map[string]string{
		"https://1.example.com": "a",
		"https://2.example.com": "b",
		"https://3.example.com": "c",
		// 4, 5 are never fetched since search caps at 3.
	}}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 3,
	}
	report, err := eng.Run(context.Background(), "q?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Sources) != 3 {
		t.Errorf("Sources = %d want 3 (capped)", len(report.Sources))
	}
}

// emitResearchPhase and emitArtifactRef are best-effort. To hit their
// append-error branches, plug a log that returns an error from Append.
// We exercise this via runResearchSession in the happy path; on failure
// the error is silently swallowed (per the contract).
type appendErrorLog struct {
	mu         sync.Mutex
	failTypes  map[agent.EventType]bool
	events     []agent.Event
	insertErrs []error
}

func (l *appendErrorLog) Append(_ context.Context, ev agent.Event) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failTypes[ev.Type] {
		return 0, errors.New("append fail")
	}
	l.events = append(l.events, ev)
	return int64(len(l.events)), nil
}

func (l *appendErrorLog) InsertAgent(_ context.Context, _ agent.AgentRow) error { return nil }
func (l *appendErrorLog) InsertArtifact(_ context.Context, _ agent.Artifact) error { return nil }

// emitResearchPhase swallowed-error branch. We fail every research-phase
// event; the session still terminates normally.
func TestSpawnResearch_EmitResearchPhaseAppendFailsSilently(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	log := &appendErrorLog{
		failTypes: map[agent.EventType]bool{agent.EvtResearchPhase: true},
	}
	eng := happyEngine(t)
	_, doneCh, err := research.SpawnResearch(context.Background(), log, eng, "q")
	if err != nil {
		t.Fatalf("SpawnResearch: %v", err)
	}
	res := drainResearch(t, doneCh, 5*time.Second)
	// The engine still succeeds; emitResearchPhase failures are swallowed.
	if res.Err != nil {
		t.Errorf("err should be nil despite phase-emit failures: %v", res.Err)
	}
}

// emitArtifactRef append-fail branch. Same swallow contract.
func TestSpawnResearch_EmitArtifactRefAppendFailsSilently(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	log := &appendErrorLog{
		failTypes: map[agent.EventType]bool{agent.EvtArtifactRef: true},
	}
	eng := happyEngine(t)
	_, doneCh, err := research.SpawnResearch(context.Background(), log, eng, "q")
	if err != nil {
		t.Fatalf("SpawnResearch: %v", err)
	}
	res := drainResearch(t, doneCh, 5*time.Second)
	if res.Err != nil {
		t.Errorf("err should be nil despite artifact-ref emit failure: %v", res.Err)
	}
}

// Fetch tool's Execute returns malformed JSON - exercises the
// fetch-result parse error branch of WebFetchAdapter.Fetch.
type stubMalformedFetchTool struct {
	out []byte
	err error
}

// Stand-in for *tools.WebFetchTool, but WebFetchAdapter is wired to
// the concrete type, so the malformed-JSON path is only hit indirectly.
// We instead exercise the Source.FinalURL fallback by configuring a
// real adapter where the body has no final_url.
// (Skipped because the adapter uses *tools.WebFetchTool concretely; the
// existing engine_adapter_test.go already covers the happy path.)

// runResearchSession: when the engine reports artifact write success but
// emitTransition to done fails. Cover by failing the LAST state-change
// append.
func TestSpawnResearch_DoneTransitionAppendFails(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	// Fail the 3rd state_change (running done is index 3: created, running, done).
	log := &countingFailLog{
		failOnNthStateChange: 3,
		failErr:              errors.New("disk full at done"),
	}
	eng := happyEngine(t)
	_, doneCh, err := research.SpawnResearch(context.Background(), log, eng, "q")
	if err != nil {
		t.Fatalf("SpawnResearch: %v", err)
	}
	res := drainResearch(t, doneCh, 5*time.Second)
	if res.Err == nil {
		t.Fatal("expected non-nil Err when terminal transition fails")
	}
}

// callProvider stream provider that exits cleanly with no events - ensures
// callProvider returns empty body without erroring.
type silentProvider struct{}

func (silentProvider) Name() string                         { return "silent" }
func (silentProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (silentProvider) Stream(_ context.Context, _ providers.Request) (<-chan providers.Event, error) {
	ch := make(chan providers.Event)
	close(ch)
	return ch, nil
}

func TestEngine_EmptyStreamYieldsEmptyDecompose(t *testing.T) {
	eng := &research.Engine{
		Provider: silentProvider{}, Model: "m",
		Search: &fakeSearch{defaultResults: []tools.SearchResult{}}, Fetcher: &fakeFetcher{},
	}
	report, err := eng.Run(context.Background(), "q?")
	// decompose returned empty body → fallback to original question;
	// search returns no results so the run fails.
	if err == nil {
		t.Fatal("expected search failure (no results) after empty-decompose fallback")
	}
	// Verify we got a fallback concern.
	saw := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "falling back") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected fallback concern, got %v", report.Concerns)
	}
}

var _ providers.Provider = (*silentProvider)(nil)
