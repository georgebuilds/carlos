package research_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// End-to-end: all six phases run; the Report carries decompose +
// search + fetch + read + synthesize + verify state, citation
// references reach the audit, and budget counters track.
func TestEngine_EndToEnd_AllPhases(t *testing.T) {
	prov := newScriptedProvider("p1",
		"How widely is WebGPU enabled in Safari?\nWhat versions ship with WebGPU support?",           // decompose
		`{"text":"Safari TP supports WebGPU as of 2025.","relevance":9}`,                             // read s1
		`{"text":"Safari stable shipped behind a flag in 2026.","relevance":8}`,                      // read s2
		"Safari now supports WebGPU in technology preview [p1] and rolled into stable in 2026 [p2].", // synthesis
	)
	fs := &fakeSearch{
		defaultResults: []tools.SearchResult{
			{Rank: 1, Title: "WebKit Blog", URL: "https://webkit.org/blog/x"},
			{Rank: 2, Title: "MDN compat", URL: "https://developer.mozilla.org/x"},
		},
	}
	ff := &fakeFetcher{
		bodies: map[string]string{
			"https://webkit.org/blog/x":       "WebKit blog body",
			"https://developer.mozilla.org/x": "MDN body",
		},
		titles: map[string]string{
			"https://webkit.org/blog/x":       "WebKit Blog",
			"https://developer.mozilla.org/x": "MDN",
		},
	}
	judge := staticJudge("openai", `{"score": 9, "concerns": [], "decision": "accept"}`)
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, Judge: judge,
		SourcesPerQuery: 5,
	}
	report, err := eng.Run(context.Background(), "How widely is WebGPU supported in Safari?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Decompose populated.
	if len(report.Query.Sub) != 2 {
		t.Errorf("Sub queries = %d want 2 (%v)", len(report.Query.Sub), report.Query.Sub)
	}
	// Two unique URLs → two sources.
	if len(report.Sources) != 2 {
		t.Errorf("Sources = %d want 2", len(report.Sources))
	}
	// Stable IDs.
	if report.Sources[0].ID != "s1" || report.Sources[1].ID != "s2" {
		t.Errorf("source IDs not s1/s2: %v", report.Sources)
	}
	// Two passages, IDs p1+p2 in order.
	if len(report.Passages) != 2 {
		t.Errorf("Passages = %d want 2", len(report.Passages))
	}
	if report.Passages[0].ID != "p1" || report.Passages[1].ID != "p2" {
		t.Errorf("passage IDs not p1/p2: %v", report.Passages)
	}
	// Passage → Source back-reference is intact.
	if report.Passages[0].SourceID != "s1" || report.Passages[1].SourceID != "s2" {
		t.Errorf("passage SourceID not s1/s2: %+v", report.Passages)
	}
	// Synthesis cites [p1] and [p2].
	if !strings.Contains(report.Synthesis, "[p1]") || !strings.Contains(report.Synthesis, "[p2]") {
		t.Errorf("synthesis missing citations: %q", report.Synthesis)
	}
	// Citation audit ran. NOTE: the existing agent.CitationAuditor
	// only recognizes URLs, /-rooted paths, and [\d+] numeric refs as
	// citations. Our synthesis prompt uses [pN] style, which the
	// auditor does NOT count as a citation today — this is a known
	// gap noted in the slice 11c follow-ups. We assert the audit ran
	// (non-nil Citations field) and surfaces claim+coverage data,
	// not that it recognized [pN] as citation tokens.
	if report.Citations == nil {
		t.Fatal("Citations nil")
	}
	// Verifier ran and accepted.
	if report.Verification == nil {
		t.Fatal("Verification nil")
	}
	if report.Verification.Decision != "accept" {
		t.Errorf("Verifier decision = %q", report.Verification.Decision)
	}
	// Budget accounted for: 1 decompose + 2 read + 1 synth + 1 judge = 5 calls.
	if report.Budget.ProviderCalls != 5 {
		t.Errorf("ProviderCalls = %d want 5", report.Budget.ProviderCalls)
	}
	totalBytes := int64(len("WebKit blog body") + len("MDN body"))
	if report.Budget.FetchedBytes != totalBytes {
		t.Errorf("FetchedBytes = %d want %d", report.Budget.FetchedBytes, totalBytes)
	}
	if report.Budget.Elapsed <= 0 {
		t.Errorf("Elapsed not recorded: %v", report.Budget.Elapsed)
	}
}

func TestEngine_BudgetExceeded_ProviderCallCap(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1\nsub2",
		`{"text":"x","relevance":7}`,
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://a.example.com"}}}
	ff := &fakeFetcher{bodies: map[string]string{"https://a.example.com": "alpha"}}
	eng := &research.Engine{
		Provider: prov, Model: "m", SourcesPerQuery: 2,
		Search: fs, Fetcher: ff,
		Budget: research.ResearchBudget{MaxProviderCalls: 1}, // only decompose may run
	}
	report, err := eng.Run(context.Background(), "q")
	if err == nil {
		t.Fatal("expected ErrBudgetExceeded")
	}
	if !errors.Is(err, research.ErrBudgetExceeded) {
		t.Errorf("error = %v want ErrBudgetExceeded", err)
	}
	if report == nil {
		t.Fatal("partial report should be non-nil")
	}
	// Decompose succeeded (one call); read tried and bounced.
	if report.Budget.ProviderCalls != 1 {
		t.Errorf("ProviderCalls = %d want 1", report.Budget.ProviderCalls)
	}
	// Concern recorded.
	saw := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "provider-call budget exhausted") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected budget-exhausted concern; got %v", report.Concerns)
	}
}

func TestEngine_BudgetExceeded_FetchByteCap(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
	)
	bodies := map[string]string{
		"https://a.example.com": "AAAAAAAAAAAA", // 12 bytes
		"https://b.example.com": "BBBBBBBBBBBB", // 12 bytes
		"https://c.example.com": "CCCCCCCCCCCC",
	}
	urls := []tools.SearchResult{}
	for u := range bodies {
		urls = append(urls, tools.SearchResult{Rank: len(urls) + 1, URL: u})
	}
	fs := &fakeSearch{defaultResults: urls}
	ff := &fakeFetcher{bodies: bodies}
	eng := &research.Engine{
		Provider: prov, Model: "m", SourcesPerQuery: 10,
		Search: fs, Fetcher: ff,
		Budget: research.ResearchBudget{MaxFetchedBytes: 12}, // exactly one source fits
	}
	report, err := eng.Run(context.Background(), "q")
	if !errors.Is(err, research.ErrBudgetExceeded) {
		t.Fatalf("error = %v want ErrBudgetExceeded", err)
	}
	if len(report.Sources) != 1 {
		t.Errorf("Sources = %d want 1 (only first source fits in budget)", len(report.Sources))
	}
	if report.Budget.FetchedBytes != 12 {
		t.Errorf("FetchedBytes = %d want 12", report.Budget.FetchedBytes)
	}
}

func TestEngine_CancellationMidDecompose(t *testing.T) {
	// A provider that blocks on ctx.Done() simulates a long-running
	// stream that the canceller has to unblock.
	prov := &blockingProvider{name: "blocker"}
	fs := &fakeSearch{}
	ff := &fakeFetcher{bodies: map[string]string{}}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff,
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel almost immediately.
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	report, err := eng.Run(ctx, "q")
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v want context.Canceled", err)
	}
	if report == nil {
		t.Fatal("report should be non-nil on cancellation")
	}
}

func TestEngine_RejectsMissingDependencies(t *testing.T) {
	cases := []struct {
		name string
		eng  *research.Engine
		want string
	}{
		{"nil provider", &research.Engine{Search: &fakeSearch{}, Fetcher: &fakeFetcher{}}, "nil provider"},
		{"nil search", &research.Engine{Provider: newScriptedProvider("p"), Fetcher: &fakeFetcher{}}, "nil search"},
		{"nil fetcher", &research.Engine{Provider: newScriptedProvider("p"), Search: &fakeSearch{}}, "nil fetcher"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.eng.Run(context.Background(), "q")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v want substring %q", err, tc.want)
			}
		})
	}
}

func TestEngine_RejectsEmptyQuestion(t *testing.T) {
	eng := &research.Engine{
		Provider: newScriptedProvider("p"), Search: &fakeSearch{}, Fetcher: &fakeFetcher{},
	}
	_, err := eng.Run(context.Background(), "    ")
	if err == nil || !strings.Contains(err.Error(), "empty question") {
		t.Errorf("err = %v want empty question", err)
	}
}

// Slice 11d: phase callbacks fire once per phase, in order, with
// elapsed times that monotonically advance from start to done.
func TestEngine_OnPhaseStart_FiresOncePerPhase(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1\nsub2",
		`{"text":"x","relevance":7}`, // read s1
		`{"text":"y","relevance":6}`, // read s2
		"synth body [p1] [p2]",
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{
		{Rank: 1, URL: "https://a.example.com"},
		{Rank: 2, URL: "https://b.example.com"},
	}}
	ff := &fakeFetcher{bodies: map[string]string{
		"https://a.example.com": "alpha",
		"https://b.example.com": "beta",
	}}

	var startMu sync.Mutex
	var starts []string
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 2,
		OnPhaseStart: func(phase string) {
			startMu.Lock()
			defer startMu.Unlock()
			starts = append(starts, phase)
		},
	}
	if _, err := eng.Run(context.Background(), "q"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"decompose", "route", "search", "fetch", "read", "synthesize", "verify"}
	startMu.Lock()
	got := append([]string(nil), starts...)
	startMu.Unlock()
	if !equalStringSlice(got, want) {
		t.Errorf("OnPhaseStart sequence = %v want %v", got, want)
	}
}

func TestEngine_OnPhaseDone_FiresWithNilErrOnSuccess(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"x","relevance":7}`,
		"synth body [p1]",
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://a.example.com"}}}
	ff := &fakeFetcher{bodies: map[string]string{"https://a.example.com": "alpha"}}

	type done struct {
		phase string
		err   error
	}
	var mu sync.Mutex
	var dones []done
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 1,
		OnPhaseDone: func(phase string, _ time.Duration, err error) {
			mu.Lock()
			defer mu.Unlock()
			dones = append(dones, done{phase, err})
		},
	}
	if _, err := eng.Run(context.Background(), "q"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dones) != 7 {
		t.Fatalf("OnPhaseDone fired %d times want 7: %+v", len(dones), dones)
	}
	for _, d := range dones {
		if d.err != nil {
			t.Errorf("phase %s done err = %v want nil", d.phase, d.err)
		}
	}
}

// When a phase fails, its OnPhaseDone callback gets the non-nil err
// (and the *next* phase's OnPhaseStart is NOT called because Engine.Run
// aborts on the first failure).
func TestEngine_OnPhaseDone_FiresWithErrOnFailure(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
	)
	fs := &fakeSearch{err: errors.New("search backend down")}
	ff := &fakeFetcher{}

	type done struct {
		phase string
		err   error
	}
	var mu sync.Mutex
	var dones []done
	var starts []string
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff,
		OnPhaseStart: func(phase string) {
			mu.Lock()
			defer mu.Unlock()
			starts = append(starts, phase)
		},
		OnPhaseDone: func(phase string, _ time.Duration, err error) {
			mu.Lock()
			defer mu.Unlock()
			dones = append(dones, done{phase, err})
		},
	}
	if _, err := eng.Run(context.Background(), "q"); err == nil {
		t.Fatal("expected search failure")
	}
	mu.Lock()
	defer mu.Unlock()
	// decompose ran to completion (one start, one done with err=nil).
	// route ran to completion (one start, one done with err=nil; soft-fails
	//   to default plan when there's no MultiBackend to consult).
	// search ran and failed (one start, one done with err!=nil).
	// fetch and beyond never started.
	want := []string{"decompose", "route", "search"}
	if !equalStringSlice(starts, want) {
		t.Errorf("starts = %v want %v", starts, want)
	}
	if len(dones) != 3 {
		t.Fatalf("dones = %d want 3: %+v", len(dones), dones)
	}
	if dones[0].err != nil {
		t.Errorf("decompose done err = %v want nil", dones[0].err)
	}
	if dones[1].err != nil {
		t.Errorf("route done err = %v want nil (route soft-fails to default)", dones[1].err)
	}
	if dones[2].err == nil {
		t.Errorf("search done err = nil want non-nil")
	}
}

// Nil callbacks behave exactly like the pre-11d engine — sanity check
// that the additive hook didn't change happy-path observable behavior.
// (The existing TestEngine_EndToEnd_AllPhases also exercises this; we
// keep this here as a targeted, faster-running guard.)
func TestEngine_NilCallbacks_NoOp(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"x","relevance":7}`,
		"synth body [p1]",
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://a.example.com"}}}
	ff := &fakeFetcher{bodies: map[string]string{"https://a.example.com": "alpha"}}

	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 1,
		// OnPhaseStart / OnPhaseDone deliberately left nil.
	}
	report, err := eng.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("Run with nil callbacks: %v", err)
	}
	if report.Synthesis == "" {
		t.Fatal("synthesis empty with nil callbacks")
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// blockingProvider blocks forever (until ctx cancels). Used to
// simulate a long-running LLM stream so we can test cancellation
// without timing fragility.
type blockingProvider struct {
	name string
}

func (b *blockingProvider) Name() string                         { return b.name }
func (b *blockingProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (b *blockingProvider) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	ch := make(chan providers.Event)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

var _ providers.Provider = (*blockingProvider)(nil)
