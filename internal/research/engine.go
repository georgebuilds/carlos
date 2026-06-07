package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// Engine wires the six research phases. Construct one per Run; the
// per-Run state lives in the *Report the engine threads through each
// phase. The engine itself holds the configuration and the
// dependencies it injects into phases.
type Engine struct {
	Provider providers.Provider  // LLM for plan / read / synthesize
	Model    string              // model id passed to Provider
	Search   tools.SearchBackend // search backend (Brave / SearXNG / DuckDuckGo)
	Fetcher  Fetcher             // source fetcher; nil → adapter over a default WebFetchTool
	Judge    providers.Provider  // optional cross-provider judge; nil → skip verify pass

	Budget          ResearchBudget
	MaxSubQueries   int // default DefaultMaxSubQueries
	SourcesPerQuery int // default DefaultSourcesPerQuery

	// OnPhaseStart is invoked at the start of each phase. If nil,
	// no-op. Used by SpawnResearch (slice 11d) to emit
	// EvtResearchPhase events without coupling the engine to the
	// agent eventlog. Callbacks must not mutate engine state; treat
	// the engine as read-only from inside them.
	OnPhaseStart func(phase string)
	// OnPhaseDone is the symmetric callback when a phase completes
	// (success or failure). Failure is signaled by a non-nil err;
	// elapsed is the wall-clock duration of the phase body. Like
	// OnPhaseStart, callbacks must not mutate engine state.
	OnPhaseDone func(phase string, elapsed time.Duration, err error)
}

// beginPhase invokes OnPhaseStart (if set) and returns a start
// timestamp the caller pairs with endPhase. Tiny helper so each
// phase_*.go file only grows a two-line decoration around its body.
func (e *Engine) beginPhase(name string) time.Time {
	if e.OnPhaseStart != nil {
		e.OnPhaseStart(name)
	}
	return time.Now()
}

// endPhase invokes OnPhaseDone (if set) with the elapsed time. The
// err argument carries the phase's terminal error (nil on success);
// pass it through unchanged so the callback sees the same value the
// engine.Run loop is about to classify.
func (e *Engine) endPhase(name string, started time.Time, err error) {
	if e.OnPhaseDone != nil {
		e.OnPhaseDone(name, time.Since(started), err)
	}
}

// Fetcher is the seam the engine uses to load source bodies. The
// production implementation wraps *tools.WebFetchTool; tests inject
// a fake. Defining the seam in this package (rather than tools) keeps
// the engine's surface contract narrow and lets tests avoid every
// HTTP-layer concern.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (Source, error)
}

// WebFetchAdapter adapts *tools.WebFetchTool to the Fetcher seam. It
// invokes WebFetchTool.Execute with a {"url": …} payload, parses the
// JSON envelope (the same shape the model sees), and maps it onto a
// Source. The adapter is the only place the engine talks to the tool's
// JSON encoding - everything downstream consumes Source values.
type WebFetchAdapter struct {
	Tool *tools.WebFetchTool

	// RespectRobots, when non-nil, overrides the tool's default
	// (true). `carlos research` sets this to false because the user
	// running the command IS the explicit consent the polite-bot
	// default is gating on; without this every Yelp / Wikipedia /
	// news fetch fails with "robots.txt disallows" and the engine
	// has nothing to read in the read phase.
	RespectRobots *bool
}

// Fetch implements Fetcher.
func (a *WebFetchAdapter) Fetch(ctx context.Context, url string) (Source, error) {
	if a == nil || a.Tool == nil {
		return Source{}, errors.New("research: WebFetchAdapter.Tool is nil")
	}
	input := map[string]any{"url": url}
	if a.RespectRobots != nil {
		input["respect_robots"] = *a.RespectRobots
	}
	in, err := json.Marshal(input)
	if err != nil {
		return Source{}, fmt.Errorf("research: marshal fetch input: %w", err)
	}
	raw, err := a.Tool.Execute(ctx, in)
	if err != nil {
		return Source{}, err
	}
	var out struct {
		URL       string `json:"url"`
		FinalURL  string `json:"final_url"`
		Title     string `json:"title"`
		Content   string `json:"content"`
		FetchedAt string `json:"fetched_at"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Source{}, fmt.Errorf("research: parse fetch result: %w", err)
	}
	ts, _ := time.Parse(time.RFC3339, out.FetchedAt)
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	finalURL := out.FinalURL
	if finalURL == "" {
		finalURL = out.URL
	}
	return Source{
		URL:       finalURL,
		Title:     out.Title,
		Content:   out.Content,
		FetchedAt: ts,
	}, nil
}

// ErrBudgetExceeded is returned when a phase refuses to proceed because
// a ResearchBudget cap was hit. The engine treats this as a graceful
// abort: it records the cause in Report.Concerns and returns the
// partial Report along with the wrapped error.
var ErrBudgetExceeded = errors.New("research: budget exceeded")

// Run orchestrates the six phases. The Report is always non-nil on
// return; even on abort the caller can inspect partial state.
//
// Phase order is fixed: decompose → search → fetch → read →
// synthesize → verify. No phase is skippable; the engine fails-loud
// if a phase is asked to consume empty input from the prior phase
// (e.g. fetch with no search results), surfacing the gap in Concerns
// rather than silently producing an empty Report.
//
// ctx cancellation propagates: an in-flight provider stream or HTTP
// fetch will see the canceled ctx and unwind cleanly. The engine
// returns a partial Report + ctx.Err().
func (e *Engine) Run(ctx context.Context, question string) (*Report, error) {
	if e == nil {
		return nil, errors.New("research: nil engine")
	}
	if strings.TrimSpace(question) == "" {
		return nil, errors.New("research: empty question")
	}
	if e.Provider == nil {
		return nil, errors.New("research: nil provider")
	}
	if e.Search == nil {
		return nil, errors.New("research: nil search backend")
	}
	if e.Fetcher == nil {
		return nil, errors.New("research: nil fetcher")
	}

	// Defaults.
	if e.MaxSubQueries <= 0 {
		e.MaxSubQueries = DefaultMaxSubQueries
	}
	if e.SourcesPerQuery <= 0 {
		e.SourcesPerQuery = DefaultSourcesPerQuery
	}
	if e.Budget.MaxProviderCalls <= 0 {
		e.Budget.MaxProviderCalls = DefaultMaxProviderCalls
	}
	if e.Budget.MaxFetchedBytes <= 0 {
		e.Budget.MaxFetchedBytes = DefaultMaxFetchedBytes
	}
	if e.Budget.MaxWallClock <= 0 {
		e.Budget.MaxWallClock = DefaultMaxWallClock
	}

	report := &Report{
		Question: question,
		Query:    Query{Question: question},
	}

	// Wall-clock timer wraps the entire run. The deadline applies to
	// every phase; a slow synthesize that pushes past the deadline
	// will see ctx.Done() and unwind.
	start := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, e.Budget.MaxWallClock)
	defer cancel()
	defer func() {
		report.Budget.Elapsed = time.Since(start)
	}()

	type phase struct {
		name string
		fn   func(context.Context, *Report) error
	}
	phases := []phase{
		{"decompose", e.runDecompose},
		{"search", e.runSearch},
		{"fetch", e.runFetch},
		{"read", e.runRead},
		{"synthesize", e.runSynthesize},
		{"verify", e.runVerify},
	}

	for _, p := range phases {
		if err := runCtx.Err(); err != nil {
			report.Concerns = append(report.Concerns,
				fmt.Sprintf("phase=%s aborted: %v", p.name, err))
			return report, err
		}
		if err := p.fn(runCtx, report); err != nil {
			// Budget-exceeded is treated as a graceful abort: the
			// concern is already recorded by the phase that tripped
			// it; we return the partial Report + wrapped error so
			// the caller can distinguish "ran out of room" from a
			// real failure.
			if errors.Is(err, ErrBudgetExceeded) {
				return report, err
			}
			// Cancellation: propagate cleanly, partial state intact.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				report.Concerns = append(report.Concerns,
					fmt.Sprintf("phase=%s aborted: %v", p.name, err))
				return report, err
			}
			report.Concerns = append(report.Concerns,
				fmt.Sprintf("phase=%s failed: %v", p.name, err))
			return report, fmt.Errorf("research: phase=%s: %w", p.name, err)
		}
	}

	return report, nil
}

// callProvider is the shared single-shot LLM helper every phase uses.
// It assembles a Request, streams it through e.Provider, collects the
// text, and increments the budget. On budget exceedance it returns
// ErrBudgetExceeded WITHOUT making the call so a runaway phase can
// never overshoot by even one call.
func (e *Engine) callProvider(ctx context.Context, report *Report, system, user string) (string, error) {
	if report.Budget.ProviderCalls >= e.Budget.MaxProviderCalls {
		report.Concerns = append(report.Concerns,
			fmt.Sprintf("provider-call budget exhausted at %d calls", report.Budget.ProviderCalls))
		return "", ErrBudgetExceeded
	}
	req := providers.Request{
		Model:  e.Model,
		System: system,
		Messages: []providers.Message{{
			Role: "user",
			Content: []providers.Block{{
				Kind: "text",
				Text: user,
			}},
		}},
	}
	stream, err := e.Provider.Stream(ctx, req)
	if err != nil {
		return "", fmt.Errorf("provider stream: %w", err)
	}
	// Charge the call regardless of stream outcome - even an errored
	// stream cost a provider round-trip.
	report.Budget.ProviderCalls++

	var buf strings.Builder
	for ev := range stream {
		switch ev.Kind {
		case providers.EventTextDelta:
			buf.WriteString(ev.Text)
		case providers.EventError:
			if ev.Err != nil {
				return buf.String(), fmt.Errorf("provider error: %w", ev.Err)
			}
		}
	}
	return buf.String(), nil
}

// auditCitations runs CitationAuditor over the synthesis body. Pulled
// out so the verify phase can call it directly without reaching
// across files for an exported helper.
func auditCitations(body string) agent.Audit {
	return (&agent.CitationAuditor{}).Audit([]byte(body))
}
