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
	"github.com/georgebuilds/carlos/internal/providers/fake"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// More phase tests targeting:
//   - truncateForRead long-body path (via runRead)
//   - pickTopResults len <= n path (via runSearch)
//   - phase_verify skip-no-judge + skip-budget paths
//   - phase_synthesize empty body branch
//   - phase_decompose fallback when no parseable lines
//   - phase_read no-passages-extracted error
//   - phase_fetch all-errors path

// truncateForRead is exercised whenever a source body exceeds the
// internal 32KiB cap. We feed a large body through runRead via Engine.Run.
func TestRunRead_TruncatesLargeBody(t *testing.T) {
	bigBody := strings.Repeat("xyz ", 32*1024) // ~128 KiB, way over cap
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"verify truncation","relevance":8}`, // read s1
		"synthesis using [p1]",
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://huge.example.com"}}}
	ff := &fakeFetcher{bodies: map[string]string{"https://huge.example.com": bigBody}}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 1,
	}
	report, err := eng.Run(context.Background(), "q?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Passages) != 1 {
		t.Errorf("Passages = %d want 1", len(report.Passages))
	}
}

// pickTopResults is hit twice in runSearch: once when results <= n (the
// less-explored arm) and once when results > n. Cover both via small
// SourcesPerQuery.
func TestRunSearch_PicksTopAndDedupes(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub a\nsub b",
		`{"text":"x","relevance":7}`,
		`{"text":"y","relevance":6}`,
		"synthesis [p1] [p2]",
	)
	// Two sub-queries, each returns 2 results; SourcesPerQuery=1 → cap.
	fs := &fakeSearch{
		results: map[string][]tools.SearchResult{
			"sub a": {{Rank: 1, URL: "https://shared.example.com"}, {Rank: 2, URL: "https://only-a.example.com"}},
			"sub b": {{Rank: 1, URL: "https://shared.example.com"}, {Rank: 2, URL: "https://only-b.example.com"}},
		},
	}
	ff := &fakeFetcher{bodies: map[string]string{
		"https://shared.example.com": "shared body",
		"https://only-a.example.com": "a body",
	}}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 1,
	}
	report, err := eng.Run(context.Background(), "q?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Each sub-query picks only its top result, and the shared URL is
	// deduped across sub-queries → 1 source.
	if len(report.Sources) != 1 {
		t.Errorf("Sources = %d want 1 (dedup), got %+v", len(report.Sources), report.Sources)
	}
}

// runVerify branch where the budget is exhausted before the judge call.
func TestRunVerify_BudgetExhaustedSkipsJudge(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",                       // decompose
		`{"text":"x","relevance":7}`, // read
		"synthesis [p1] - claim that requires a citation", // synthesize
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://a.example.com"}}}
	ff := &fakeFetcher{bodies: map[string]string{"https://a.example.com": "alpha"}}
	judge := staticJudge("openai", `{"score":9,"concerns":[],"decision":"accept"}`)
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, Judge: judge,
		Budget:          research.ResearchBudget{MaxProviderCalls: 3}, // just enough for decompose+read+synth, no judge
		SourcesPerQuery: 1,
	}
	report, err := eng.Run(context.Background(), "q?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Verification != nil {
		t.Errorf("Verification should be skipped on budget exhaust, got %+v", report.Verification)
	}
	// And a concern was logged.
	sawSkip := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "skipping LLM judge") {
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Errorf("expected skip-LLM-judge concern, got %v", report.Concerns)
	}
}

// runVerify branch where the judge returns a non-accept verdict so the
// concerns slice gets a "judge decision=..." entry.
func TestRunVerify_JudgeRejectsRecordsConcern(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"x","relevance":7}`,
		"synthesis [p1]",
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://a.example.com"}}}
	ff := &fakeFetcher{bodies: map[string]string{"https://a.example.com": "alpha"}}
	judge := staticJudge("openai", `{"score":3,"concerns":["weak"],"decision":"reject"}`)
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, Judge: judge,
		SourcesPerQuery: 1,
	}
	report, err := eng.Run(context.Background(), "q?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Verification == nil {
		t.Fatal("Verification nil; want populated with reject")
	}
	if report.Verification.Decision != agent.VerificationReject {
		t.Errorf("Decision = %q want reject", report.Verification.Decision)
	}
	// Concern recorded.
	saw := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "decision=reject") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected reject concern, got %v", report.Concerns)
	}
}

// Verifier returns an error: phase_verify swallows it as a concern.
func TestRunVerify_JudgeMalformedRecordsConcern(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"x","relevance":7}`,
		"synthesis [p1]",
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://a.example.com"}}}
	ff := &fakeFetcher{bodies: map[string]string{"https://a.example.com": "alpha"}}
	// Judge emits non-JSON; Verifier returns ErrMalformedJudgeResponse.
	judge := staticJudge("openai", "this is not json at all")
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, Judge: judge,
		SourcesPerQuery: 1,
	}
	report, err := eng.Run(context.Background(), "q?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	saw := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "judge failed") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected judge-failed concern, got %v", report.Concerns)
	}
}

// runSynthesize: empty body → concern + err.
func TestRunSynthesize_EmptyBodyErrors(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"x","relevance":7}`,
		"   ", // whitespace only - synthesize returns empty
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://a.example.com"}}}
	ff := &fakeFetcher{bodies: map[string]string{"https://a.example.com": "alpha"}}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 1,
	}
	_, err := eng.Run(context.Background(), "q?")
	if err == nil || !strings.Contains(err.Error(), "synthesize") {
		t.Errorf("expected synthesize failure, got %v", err)
	}
}

// runRead: zero parseable lines → no passages → error.
func TestRunRead_NoPassagesErrors(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		"this is not json", // read returns no passages
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://a.example.com"}}}
	ff := &fakeFetcher{bodies: map[string]string{"https://a.example.com": "alpha"}}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 1,
	}
	_, err := eng.Run(context.Background(), "q?")
	if err == nil || !strings.Contains(err.Error(), "read") {
		t.Errorf("expected read failure, got %v", err)
	}
}

// runDecompose: model emits zero lines → falls back to using the
// original question as a single sub-query.
func TestRunDecompose_FallbackOnEmpty(t *testing.T) {
	prov := newScriptedProvider("p1",
		"",                           // decompose returns nothing parseable
		`{"text":"x","relevance":7}`, // read s1
		"synth body [p1]",
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://a.example.com"}}}
	ff := &fakeFetcher{bodies: map[string]string{"https://a.example.com": "alpha"}}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 1,
	}
	report, err := eng.Run(context.Background(), "what is q?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Query.Sub) != 1 || report.Query.Sub[0] != "what is q?" {
		t.Errorf("expected fallback sub = original question, got %v", report.Query.Sub)
	}
	// Fallback concern surfaced.
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

// runFetch: all sources error → "no sources fetched".
func TestRunFetch_AllErrorsAborts(t *testing.T) {
	prov := newScriptedProvider("p1", "sub1")
	fs := &fakeSearch{defaultResults: []tools.SearchResult{
		{Rank: 1, URL: "https://a.example.com"},
		{Rank: 2, URL: "https://b.example.com"},
	}}
	ff := &fakeFetcher{
		bodies:  map[string]string{},
		failFor: map[string]error{}, // every URL is unknown → fetcher errors
	}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 2,
	}
	_, err := eng.Run(context.Background(), "q?")
	if err == nil || !strings.Contains(err.Error(), "fetch") {
		t.Errorf("expected fetch failure, got %v", err)
	}
}

// emitTransition error: when Append fails inside SpawnResearch's running
// transition. Forces the runResearchSession to bail out early and
// transition to failed instead.
func TestSpawnResearch_AppendRunningFailsTransitionsToFailed(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))

	// Fail the SECOND state_change append (the running transition).
	// We achieve this by a custom log that counts appends.
	log := &countingFailLog{
		failOnNthStateChange: 2,
		failErr:              errors.New("append disk full"),
	}
	eng := happyEngine(t)

	_, doneCh, err := research.SpawnResearch(context.Background(), log, eng, "q")
	if err != nil {
		t.Fatalf("SpawnResearch: %v", err)
	}
	res := drainResearch(t, doneCh, 5*time.Second)
	if res.Err == nil {
		t.Fatal("expected non-nil Err on append failure")
	}
}

// countingFailLog is a research.ResearchLog that fails the Nth append of
// EvtStateChange while letting others succeed.
type countingFailLog struct {
	mu                   sync.Mutex
	stateChangeCount     int
	failOnNthStateChange int
	failErr              error
	events               []agent.Event
	rows                 []agent.AgentRow
	artifacts            []agent.Artifact
}

func (l *countingFailLog) Append(_ context.Context, ev agent.Event) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ev.Type == agent.EvtStateChange {
		l.stateChangeCount++
		if l.stateChangeCount == l.failOnNthStateChange {
			return 0, l.failErr
		}
	}
	l.events = append(l.events, ev)
	return int64(len(l.events)), nil
}

func (l *countingFailLog) InsertAgent(_ context.Context, r agent.AgentRow) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rows = append(l.rows, r)
	return nil
}

func (l *countingFailLog) InsertArtifact(_ context.Context, a agent.Artifact) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.artifacts = append(l.artifacts, a)
	return nil
}

// newResearchAgentID determinism / collision behavior.
func TestSpawnResearch_RapidFireProducesDistinctIDs(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	log := &recordingLog{}
	eng := happyEngine(t)
	eng2 := happyEngine(t)
	id1, ch1, err := research.SpawnResearch(context.Background(), log, eng, "q1")
	if err != nil {
		t.Fatal(err)
	}
	id2, ch2, err := research.SpawnResearch(context.Background(), log, eng2, "q2")
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Errorf("collision: %q == %q", id1, id2)
	}
	drainResearch(t, ch1, 5*time.Second)
	drainResearch(t, ch2, 5*time.Second)
}

// Engine.Fetch error path through callProvider: provider Stream
// returns an error, callProvider wraps it.
type errStreamProvider struct {
	err error
}

func (p *errStreamProvider) Name() string                         { return "errstream" }
func (p *errStreamProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (p *errStreamProvider) Stream(_ context.Context, _ providers.Request) (<-chan providers.Event, error) {
	return nil, p.err
}

func TestEngine_ProviderStreamErrorBubblesUp(t *testing.T) {
	prov := &errStreamProvider{err: errors.New("provider down")}
	fs := &fakeSearch{}
	ff := &fakeFetcher{}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff,
	}
	_, err := eng.Run(context.Background(), "q")
	if err == nil || !strings.Contains(err.Error(), "decompose") {
		t.Errorf("expected decompose failure with provider err, got %v", err)
	}
}

// callProvider's EventError branch: provider yields an EventError mid-stream.
type errEventProvider struct {
	err error
}

func (p *errEventProvider) Name() string                         { return "errev" }
func (p *errEventProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (p *errEventProvider) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	ch := make(chan providers.Event, 2)
	ch <- providers.Event{Kind: providers.EventTextDelta, Text: "partial"}
	ch <- providers.Event{Kind: providers.EventError, Err: p.err}
	close(ch)
	return ch, nil
}

func TestEngine_StreamYieldsEventErrorAborts(t *testing.T) {
	prov := &errEventProvider{err: errors.New("stream blew up")}
	fs := &fakeSearch{}
	ff := &fakeFetcher{}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff,
	}
	_, err := eng.Run(context.Background(), "q")
	if err == nil {
		t.Fatal("expected error from event-error provider")
	}
}

var _ providers.Provider = (*errStreamProvider)(nil)
var _ providers.Provider = (*errEventProvider)(nil)

// fake.Provider compile-time sanity check (we use fake.New in
// staticJudge helpers elsewhere).
var _ providers.Provider = fake.New("x", nil)
