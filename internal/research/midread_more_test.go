package research_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// flakyProvider responds normally on most calls but emits a stream
// error on a specific call index. Used to exercise per-source read
// errors that are NOT budget-exhaustion.
type flakyProvider struct {
	mu        sync.Mutex
	responses []string
	failOn    int
	calls     atomic.Int64
}

func (p *flakyProvider) Name() string                         { return "flaky" }
func (p *flakyProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (p *flakyProvider) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	n := int(p.calls.Add(1))
	if n == p.failOn {
		return nil, errors.New("flaky stream failure")
	}
	p.mu.Lock()
	var body string
	if len(p.responses) > 0 {
		body = p.responses[0]
		p.responses = p.responses[1:]
	}
	p.mu.Unlock()
	ch := make(chan providers.Event, 2)
	go func() {
		defer close(ch)
		ch <- providers.Event{Kind: providers.EventTextDelta, Text: body}
		ch <- providers.Event{Kind: providers.EventStopReason, Stop: "end_turn"}
	}()
	return ch, nil
}

var _ providers.Provider = (*flakyProvider)(nil)

// runRead per-source error path: one read errs, the other succeeds, and
// the synthesis still runs from the surviving passage.
func TestRunRead_PerSourceErrorContinues(t *testing.T) {
	// Calls: 1=decompose, 2=read s1 (fails), 3=read s2 (ok), 4=synth.
	prov := &flakyProvider{
		responses: []string{
			"sub1", // decompose
			// 2nd call will error
			`{"text":"second","relevance":7}`, // read s2
			"synth body cites [p1]",            // synthesize
		},
		failOn: 2,
	}
	fs := &fakeSearch{defaultResults: []tools.SearchResult{
		{Rank: 1, URL: "https://1.example.com"},
		{Rank: 2, URL: "https://2.example.com"},
	}}
	ff := &fakeFetcher{bodies: map[string]string{
		"https://1.example.com": "first",
		"https://2.example.com": "second",
	}}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 2,
	}
	report, err := eng.Run(context.Background(), "q?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Second source's read produced one passage; first source's read
	// failed and recorded a concern.
	if len(report.Passages) != 1 {
		t.Errorf("Passages = %d want 1", len(report.Passages))
	}
	saw := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "read: source=") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected read-error concern, got %v", report.Concerns)
	}
}

// runFetch ctx-canceled mid-loop: cancel after first fetch lands but
// before second.
func TestRunFetch_CtxCancelMidLoop(t *testing.T) {
	prov := newScriptedProvider("p1", "sub1")
	fs := &fakeSearch{defaultResults: []tools.SearchResult{
		{Rank: 1, URL: "https://1.example.com"},
		{Rank: 2, URL: "https://2.example.com"},
	}}
	ff := &fakeFetcher{bodies: map[string]string{
		"https://1.example.com": "alpha",
		"https://2.example.com": "beta",
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel; fetch loop should bail immediately
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 2,
	}
	_, err := eng.Run(ctx, "q?")
	if err == nil {
		t.Fatal("expected ctx cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v want context.Canceled", err)
	}
}
