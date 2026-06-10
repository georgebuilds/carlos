package research

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// =============================================================================
// Test helpers (whitebox — package research)
// =============================================================================

// namedFakeBackend is a no-op SearchBackend used to populate a
// *tools.MultiBackend with arbitrary backend names. The route phase
// never calls Search on these — it only inspects names — so the
// implementation can return nil.
type namedFakeBackend struct {
	name string
}

func (n *namedFakeBackend) Name() string { return n.name }
func (n *namedFakeBackend) Search(ctx context.Context, q string, max int) ([]tools.SearchResult, error) {
	return nil, nil
}

// scriptedProv is a tiny providers.Provider that returns canned
// strings in order, one per Stream call. Re-implemented in the
// whitebox package because the _test package's helper isn't
// importable here.
type scriptedProv struct {
	name      string
	mu        sync.Mutex
	responses []string
	calls     atomic.Int64
	systems   []string // captured system prompts, for assertion
	users     []string // captured user prompts, for assertion
}

func newScripted(name string, responses ...string) *scriptedProv {
	return &scriptedProv{name: name, responses: append([]string(nil), responses...)}
}

func (p *scriptedProv) Name() string                         { return p.name }
func (p *scriptedProv) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (p *scriptedProv) CallCount() int64                     { return p.calls.Load() }

func (p *scriptedProv) Stream(ctx context.Context, req providers.Request) (<-chan providers.Event, error) {
	p.mu.Lock()
	var body string
	if len(p.responses) > 0 {
		body = p.responses[0]
		p.responses = p.responses[1:]
	}
	p.systems = append(p.systems, req.System)
	var user string
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Kind == "text" {
				user += b.Text
			}
		}
	}
	p.users = append(p.users, user)
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

// erringProvider returns an error directly from Stream. Models a
// transport / API-key failure.
type erringProvider struct{ err error }

func (*erringProvider) Name() string                         { return "err-fake" }
func (*erringProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (p *erringProvider) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	return nil, p.err
}

// slowProv hangs forever on Stream until ctx is cancelled, then
// emits no events and closes. Pair with a pre-cancelled context to
// drive cancellation tests.
type slowProv struct{}

func (*slowProv) Name() string                         { return "slow" }
func (*slowProv) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (*slowProv) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	ch := make(chan providers.Event)
	go func() {
		defer close(ch)
		<-ctx.Done()
	}()
	return ch, nil
}

func ptrTrue() *bool  { b := true; return &b }
func ptrFalse() *bool { b := false; return &b }

// makeMultiBackend builds a *tools.MultiBackend from a list of backend
// names. The route phase only reads Names() off the multi, so the
// per-backend Search implementations stay no-op.
func makeMultiBackend(names ...string) *tools.MultiBackend {
	if len(names) == 0 {
		return nil
	}
	primary := &namedFakeBackend{name: names[0]}
	aux := make([]tools.SearchBackend, 0, len(names)-1)
	for _, n := range names[1:] {
		aux = append(aux, &namedFakeBackend{name: n})
	}
	return tools.NewMultiBackend(primary, aux...)
}

// newRouteEngine builds a minimal Engine wired for route-phase
// testing. Caps default to the documented values so the clamp
// branches behave deterministically.
func newRouteEngine(prov providers.Provider, search tools.SearchBackend) *Engine {
	return &Engine{
		Provider:        prov,
		Model:           "test-model",
		Search:          search,
		MaxSubQueries:   DefaultMaxSubQueries,
		SourcesPerQuery: DefaultSourcesPerQuery,
		PerSubQueryCap:  DefaultPerSubQueryCap,
		PerBackendCap:   DefaultPerBackendCap,
		Budget: ResearchBudget{
			MaxProviderCalls: DefaultMaxProviderCalls,
			MaxFetchedBytes:  DefaultMaxFetchedBytes,
			MaxWallClock:     DefaultMaxWallClock,
		},
	}
}

func hasConcernPrefixR(concerns []string, prefix string) bool {
	for _, c := range concerns {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// =============================================================================
// runRoute behaviour
// =============================================================================

func TestRunRoute_DisabledExplicitly_NoLLMCall_DefaultPlan(t *testing.T) {
	prov := newScripted("p1")
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	eng.RoutingEnabled = ptrFalse()
	report := &Report{Question: "q", Query: Query{Question: "q", Sub: []string{"alpha", "beta"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute err: %v", err)
	}
	if prov.CallCount() != 0 {
		t.Errorf("expected 0 provider calls when RoutingEnabled=false; got %d", prov.CallCount())
	}
	if len(report.Routing) != 2 {
		t.Fatalf("Routing rows = %d", len(report.Routing))
	}
	for i, row := range report.Routing {
		if row.SubQuery != report.Query.Sub[i] {
			t.Errorf("row %d SubQuery mismatch", i)
		}
		if len(row.Searches) != 2 {
			t.Errorf("row %d default plan expected 2 searches; got %d", i, len(row.Searches))
		}
		for _, s := range row.Searches {
			if s.Query != row.SubQuery {
				t.Errorf("default plan should use verbatim query; got %q", s.Query)
			}
			if s.MaxResults != eng.SourcesPerQuery {
				t.Errorf("default MaxResults = %d want %d", s.MaxResults, eng.SourcesPerQuery)
			}
		}
	}
}

func TestRunRoute_SingleBackendAutoOff_NoLLMCall(t *testing.T) {
	prov := newScripted("p1")
	search := &namedFakeBackend{name: "solo"}
	eng := newRouteEngine(prov, search)
	report := &Report{Question: "q", Query: Query{Sub: []string{"only sub"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute err: %v", err)
	}
	if prov.CallCount() != 0 {
		t.Errorf("single backend auto-off should skip LLM; got %d calls", prov.CallCount())
	}
	if len(report.Routing) != 1 {
		t.Fatalf("Routing rows = %d", len(report.Routing))
	}
	if got := report.Routing[0].Searches[0].Backend; got != "solo" {
		t.Errorf("Backend = %q want solo", got)
	}
}

func TestRunRoute_MultiBackend_ValidJSON_Populates(t *testing.T) {
	body := `[
	  {"sub_query": "alpha", "searches": [
	    {"backend": "brave", "query": "alpha web", "max_results": 3},
	    {"backend": "arxiv", "query": "alpha papers", "max_results": 2}
	  ]},
	  {"sub_query": "beta", "searches": [
	    {"backend": "wikipedia", "query": "beta wiki", "max_results": 1}
	  ]}
	]`
	prov := newScripted("p1", body)
	multi := makeMultiBackend("brave", "arxiv", "wikipedia")
	eng := newRouteEngine(prov, multi)
	report := &Report{Question: "Q", Query: Query{Question: "Q", Sub: []string{"alpha", "beta"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute: %v", err)
	}
	if len(report.Routing) != 2 {
		t.Fatalf("Routing rows = %d", len(report.Routing))
	}
	if report.Routing[0].Searches[0].Backend != "brave" ||
		report.Routing[0].Searches[0].Query != "alpha web" ||
		report.Routing[0].Searches[0].MaxResults != 3 {
		t.Errorf("alpha brave row wrong: %+v", report.Routing[0].Searches[0])
	}
	if report.Routing[0].Searches[1].Backend != "arxiv" ||
		report.Routing[0].Searches[1].Query != "alpha papers" {
		t.Errorf("alpha arxiv row wrong: %+v", report.Routing[0].Searches[1])
	}
	if report.Routing[1].Searches[0].Backend != "wikipedia" {
		t.Errorf("beta wikipedia row wrong: %+v", report.Routing[1].Searches[0])
	}
}

func TestRunRoute_EnvelopeShape_AlsoParses(t *testing.T) {
	body := `{"plans": [
	  {"sub_query": "alpha", "searches": [{"backend": "brave", "query": "a", "max_results": 2}]}
	]}`
	prov := newScripted("p1", body)
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	report := &Report{Query: Query{Sub: []string{"alpha"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute: %v", err)
	}
	if len(report.Routing) != 1 || report.Routing[0].Searches[0].Backend != "brave" {
		t.Errorf("envelope didn't parse: %+v", report.Routing)
	}
}

func TestRunRoute_MalformedJSON_FallbackPlusConcern(t *testing.T) {
	prov := newScripted("p1", "not JSON at all")
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	report := &Report{Query: Query{Sub: []string{"alpha", "beta"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute should soft-fail; got err: %v", err)
	}
	if len(report.Routing) != 2 {
		t.Fatalf("Routing rows = %d", len(report.Routing))
	}
	for _, row := range report.Routing {
		if len(row.Searches) != 2 {
			t.Errorf("default fan-out expected 2 searches; got %d", len(row.Searches))
		}
	}
	if !hasConcernPrefixR(report.Concerns, "route: parse response:") {
		t.Errorf("expected parse-response concern; got %v", report.Concerns)
	}
}

func TestRunRoute_ProviderError_FallbackPlusConcern(t *testing.T) {
	prov := &erringProvider{err: errors.New("upstream 503")}
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	report := &Report{Query: Query{Sub: []string{"alpha"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute should soft-fail; got err: %v", err)
	}
	if len(report.Routing) != 1 || len(report.Routing[0].Searches) != 2 {
		t.Errorf("expected default-fallback fan-out; got %+v", report.Routing)
	}
	if !hasConcernPrefixR(report.Concerns, "route: provider call:") {
		t.Errorf("expected provider-call concern; got %v", report.Concerns)
	}
}

func TestRunRoute_BudgetExceeded_Propagates(t *testing.T) {
	prov := newScripted("p1", "doesn't matter")
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	// Exhaust the budget before runRoute starts.
	report := &Report{
		Query:  Query{Sub: []string{"alpha"}},
		Budget: BudgetUsage{ProviderCalls: eng.Budget.MaxProviderCalls},
	}
	err := eng.runRoute(context.Background(), report)
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("expected ErrBudgetExceeded; got %v", err)
	}
}

func TestRunRoute_UnknownBackendDropped_RowGetsDefaultFanout(t *testing.T) {
	body := `[
	  {"sub_query": "alpha", "searches": [
	    {"backend": "tavily", "query": "x", "max_results": 2}
	  ]},
	  {"sub_query": "beta", "searches": [
	    {"backend": "brave", "query": "beta", "max_results": 2}
	  ]}
	]`
	prov := newScripted("p1", body)
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	report := &Report{Query: Query{Sub: []string{"alpha", "beta"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute: %v", err)
	}
	if len(report.Routing[0].Searches) != 2 {
		t.Errorf("alpha row should default-fanout; got %d searches", len(report.Routing[0].Searches))
	}
	if len(report.Routing[1].Searches) != 1 ||
		report.Routing[1].Searches[0].Backend != "brave" {
		t.Errorf("beta row should keep brave-only plan; got %+v", report.Routing[1])
	}
	if !hasConcernPrefixR(report.Concerns, "route: sub-query \"alpha\" has no usable searches") {
		t.Errorf("expected default-fallback concern for alpha; got %v", report.Concerns)
	}
}

func TestRunRoute_OmittedSubQueryGetsDefaultPlan(t *testing.T) {
	body := `[
	  {"sub_query": "alpha", "searches": [{"backend": "brave", "query": "a", "max_results": 2}]}
	]`
	prov := newScripted("p1", body)
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	report := &Report{Query: Query{Sub: []string{"alpha", "beta"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute: %v", err)
	}
	if len(report.Routing[1].Searches) != 2 {
		t.Errorf("beta default fan-out expected 2 searches; got %d", len(report.Routing[1].Searches))
	}
	for _, s := range report.Routing[1].Searches {
		if s.Query != "beta" {
			t.Errorf("beta default plan should use verbatim query; got %q", s.Query)
		}
	}
}

func TestRunRoute_PerBackendCapClamps(t *testing.T) {
	body := `[
	  {"sub_query": "alpha", "searches": [{"backend": "brave", "query": "a", "max_results": 50}]}
	]`
	prov := newScripted("p1", body)
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	eng.PerBackendCap = 5
	report := &Report{Query: Query{Sub: []string{"alpha"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute: %v", err)
	}
	if got := report.Routing[0].Searches[0].MaxResults; got != 5 {
		t.Errorf("MaxResults = %d want 5 clamped", got)
	}
	if !hasConcernPrefixR(report.Concerns, "route: clamped brave/") {
		t.Errorf("expected clamp concern; got %v", report.Concerns)
	}
}

func TestRunRoute_PerSubQueryCapReduces(t *testing.T) {
	body := `[
	  {"sub_query": "alpha", "searches": [
	    {"backend": "brave", "query": "a", "max_results": 5},
	    {"backend": "arxiv", "query": "a", "max_results": 5},
	    {"backend": "wikipedia", "query": "a", "max_results": 5}
	  ]}
	]`
	prov := newScripted("p1", body)
	multi := makeMultiBackend("brave", "arxiv", "wikipedia")
	eng := newRouteEngine(prov, multi)
	eng.PerBackendCap = 5
	eng.PerSubQueryCap = 6
	report := &Report{Query: Query{Sub: []string{"alpha"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute: %v", err)
	}
	total := 0
	for _, s := range report.Routing[0].Searches {
		total += s.MaxResults
	}
	if total > 6 {
		t.Errorf("total %d > per-sub-query cap 6", total)
	}
	if !hasConcernPrefixR(report.Concerns, "route: sub-query \"alpha\" total") {
		t.Errorf("expected per-sub-query-cap concern; got %v", report.Concerns)
	}
}

func TestRunRoute_MismatchedSubQueryIgnoredPlusConcern(t *testing.T) {
	body := `[
	  {"sub_query": "totally different", "searches": [
	    {"backend": "brave", "query": "x", "max_results": 1}
	  ]},
	  {"sub_query": "alpha", "searches": [
	    {"backend": "arxiv", "query": "a", "max_results": 1}
	  ]}
	]`
	prov := newScripted("p1", body)
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	report := &Report{Query: Query{Sub: []string{"alpha", "beta"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute: %v", err)
	}
	if !hasConcernPrefixR(report.Concerns, "route: planned sub_query") {
		t.Errorf("expected mismatched-sub_query concern; got %v", report.Concerns)
	}
	// alpha kept its plan; beta default-fanned-out.
	if len(report.Routing[0].Searches) != 1 || report.Routing[0].Searches[0].Backend != "arxiv" {
		t.Errorf("alpha plan dropped: %+v", report.Routing[0])
	}
	if len(report.Routing[1].Searches) != 2 {
		t.Errorf("beta default fan-out: %+v", report.Routing[1])
	}
}

func TestRunRoute_CtxCancellation_PropagatesCtxErr(t *testing.T) {
	prov := &slowProv{}
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	report := &Report{Query: Query{Sub: []string{"alpha"}}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := eng.runRoute(ctx, report)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("expected ctx.Canceled; got %v", err)
	}
}

func TestRunRoute_ZeroSubQueries_NoLLMCall_NoOp(t *testing.T) {
	prov := newScripted("p1")
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	report := &Report{Query: Query{Sub: nil}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute: %v", err)
	}
	if prov.CallCount() != 0 {
		t.Errorf("expected no provider calls; got %d", prov.CallCount())
	}
	if len(report.Routing) != 0 {
		t.Errorf("expected empty Routing; got %d rows", len(report.Routing))
	}
}

func TestRunRoute_CaseInsensitiveBackendMatching(t *testing.T) {
	body := `[
	  {"sub_query": "alpha", "searches": [
	    {"backend": "ArXiv", "query": "a", "max_results": 1}
	  ]}
	]`
	prov := newScripted("p1", body)
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	report := &Report{Query: Query{Sub: []string{"alpha"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute: %v", err)
	}
	if got := report.Routing[0].Searches[0].Backend; got != "arxiv" {
		t.Errorf("expected canonical 'arxiv'; got %q", got)
	}
}

func TestRunRoute_JSONFenceStripping(t *testing.T) {
	body := "```json\n[{\"sub_query\":\"alpha\",\"searches\":[{\"backend\":\"brave\",\"query\":\"a\",\"max_results\":1}]}]\n```"
	prov := newScripted("p1", body)
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	report := &Report{Query: Query{Sub: []string{"alpha"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute: %v", err)
	}
	if len(report.Routing[0].Searches) != 1 ||
		report.Routing[0].Searches[0].Backend != "brave" {
		t.Errorf("fenced JSON didn't parse: %+v", report.Routing)
	}
}

func TestRunRoute_EmptyQueryFallsBackToSub(t *testing.T) {
	body := `[
	  {"sub_query": "alpha thing", "searches": [
	    {"backend": "brave", "query": "", "max_results": 1}
	  ]}
	]`
	prov := newScripted("p1", body)
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	report := &Report{Query: Query{Sub: []string{"alpha thing"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute: %v", err)
	}
	if got := report.Routing[0].Searches[0].Query; got != "alpha thing" {
		t.Errorf("expected verbatim fallback; got %q", got)
	}
}

func TestRunRoute_ZeroMaxResultsDefaultsToPerBackendCap(t *testing.T) {
	body := `[
	  {"sub_query": "alpha", "searches": [
	    {"backend": "brave", "query": "a", "max_results": 0}
	  ]}
	]`
	prov := newScripted("p1", body)
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	eng.PerBackendCap = 5
	report := &Report{Query: Query{Sub: []string{"alpha"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute: %v", err)
	}
	if got := report.Routing[0].Searches[0].MaxResults; got != 5 {
		t.Errorf("zero MaxResults should default to PerBackendCap (5); got %d", got)
	}
}

func TestRunRoute_SingleBackendForcedOn_TailorsQuery(t *testing.T) {
	body := `[
	  {"sub_query": "alpha", "searches": [
	    {"backend": "solo", "query": "tailored alpha", "max_results": 2}
	  ]}
	]`
	prov := newScripted("p1", body)
	search := &namedFakeBackend{name: "solo"}
	eng := newRouteEngine(prov, search)
	eng.RoutingEnabled = ptrTrue()
	report := &Report{Query: Query{Sub: []string{"alpha"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute: %v", err)
	}
	if got := report.Routing[0].Searches[0].Query; got != "tailored alpha" {
		t.Errorf("expected tailored query; got %q", got)
	}
}

func TestRunRoute_PromptContainsAllInputs(t *testing.T) {
	prov := newScripted("p1", "[]")
	multi := makeMultiBackend("brave", "arxiv")
	eng := newRouteEngine(prov, multi)
	report := &Report{Question: "the original question",
		Query: Query{Question: "the original question",
			Sub: []string{"alpha thing", "beta query"}}}

	if err := eng.runRoute(context.Background(), report); err != nil {
		t.Fatalf("runRoute: %v", err)
	}
	if len(prov.users) == 0 {
		t.Fatalf("no user prompts captured")
	}
	got := prov.users[0]
	for _, want := range []string{
		"the original question",
		"alpha thing",
		"beta query",
		"brave",
		"arxiv",
		"general web search",
		"scientific papers",
		"Budget:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q; got:\n%s", want, got)
		}
	}
}

// =============================================================================
// Unit tests for unexported helpers
// =============================================================================

func TestDefaultRoutingPlan_Shape(t *testing.T) {
	eng := &Engine{SourcesPerQuery: 4}
	out := eng.defaultRoutingPlan([]string{"alpha", "beta"}, []string{"brave", "arxiv"})
	if len(out) != 2 {
		t.Fatalf("rows = %d want 2", len(out))
	}
	for i, want := range []string{"alpha", "beta"} {
		if out[i].SubQuery != want {
			t.Errorf("row %d SubQuery = %q want %q", i, out[i].SubQuery, want)
		}
		if len(out[i].Searches) != 2 {
			t.Errorf("row %d Searches = %d want 2", i, len(out[i].Searches))
		}
		for j, b := range []string{"brave", "arxiv"} {
			if out[i].Searches[j].Backend != b {
				t.Errorf("row %d backend %d = %q want %q", i, j, out[i].Searches[j].Backend, b)
			}
			if out[i].Searches[j].Query != want {
				t.Errorf("row %d backend %d query = %q want verbatim %q", i,
					j, out[i].Searches[j].Query, want)
			}
			if out[i].Searches[j].MaxResults != 4 {
				t.Errorf("row %d backend %d MaxResults = %d want 4", i,
					j, out[i].Searches[j].MaxResults)
			}
		}
	}
}

func TestDefaultRoutingPlan_NoSubQueries_Nil(t *testing.T) {
	eng := &Engine{SourcesPerQuery: 4}
	if out := eng.defaultRoutingPlan(nil, []string{"brave"}); out != nil {
		t.Errorf("expected nil; got %+v", out)
	}
}

func TestDefaultRoutingPlan_NoBackends_StillReturnsRowWithEmptySearches(t *testing.T) {
	eng := &Engine{SourcesPerQuery: 3}
	out := eng.defaultRoutingPlan([]string{"alpha"}, nil)
	if len(out) != 1 || len(out[0].Searches) != 0 {
		t.Errorf("expected 1 row with no searches; got %+v", out)
	}
}

func TestDefaultSearchesFor_ZeroMaxFallsBackToConst(t *testing.T) {
	out := defaultSearchesFor("alpha", []string{"brave"}, 0)
	if len(out) != 1 || out[0].MaxResults != DefaultSourcesPerQuery {
		t.Errorf("expected default fallback MaxResults; got %+v", out)
	}
}

func TestBuildRoutingPrompt_ContainsAllPieces(t *testing.T) {
	eng := &Engine{PerSubQueryCap: 7, PerBackendCap: 4}
	system, user := eng.buildRoutingPrompt("the question",
		[]string{"alpha", "beta"},
		[]string{"brave", "arxiv", "mystery-backend"})

	if !strings.Contains(system, "TAILORED") {
		t.Errorf("system missing TAILORED hint: %s", system)
	}
	for _, want := range []string{
		"the question",
		"1. alpha",
		"2. beta",
		"brave: general web search",
		"arxiv: scientific papers",
		"mystery-backend: (general search backend)",
		"<= 7 results/sub-question",
		"<= 4 per backend",
		"sub_query",
		"searches",
		"max_results",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing %q; got:\n%s", want, user)
		}
	}
}

func TestBuildRoutingPrompt_ZeroCapsUseDefaults(t *testing.T) {
	eng := &Engine{} // zero caps
	_, user := eng.buildRoutingPrompt("q", []string{"alpha"}, []string{"brave"})
	if !strings.Contains(user, "<= 8 results/sub-question") {
		t.Errorf("expected default PerSubQueryCap (8) in prompt; got %s", user)
	}
	if !strings.Contains(user, "<= 5 per backend") {
		t.Errorf("expected default PerBackendCap (5) in prompt; got %s", user)
	}
}

func TestParseRoutePlans_BareArray(t *testing.T) {
	body := `[{"sub_query":"a","searches":[{"backend":"brave","query":"a","max_results":1}]}]`
	plans, err := parseRoutePlans(body)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if len(plans) != 1 || plans[0].SubQuery != "a" {
		t.Errorf("unexpected parse: %+v", plans)
	}
}

func TestParseRoutePlans_Envelope(t *testing.T) {
	body := `{"plans":[{"sub_query":"a","searches":[]}]}`
	plans, err := parseRoutePlans(body)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if len(plans) != 1 {
		t.Errorf("unexpected: %+v", plans)
	}
}

func TestParseRoutePlans_Empty(t *testing.T) {
	if _, err := parseRoutePlans(""); err == nil {
		t.Errorf("expected err on empty body")
	}
	if _, err := parseRoutePlans("   \n\t"); err == nil {
		t.Errorf("expected err on whitespace-only body")
	}
}

func TestParseRoutePlans_InvalidShape(t *testing.T) {
	// Valid JSON but neither bare array nor envelope.
	body := `{"not_plans": [], "stray": "data"}`
	if _, err := parseRoutePlans(body); err == nil {
		t.Errorf("expected err on wrong shape")
	}
}

func TestParseRoutePlans_NotJSON(t *testing.T) {
	if _, err := parseRoutePlans("this isn't JSON"); err == nil {
		t.Errorf("expected err on non-JSON body")
	}
}

func TestParseRoutePlans_FencedJSON(t *testing.T) {
	body := "```\n[{\"sub_query\":\"a\",\"searches\":[]}]\n```"
	plans, err := parseRoutePlans(body)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if len(plans) != 1 {
		t.Errorf("fenced body didn't parse: %+v", plans)
	}
}

func TestStripJSONFence_NoFenceIsNoOp(t *testing.T) {
	in := "[1,2,3]"
	if out := stripJSONFence(in); out != in {
		t.Errorf("no-fence input changed: %q -> %q", in, out)
	}
}

func TestStripJSONFence_FenceOnSingleLine(t *testing.T) {
	// Edge: only a fence-opening line, nothing else.
	if out := stripJSONFence("```json"); out != "```json" {
		// Single-line fence with no newline → we bail and return
		// unchanged. Documented behaviour.
		t.Errorf("expected no-op on single-line fence; got %q", out)
	}
}

func TestClampRoutingPlan_NoChangesWhenUnderCaps(t *testing.T) {
	routes := []SubQueryRoute{
		{SubQuery: "alpha", Searches: []PlannedSearch{
			{Backend: "brave", Query: "a", MaxResults: 2},
			{Backend: "arxiv", Query: "a", MaxResults: 2},
		}},
	}
	out, concerns := clampRoutingPlan(routes, 8, 5)
	if len(concerns) != 0 {
		t.Errorf("expected no concerns; got %v", concerns)
	}
	if out[0].Searches[0].MaxResults != 2 || out[0].Searches[1].MaxResults != 2 {
		t.Errorf("unexpected mutation: %+v", out)
	}
}

func TestClampRoutingPlan_PerBackendCap(t *testing.T) {
	routes := []SubQueryRoute{
		{SubQuery: "alpha", Searches: []PlannedSearch{
			{Backend: "brave", Query: "a", MaxResults: 99},
		}},
	}
	out, concerns := clampRoutingPlan(routes, 100, 5)
	if out[0].Searches[0].MaxResults != 5 {
		t.Errorf("expected clamp to 5; got %d", out[0].Searches[0].MaxResults)
	}
	if len(concerns) == 0 {
		t.Errorf("expected concern; got none")
	}
}

func TestClampRoutingPlan_PerSubQueryCapReducesProportionally(t *testing.T) {
	routes := []SubQueryRoute{
		{SubQuery: "alpha", Searches: []PlannedSearch{
			{Backend: "brave", Query: "a", MaxResults: 4},
			{Backend: "arxiv", Query: "a", MaxResults: 4},
			{Backend: "wikipedia", Query: "a", MaxResults: 4},
		}},
	}
	out, concerns := clampRoutingPlan(routes, 6, 5)
	total := 0
	for _, s := range out[0].Searches {
		total += s.MaxResults
	}
	if total > 6 {
		t.Errorf("total %d > cap 6", total)
	}
	if len(concerns) == 0 {
		t.Errorf("expected concern; got none")
	}
}

func TestClampRoutingPlan_ZeroMaxDefaultsToPerBackendCap(t *testing.T) {
	routes := []SubQueryRoute{
		{SubQuery: "alpha", Searches: []PlannedSearch{
			{Backend: "brave", Query: "a", MaxResults: 0},
		}},
	}
	out, _ := clampRoutingPlan(routes, 8, 5)
	if out[0].Searches[0].MaxResults != 5 {
		t.Errorf("expected default to PerBackendCap (5); got %d", out[0].Searches[0].MaxResults)
	}
}

func TestClampRoutingPlan_ZeroCapsUseDefaults(t *testing.T) {
	routes := []SubQueryRoute{
		{SubQuery: "alpha", Searches: []PlannedSearch{
			{Backend: "brave", Query: "a", MaxResults: 999},
		}},
	}
	// Zero caps should fall back to the defaults.
	out, _ := clampRoutingPlan(routes, 0, 0)
	if out[0].Searches[0].MaxResults != DefaultPerBackendCap {
		t.Errorf("zero cap should default; got %d", out[0].Searches[0].MaxResults)
	}
}

func TestProportionalReduce_PreservesAtLeastOnePerEntry(t *testing.T) {
	in := []PlannedSearch{
		{Backend: "a", MaxResults: 1},
		{Backend: "b", MaxResults: 100},
	}
	out := proportionalReduce(in, 5)
	if len(out) == 0 {
		t.Fatalf("expected at least one survivor")
	}
	total := 0
	for _, s := range out {
		total += s.MaxResults
	}
	if total > 5 {
		t.Errorf("total %d > 5", total)
	}
}

func TestProportionalReduce_ZeroCap_Nil(t *testing.T) {
	in := []PlannedSearch{{Backend: "a", MaxResults: 5}}
	if out := proportionalReduce(in, 0); out != nil {
		t.Errorf("zero cap should return nil; got %+v", out)
	}
}

func TestProportionalReduce_AlreadyUnderCap_PassThrough(t *testing.T) {
	in := []PlannedSearch{{Backend: "a", MaxResults: 1}, {Backend: "b", MaxResults: 2}}
	out := proportionalReduce(in, 10)
	if len(out) != 2 || out[0].MaxResults != 1 || out[1].MaxResults != 2 {
		t.Errorf("unexpected reduction: %+v", out)
	}
}

func TestProportionalReduce_SingleSurvivorHardClamp(t *testing.T) {
	// Single entry, way over cap → hard-clamp.
	in := []PlannedSearch{{Backend: "a", MaxResults: 100}}
	out := proportionalReduce(in, 5)
	if len(out) != 1 || out[0].MaxResults != 5 {
		t.Errorf("expected single hard-clamped survivor; got %+v", out)
	}
}

func TestNormaliseSubQuery_LowerAndTrim(t *testing.T) {
	if got := normaliseSubQuery("  Alpha THING  "); got != "alpha thing" {
		t.Errorf("got %q", got)
	}
}

func TestProportionalReduce_DropLowestThenScale(t *testing.T) {
	// After scaling, sum can still exceed cap (rounding-up due to
	// the "preserve at least 1" rule). Verify the drop-lowest loop
	// kicks in and removes entries until sum <= cap.
	in := []PlannedSearch{
		{Backend: "a", MaxResults: 1},
		{Backend: "b", MaxResults: 1},
		{Backend: "c", MaxResults: 1},
		{Backend: "d", MaxResults: 1},
		{Backend: "e", MaxResults: 1},
	}
	// total=5, cap=2 → scaled=0 each → bumped to 1 each → sum=5 > 2.
	// Drop loop should leave 2 entries surviving.
	out := proportionalReduce(in, 2)
	sum := 0
	for _, s := range out {
		sum += s.MaxResults
	}
	if sum > 2 {
		t.Errorf("sum %d > cap 2 after drop-loop", sum)
	}
	if len(out) == 0 || len(out) >= 5 {
		t.Errorf("expected reduced length; got %d", len(out))
	}
}

func TestBuildRoutesFromPlans_EmptyPlans_AllDefaults(t *testing.T) {
	routes, concerns := buildRoutesFromPlans(nil,
		[]string{"alpha", "beta"},
		[]string{"brave"})
	if len(concerns) != 0 {
		t.Errorf("expected no concerns; got %v", concerns)
	}
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes; got %d", len(routes))
	}
	for _, r := range routes {
		if len(r.Searches) != 0 {
			t.Errorf("expected empty Searches before clamp/default-fill; got %+v", r)
		}
	}
}
