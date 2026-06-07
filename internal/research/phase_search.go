package research

import (
	"context"
	"fmt"
	"strings"

	"github.com/georgebuilds/carlos/internal/tools"
)

// runSearch runs the configured SearchBackend once per sub-query and
// records the top N URLs (where N = SourcesPerQuery) as candidate
// sources on Report. Sources are deduplicated across sub-queries by
// URL so a hit returned by two queries doesn't get fetched twice.
//
// Search results are stored as Sources with empty Content until the
// fetch phase fills them in. IDs are assigned at fetch time, not
// here - that way a search hit we skip (because it dupes an earlier
// one) doesn't waste a slot in the s1/s2/… numbering.
//
// Search errors per sub-query are NOT fatal; we record them in
// Concerns and continue. A research arc with 4 successful sub-queries
// and 1 search failure is more useful than a hard abort.
func (e *Engine) runSearch(ctx context.Context, report *Report) (err error) {
	t0 := e.beginPhase("search")
	defer func() { e.endPhase("search", t0, err) }()

	if len(report.Query.Sub) == 0 {
		return fmt.Errorf("no sub-queries to search")
	}
	seenURL := map[string]bool{}
	for _, sub := range report.Query.Sub {
		if err := ctx.Err(); err != nil {
			return err
		}
		results, err := e.Search.Search(ctx, sub, e.SourcesPerQuery)
		if err != nil {
			report.Concerns = append(report.Concerns,
				fmt.Sprintf("search: sub-query %q: %v", sub, err))
			continue
		}
		for _, r := range pickTopResults(results, e.SourcesPerQuery) {
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
	if len(report.Sources) == 0 {
		report.Concerns = append(report.Concerns,
			"search: no sources found for any sub-query")
		return fmt.Errorf("no sources found")
	}
	return nil
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
