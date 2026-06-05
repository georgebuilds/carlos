package research_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// --- Fake provider (scripted per-call) -------------------------------------

// scriptedProvider feeds a queue of canned response bodies. Each call
// to Stream pops the next body and emits it as one text delta + a
// stop event. Test setups push responses in the order the engine
// will request them.
type scriptedProvider struct {
	name      string
	mu        sync.Mutex
	responses []string
	calls     atomic.Int64
}

func newScriptedProvider(name string, responses ...string) *scriptedProvider {
	return &scriptedProvider{name: name, responses: append([]string(nil), responses...)}
}

func (p *scriptedProvider) Name() string                           { return p.name }
func (p *scriptedProvider) Capabilities() providers.Capabilities   { return providers.Capabilities{} }
func (p *scriptedProvider) CallCount() int64                       { return p.calls.Load() }

func (p *scriptedProvider) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	p.mu.Lock()
	var body string
	if len(p.responses) > 0 {
		body = p.responses[0]
		p.responses = p.responses[1:]
	}
	p.mu.Unlock()
	p.calls.Add(1)

	ch := make(chan providers.Event, 2)
	go func() {
		defer close(ch)
		select {
		case <-ctx.Done():
			return
		case ch <- providers.Event{Kind: providers.EventTextDelta, Text: body}:
		}
		select {
		case <-ctx.Done():
			return
		case ch <- providers.Event{Kind: providers.EventStopReason, Stop: "end_turn"}:
		}
	}()
	return ch, nil
}

// --- Fake search backend ---------------------------------------------------

type fakeSearch struct {
	mu      sync.Mutex
	calls   int
	results map[string][]tools.SearchResult
	defaultResults []tools.SearchResult
	err     error
}

func (f *fakeSearch) Name() string { return "fake-search" }

func (f *fakeSearch) Search(ctx context.Context, query string, max int) ([]tools.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if rs, ok := f.results[query]; ok {
		if len(rs) > max {
			return rs[:max], nil
		}
		return rs, nil
	}
	if len(f.defaultResults) > max {
		return f.defaultResults[:max], nil
	}
	return f.defaultResults, nil
}

// --- Fake Fetcher ----------------------------------------------------------

type fakeFetcher struct {
	mu       sync.Mutex
	bodies   map[string]string
	titles   map[string]string
	calls    int
	failFor  map[string]error
}

func (f *fakeFetcher) Fetch(ctx context.Context, url string) (research.Source, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err, ok := f.failFor[url]; ok {
		return research.Source{}, err
	}
	body, ok := f.bodies[url]
	if !ok {
		return research.Source{}, errors.New("fake-fetcher: no body for " + url)
	}
	return research.Source{
		URL:     url,
		Title:   f.titles[url],
		Content: body,
	}, nil
}

// --- Helpers ---------------------------------------------------------------

// staticJudge returns a fake provider that always emits the given
// JSON verdict body, mimicking a working LLM judge.
func staticJudge(name, jsonBody string) providers.Provider {
	return fake.New(name, fake.Script{
		{Kind: providers.EventTextDelta, Text: jsonBody},
		{Kind: providers.EventStopReason, Stop: "end_turn"},
	})
}
