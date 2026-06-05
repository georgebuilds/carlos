package research

import (
	"context"
	"fmt"
)

// runFetch resolves the URLs the search phase queued into Source
// bodies. IDs ("s1", "s2", …) are assigned here in fetch order so the
// numbering is stable across re-runs as long as the search input is.
//
// Per-source fetch errors are NOT fatal; the failing source is
// dropped (no ID assigned) and the failure is recorded in Concerns.
// The reasoning mirrors search: a research arc that lost one source
// to a 404 is still useful.
//
// Budget enforcement: after each fetch, the body size is added to
// Report.Budget.FetchedBytes. If the cap would be exceeded by the
// next fetch, the phase stops fetching, surfaces the budget concern,
// and returns ErrBudgetExceeded. Sources already fetched stay in
// Report.
func (e *Engine) runFetch(ctx context.Context, report *Report) (err error) {
	t0 := e.beginPhase("fetch")
	defer func() { e.endPhase("fetch", t0, err) }()

	if len(report.Sources) == 0 {
		return fmt.Errorf("no sources to fetch")
	}
	fetched := make([]Source, 0, len(report.Sources))
	nextID := 1
	for _, candidate := range report.Sources {
		if err := ctx.Err(); err != nil {
			report.Sources = fetched
			return err
		}
		// Refuse before each fetch — we don't know the body size
		// until we read it, so the check uses what we've already
		// charged. A single oversized fetch can still push us past
		// the cap by one body; we accept that overshoot in exchange
		// for the simpler "stop AT the cap" semantics.
		if report.Budget.FetchedBytes >= e.Budget.MaxFetchedBytes {
			report.Sources = fetched
			report.Concerns = append(report.Concerns,
				fmt.Sprintf("fetch: byte budget exhausted at %d/%d bytes; fetched %d/%d sources",
					report.Budget.FetchedBytes, e.Budget.MaxFetchedBytes,
					len(fetched), len(report.Sources)))
			return ErrBudgetExceeded
		}
		src, err := e.Fetcher.Fetch(ctx, candidate.URL)
		if err != nil {
			report.Concerns = append(report.Concerns,
				fmt.Sprintf("fetch: %s: %v", candidate.URL, err))
			continue
		}
		// Preserve the search-time title if the fetch didn't yield
		// one (common for plain text or sites that omit <title>).
		if src.Title == "" {
			src.Title = candidate.Title
		}
		src.ID = fmt.Sprintf("s%d", nextID)
		nextID++
		report.Budget.FetchedBytes += int64(len(src.Content))
		fetched = append(fetched, src)
	}
	report.Sources = fetched
	if len(fetched) == 0 {
		return fmt.Errorf("no sources fetched")
	}
	return nil
}
