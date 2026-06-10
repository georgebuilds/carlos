package research

import (
	"context"
	"fmt"
	"strings"

	"github.com/georgebuilds/carlos/internal/tools"
)

// runSearch consumes report.Routing (the model-picked plan from the
// route phase) and dispatches one search per PlannedSearch, then
// records every unique result URL as a candidate Source for the
// fetch phase.
//
// Compatibility with the legacy (pre-routing) shape: when
// report.Routing is empty — which happens when the engine is run
// without the route phase wired in, as is still the case for some
// tests — we synthesise an inline default plan (verbatim sub-query,
// SourcesPerQuery cap, single Search.Search call per sub-query) so
// the body below stays uniform. The behaviour in that branch is
// byte-identical to what the engine produced before slice 11.5
// introduced routing.
//
// Search results are stored as Sources with empty Content until the
// fetch phase fills them in. IDs are assigned at fetch time, not
// here — that way a search hit we skip (because it dupes an earlier
// one) doesn't waste a slot in the s1/s2/… numbering.
//
// Errors per PlannedSearch are NOT fatal; we record them in
// Concerns and continue. A research arc with 9/10 successful
// sub-search calls is more useful than a hard abort.
func (e *Engine) runSearch(ctx context.Context, report *Report) (err error) {
	t0 := e.beginPhase("search")
	defer func() { e.endPhase("search", t0, err) }()

	if len(report.Routing) == 0 {
		if len(report.Query.Sub) == 0 {
			return fmt.Errorf("no sub-queries to search")
		}
		// Legacy fallback: no routing decided. Build a default inline
		// plan that mirrors the pre-routing engine: every sub-query
		// gets one Search call against the configured backend, asking
		// for SourcesPerQuery results, with the verbatim sub-query as
		// the query string. The dispatch loop below treats this
		// uniformly with the model-routed shape.
		report.Routing = legacyRouting(report.Query.Sub, e.searchBackendName(), e.SourcesPerQuery)
	}

	seenURL := map[string]bool{}
	for _, route := range report.Routing {
		for _, planned := range route.Searches {
			if err := ctx.Err(); err != nil {
				return err
			}
			results, searchErr := e.dispatchPlanned(ctx, planned)
			if searchErr != nil {
				report.Concerns = append(report.Concerns,
					fmt.Sprintf("search: backend=%s sub-query=%q: %v",
						planned.Backend, route.SubQuery, searchErr))
				continue
			}
			cap := planned.MaxResults
			if cap <= 0 {
				cap = e.SourcesPerQuery
			}
			for _, r := range pickTopResults(results, cap) {
				u := strings.TrimSpace(r.URL)
				if u == "" || seenURL[u] {
					continue
				}
				seenURL[u] = true
				report.Sources = append(report.Sources, Source{
					URL:   u,
					Title: r.Title,
				})
			}
		}
	}
	if len(report.Sources) == 0 {
		report.Concerns = append(report.Concerns,
			"search: no sources found for any planned search")
		return fmt.Errorf("no sources found")
	}
	return nil
}

// dispatchPlanned routes ONE PlannedSearch to the configured backend.
// If the backend is a *tools.MultiBackend, we restrict the fan-out to
// the single named backend via SearchSubset; if it's a single
// backend, we ignore the planned.Backend name (there's only one
// possible target) and call Search directly.
func (e *Engine) dispatchPlanned(ctx context.Context, planned PlannedSearch) ([]tools.SearchResult, error) {
	max := planned.MaxResults
	if max <= 0 {
		max = e.SourcesPerQuery
	}
	if multi, ok := e.Search.(*tools.MultiBackend); ok {
		return multi.SearchSubset(ctx, planned.Query, max,
			[]string{planned.Backend}, 0)
	}
	return e.Search.Search(ctx, planned.Query, max)
}

// searchBackendName returns the configured backend's Name(), or
// "(unknown)" if Search is nil (defensive — Run() validates Search
// before this is reachable).
func (e *Engine) searchBackendName() string {
	if e.Search == nil {
		return "(unknown)"
	}
	return e.Search.Name()
}

// legacyRouting builds the verbatim-query, single-call-per-sub-query
// plan that mirrors what the pre-routing engine produced. Used only
// in the runSearch fallback branch — the route phase itself uses
// Engine.defaultRoutingPlan, which fans out across every backend.
func legacyRouting(subQueries []string, backendName string, perQuery int) []SubQueryRoute {
	if perQuery <= 0 {
		perQuery = DefaultSourcesPerQuery
	}
	out := make([]SubQueryRoute, 0, len(subQueries))
	for _, sub := range subQueries {
		out = append(out, SubQueryRoute{
			SubQuery: sub,
			Searches: []PlannedSearch{{
				Backend:    backendName,
				Query:      sub,
				MaxResults: perQuery,
			}},
		})
	}
	return out
}

// pickTopResults returns at most n results from the backend's
// response. The backend already promises at most n; this is a
// belt-and-braces guard.
func pickTopResults(rs []tools.SearchResult, n int) []tools.SearchResult {
	if len(rs) <= n {
		return rs
	}
	return rs[:n]
}
