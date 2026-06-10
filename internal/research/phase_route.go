package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/georgebuilds/carlos/internal/tools"
)

// routeSystem is the system prompt for the routing phase. We tell the
// model exactly what shape we want back; the user prompt enumerates
// the available backends with one-line descriptions so the model can
// pick well.
const routeSystem = `You plan multi-source research. Choose which backends to query for each sub-question and craft a TAILORED query string for each backend's strengths. Return strict JSON.`

// routeUserTemplate is built by buildRoutingPrompt; documented here as
// a comment-only reference. The actual prompt is assembled inline so
// we can interpolate backend descriptions cleanly.

// backendDescriptions hard-codes a one-line hint per known backend.
// The model is far more likely to tailor queries well when it knows
// what each backend is good at. Unknown backends get a generic
// description; the routing flow still works, it just won't get
// backend-tailored phrasing for them.
var backendDescriptions = map[string]string{
	"arxiv":      "scientific papers + preprints (ML, CS, physics, math)",
	"wikipedia":  "encyclopedia articles",
	"brave":      "general web search",
	"searxng":    "general web (privacy)",
	"duckduckgo": "general web",
	"github":     "code search on GitHub",
}

// routePlan is the parsed shape of one entry in the model's JSON
// response. Each entry covers one sub-query and the per-backend
// searches the model recommends running for it.
type routePlan struct {
	SubQuery string            `json:"sub_query"`
	Searches []routePlanSearch `json:"searches"`
}

// routePlanSearch is one (backend, tailored-query, max-results)
// tuple inside a routePlan. The engine clamps MaxResults to
// PerBackendCap; the model can propose more (it's hint-only).
type routePlanSearch struct {
	Backend    string `json:"backend"`
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

// routePlanEnvelope handles the case where the model wraps the array
// in `{"plans": [...]}` instead of returning a bare array. We try the
// bare-array shape first, then fall back to the envelope shape; both
// shapes are accepted as the canonical wire format.
type routePlanEnvelope struct {
	Plans []routePlan `json:"plans"`
}

// runRoute is the LLM-routed search planning phase. The model is given
// the user's question, the decomposed sub-queries, and the list of
// available search backends, and returns a per-sub-query plan: which
// backends to query and what tailored query string to send each.
//
// The result populates report.Routing, which the search phase
// consumes. On any failure (LLM error, malformed JSON, all-zero
// matches), the phase falls back to a DEFAULT plan (every available
// backend, verbatim sub-query, SourcesPerQuery cap) and records a
// concern. The fallback shape is byte-identical to what the legacy
// (pre-routing) engine produced, so the run NEVER aborts due to
// routing failure.
//
// RoutingEnabled toggle:
//   - nil    → auto: true if Search is *tools.MultiBackend, else false
//   - *true  → always route (even with a single backend; just tailors
//              query strings)
//   - *false → skip the LLM call entirely; default-plan straight away
func (e *Engine) runRoute(ctx context.Context, report *Report) (err error) {
	t0 := e.beginPhase("route")
	defer func() { e.endPhase("route", t0, err) }()

	// 1. Determine available backend names. A MultiBackend exposes
	// every contributing backend's name; a single backend exposes its
	// own. We always have at least one entry unless Search is nil
	// (Run() validates that already, but guard anyway for direct
	// runRoute calls in tests).
	var backends []string
	if multi, ok := e.Search.(*tools.MultiBackend); ok {
		backends = multi.Names()
	} else if e.Search != nil {
		backends = []string{e.Search.Name()}
	}

	// 2. Determine effective RoutingEnabled. nil → auto (true only
	// for MultiBackend, since single-backend routing only tailors the
	// query string — useful when forced on, not by default).
	routingEnabled := false
	if e.RoutingEnabled != nil {
		routingEnabled = *e.RoutingEnabled
	} else {
		_, isMulti := e.Search.(*tools.MultiBackend)
		routingEnabled = isMulti
	}

	// 3. Short-circuit: no sub-queries, no backends, or routing
	// explicitly off → default plan, no LLM call.
	if !routingEnabled || len(backends) == 0 || len(report.Query.Sub) == 0 {
		report.Routing = e.defaultRoutingPlan(report.Query.Sub, backends)
		return nil
	}

	// 4. Build the prompt and call the provider.
	system, user := e.buildRoutingPrompt(report.Question, report.Query.Sub, backends)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	body, err := e.callProvider(ctx, report, system, user)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if err != nil {
		// Budget exceeded is the engine's graceful-abort signal —
		// propagate so the main loop unwinds with the partial Report.
		if errors.Is(err, ErrBudgetExceeded) {
			return err
		}
		// Cancellation also propagates: callers asked us to stop.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		report.Concerns = append(report.Concerns,
			fmt.Sprintf("route: provider call: %v", err))
		report.Routing = e.defaultRoutingPlan(report.Query.Sub, backends)
		return nil
	}

	// 5. Parse the response. Try bare array first, then envelope. If
	// both fail, fall back to default plan.
	plans, parseErr := parseRoutePlans(body)
	if parseErr != nil {
		report.Concerns = append(report.Concerns,
			fmt.Sprintf("route: parse response: %v", parseErr))
		report.Routing = e.defaultRoutingPlan(report.Query.Sub, backends)
		return nil
	}

	// 6. Validate & assemble the SubQueryRoute list, one entry per
	// sub-query in report.Query.Sub order.
	routes, concerns := buildRoutesFromPlans(plans, report.Query.Sub, backends)

	// 7. Clamp per-backend + per-sub-query caps.
	clamped, clampConcerns := clampRoutingPlan(routes, e.PerSubQueryCap, e.PerBackendCap)

	// 8. For any sub-query whose row ended up with zero planned
	// searches (e.g. the model only listed unknown backends), fall
	// back to the default fan-out for that row and record a concern.
	for i := range clamped {
		if len(clamped[i].Searches) == 0 {
			clamped[i] = SubQueryRoute{
				SubQuery: report.Query.Sub[i],
				Searches: defaultSearchesFor(report.Query.Sub[i], backends, e.SourcesPerQuery),
			}
			concerns = append(concerns,
				fmt.Sprintf("route: sub-query %q has no usable searches after validation; using default fan-out",
					report.Query.Sub[i]))
		}
	}

	report.Concerns = append(report.Concerns, concerns...)
	report.Concerns = append(report.Concerns, clampConcerns...)
	report.Routing = clamped
	return nil
}

// defaultRoutingPlan builds the verbatim-query, every-backend fan-out
// fallback used both when routing is off and when the LLM-driven path
// fails. The shape matches what the legacy (pre-routing) engine
// produced — every backend gets the original sub-query, capped at
// SourcesPerQuery.
func (e *Engine) defaultRoutingPlan(subQueries []string, backends []string) []SubQueryRoute {
	if len(subQueries) == 0 {
		return nil
	}
	out := make([]SubQueryRoute, 0, len(subQueries))
	for _, sub := range subQueries {
		out = append(out, SubQueryRoute{
			SubQuery: sub,
			Searches: defaultSearchesFor(sub, backends, e.SourcesPerQuery),
		})
	}
	return out
}

// defaultSearchesFor builds the per-backend PlannedSearch slice for
// ONE sub-query. Used both by defaultRoutingPlan and by the
// per-row fallback when a sub-query's plan collapses to zero.
func defaultSearchesFor(sub string, backends []string, max int) []PlannedSearch {
	if len(backends) == 0 {
		return nil
	}
	if max <= 0 {
		max = DefaultSourcesPerQuery
	}
	out := make([]PlannedSearch, 0, len(backends))
	for _, b := range backends {
		out = append(out, PlannedSearch{
			Backend:    b,
			Query:      sub,
			MaxResults: max,
		})
	}
	return out
}

// buildRoutingPrompt constructs the (system, user) pair for the route
// phase. The system prompt is a constant; the user prompt enumerates
// the sub-queries, the available backends with one-line descriptions,
// and the result-budget caps so the model can size its plan.
func (e *Engine) buildRoutingPrompt(question string, subQueries []string, backends []string) (system, user string) {
	var sb strings.Builder
	sb.WriteString("User question: ")
	sb.WriteString(question)
	sb.WriteString("\n\nSub-questions:\n")
	for i, sub := range subQueries {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, sub)
	}
	sb.WriteString("\nAvailable backends:\n")
	for _, b := range backends {
		desc, ok := backendDescriptions[strings.ToLower(b)]
		if !ok {
			desc = "(general search backend)"
		}
		fmt.Fprintf(&sb, "%s: %s\n", b, desc)
	}
	perSubCap := e.PerSubQueryCap
	if perSubCap <= 0 {
		perSubCap = DefaultPerSubQueryCap
	}
	perBackendCap := e.PerBackendCap
	if perBackendCap <= 0 {
		perBackendCap = DefaultPerBackendCap
	}
	fmt.Fprintf(&sb, "\nBudget: <= %d results/sub-question, <= %d per backend.\n", perSubCap, perBackendCap)
	sb.WriteString("\nReturn JSON array. Each entry:\n")
	sb.WriteString(`  {"sub_query": str, "searches": [{"backend": str, "query": str, "max_results": int}]}` + "\n")
	sb.WriteString("No preamble. No markdown fence.")
	return routeSystem, sb.String()
}

// parseRoutePlans tries the bare-array shape first, then the
// `{"plans": [...]}` envelope. Returns the parsed plans or the first
// parse error encountered. Also tolerates surrounding whitespace and
// a stray markdown code fence the model might emit despite the
// "no markdown fence" instruction.
func parseRoutePlans(body string) ([]routePlan, error) {
	trimmed := stripJSONFence(strings.TrimSpace(body))
	if trimmed == "" {
		return nil, errors.New("empty response")
	}
	var plans []routePlan
	if err := json.Unmarshal([]byte(trimmed), &plans); err == nil {
		return plans, nil
	}
	var env routePlanEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err == nil && len(env.Plans) > 0 {
		return env.Plans, nil
	}
	// Re-try the bare-array decode to surface its error message — the
	// envelope's "no plans key" failure isn't as informative.
	var probe any
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("response was not a JSON array or {\"plans\": [...]} envelope")
}

// stripJSONFence drops a leading ```json / ``` fence and matching
// trailing ``` if present. Defensive; the prompt explicitly forbids
// fences but models sometimes ignore the instruction.
func stripJSONFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop first line.
	if nl := strings.Index(s, "\n"); nl >= 0 {
		s = s[nl+1:]
	} else {
		return s
	}
	s = strings.TrimRight(s, "` \n\t")
	return s
}

// buildRoutesFromPlans validates the model's plans against the
// available sub-queries and backends, assembling them into a
// SubQueryRoute slice in the same order as report.Query.Sub. Unknown
// backends are silently dropped; mismatched sub_query strings are
// recorded as concerns and skipped. Sub-queries the model omitted are
// filled in with the default plan.
func buildRoutesFromPlans(plans []routePlan, subQueries, backends []string) ([]SubQueryRoute, []string) {
	// Build a lookup from lowercased+trimmed sub-query to its index.
	subIdx := make(map[string]int, len(subQueries))
	for i, sub := range subQueries {
		subIdx[normaliseSubQuery(sub)] = i
	}
	// Build a lookup for backend names (case-insensitive → canonical).
	backendMap := make(map[string]string, len(backends))
	for _, b := range backends {
		backendMap[strings.ToLower(strings.TrimSpace(b))] = b
	}

	// One slot per sub-query; we fill from the plans then default-
	// fill any holes at the end.
	routes := make([]SubQueryRoute, len(subQueries))
	for i, sub := range subQueries {
		routes[i] = SubQueryRoute{SubQuery: sub}
	}

	var concerns []string
	for _, plan := range plans {
		idx, ok := subIdx[normaliseSubQuery(plan.SubQuery)]
		if !ok {
			concerns = append(concerns,
				fmt.Sprintf("route: planned sub_query %q does not match any decomposed sub-query; ignored",
					plan.SubQuery))
			continue
		}
		for _, s := range plan.Searches {
			canonical, ok := backendMap[strings.ToLower(strings.TrimSpace(s.Backend))]
			if !ok {
				// Unknown backend — silently dropped (the docstring
				// for SubQueryRoute calls this out as silent-drop
				// behaviour). We don't record a concern per entry to
				// avoid Concerns blow-up when the model proposes a
				// whole-class typo.
				continue
			}
			query := strings.TrimSpace(s.Query)
			if query == "" {
				// Fall back to the verbatim sub-query when the model
				// neglected to tailor.
				query = subQueries[idx]
			}
			routes[idx].Searches = append(routes[idx].Searches, PlannedSearch{
				Backend:    canonical,
				Query:      query,
				MaxResults: s.MaxResults,
			})
		}
	}
	return routes, concerns
}

// clampRoutingPlan enforces the PerBackendCap and PerSubQueryCap
// hard ceilings the engine guarantees regardless of what the model
// proposes. PerBackendCap is applied first (each PlannedSearch
// clamped); then PerSubQueryCap is enforced by proportional reduction
// across the row, with rounding-tie-breaking by dropping the lowest-
// cap entries.
//
// Returns the clamped routes plus a concern slice describing every
// clamp that fired. Concerns are deliberately specific so an operator
// can see whether the model is consistently over-budget.
func clampRoutingPlan(routes []SubQueryRoute, perSubCap, perBackendCap int) ([]SubQueryRoute, []string) {
	if perBackendCap <= 0 {
		perBackendCap = DefaultPerBackendCap
	}
	if perSubCap <= 0 {
		perSubCap = DefaultPerSubQueryCap
	}
	var concerns []string
	out := make([]SubQueryRoute, len(routes))
	for i, route := range routes {
		searches := make([]PlannedSearch, len(route.Searches))
		copy(searches, route.Searches)

		// Per-backend clamp. Model may also propose 0 or negative;
		// fall back to perBackendCap there too so search dispatches
		// have a sensible quota.
		for j := range searches {
			if searches[j].MaxResults <= 0 {
				searches[j].MaxResults = perBackendCap
				continue
			}
			if searches[j].MaxResults > perBackendCap {
				concerns = append(concerns,
					fmt.Sprintf("route: clamped %s/%q max_results %d -> %d (per-backend cap)",
						searches[j].Backend, route.SubQuery,
						searches[j].MaxResults, perBackendCap))
				searches[j].MaxResults = perBackendCap
			}
		}

		// Per-sub-query total clamp.
		total := 0
		for _, s := range searches {
			total += s.MaxResults
		}
		if total > perSubCap {
			searches = proportionalReduce(searches, perSubCap)
			concerns = append(concerns,
				fmt.Sprintf("route: sub-query %q total %d > per-sub-query cap %d; reduced",
					route.SubQuery, total, perSubCap))
		}

		out[i] = SubQueryRoute{
			SubQuery: route.SubQuery,
			Searches: searches,
		}
	}
	return out, concerns
}

// proportionalReduce scales each PlannedSearch.MaxResults down so the
// sum lands at-or-below cap, dropping zero-quota entries. If
// rounding still leaves us over the cap, the lowest-quota entries are
// dropped (last-in-list deterministic tiebreaker so the result is
// stable across runs). At least one search survives for any non-empty
// input — we never reduce a row to zero unless cap itself was zero.
func proportionalReduce(searches []PlannedSearch, cap int) []PlannedSearch {
	if cap <= 0 {
		return nil
	}
	total := 0
	for _, s := range searches {
		total += s.MaxResults
	}
	if total <= cap {
		return searches
	}
	out := make([]PlannedSearch, 0, len(searches))
	for _, s := range searches {
		scaled := s.MaxResults * cap / total
		if scaled <= 0 {
			// Integer truncation can zero out small entries; preserve
			// at least 1 so the backend still gets exercised.
			scaled = 1
		}
		s.MaxResults = scaled
		out = append(out, s)
	}
	// Recompute sum; if still over cap, trim the lowest-quota entries.
	sum := 0
	for _, s := range out {
		sum += s.MaxResults
	}
	for sum > cap && len(out) > 1 {
		// Find the index of the lowest-MaxResults entry; on ties,
		// pick the highest index (stable, deterministic).
		dropIdx := 0
		for j, s := range out {
			if s.MaxResults <= out[dropIdx].MaxResults {
				dropIdx = j
			}
		}
		sum -= out[dropIdx].MaxResults
		out = append(out[:dropIdx], out[dropIdx+1:]...)
	}
	// Last-ditch: if a single survivor is still over cap, hard-clamp.
	if len(out) == 1 && out[0].MaxResults > cap {
		out[0].MaxResults = cap
	}
	return out
}

// normaliseSubQuery lowercases + trims for case-insensitive match
// between the model's echoed sub_query and the decomposed list.
func normaliseSubQuery(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

