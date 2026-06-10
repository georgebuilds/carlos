// Shared multi-query plumbing for the four search tools (web_search,
// arxiv_search, wikipedia_search, gh_search). The model-facing shape
// is backward compatible:
//
//   - {"query": "foo"}                          → single-query path
//   - {"queries": ["a", "b", "c"]}              → batched path
//
// Setting both, or neither, is a validation error. The batched path
// runs each query through the same backend code under a per-provider
// throttle (serial for arxiv + github, capped-concurrent for
// wikipedia, etc) and returns a per-query result block so the model
// can map each batch entry back to its query.
//
// Why a single shared parser instead of inlining the logic into each
// tool: the validation rules are identical across all four tools, and
// regression-testing five copies of the same "both set → error" check
// is a maintenance bill we don't have to pay. The throttling itself
// stays in each tool's Execute because each provider's rate-limit
// budget is unique.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// multiQueryInput is the discriminated union for the four search
// tools' Execute input. Either Query is non-empty (legacy) or Queries
// is non-empty (batched). Exactly one of the two must be set.
type multiQueryInput struct {
	Query   string   `json:"query"`
	Queries []string `json:"queries"`
}

// parseQueries pulls the query / queries fields out of input and
// returns the validated list. Returns (nil, isBatch=false, error) when
// validation fails. The single-query path returns a one-element slice
// with isBatch=false so callers can use the same loop in both modes;
// callers that need to format output differently (batched envelope vs
// flat envelope) check isBatch.
//
// Errors are returned with the tool name prefix already attached so
// callers can return them verbatim.
func parseQueries(tool string, raw []byte) (queries []string, isBatch bool, err error) {
	var in multiQueryInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, false, fmt.Errorf("%s: parse input: %w", tool, err)
	}
	trimmedQuery := strings.TrimSpace(in.Query)
	hasQuery := trimmedQuery != ""
	// Filter out empty/whitespace entries from queries; if every entry
	// is empty the list is effectively empty.
	cleanedQueries := make([]string, 0, len(in.Queries))
	for _, q := range in.Queries {
		if t := strings.TrimSpace(q); t != "" {
			cleanedQueries = append(cleanedQueries, t)
		}
	}
	hasQueries := len(cleanedQueries) > 0

	switch {
	case hasQuery && hasQueries:
		return nil, false, fmt.Errorf("%s: pass either query or queries, not both", tool)
	case !hasQuery && !hasQueries:
		return nil, false, fmt.Errorf("%s: empty query (set query or queries)", tool)
	case hasQueries:
		return cleanedQueries, true, nil
	default:
		return []string{trimmedQuery}, false, nil
	}
}

// batchedSearchResultBlock is one entry in the batched output envelope.
// Fields stay JSON-stable so model prompts can rely on the shape.
type batchedSearchResultBlock struct {
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
	Error   string         `json:"error,omitempty"`
}

// runSerialBatch runs queries one at a time, calling fn for each. The
// spacing between calls is fn's responsibility (arxiv has its own
// rate-limit gate built into the backend; this helper just sequences).
// A per-query error is captured in the block's Error field rather than
// aborting the whole batch - the model can still use the queries that
// did succeed.
func runSerialBatch(ctx context.Context, queries []string, fn func(ctx context.Context, q string) ([]SearchResult, error)) []batchedSearchResultBlock {
	out := make([]batchedSearchResultBlock, 0, len(queries))
	for _, q := range queries {
		if err := ctx.Err(); err != nil {
			out = append(out, batchedSearchResultBlock{Query: q, Error: err.Error()})
			continue
		}
		results, err := fn(ctx, q)
		blk := batchedSearchResultBlock{Query: q, Results: results}
		if err != nil {
			blk.Error = err.Error()
		}
		out = append(out, blk)
	}
	return out
}

// runConcurrentBatch runs queries with a fan-out cap. concurrency
// values below 1 are treated as 1 (= serial). Results come back in
// input order regardless of completion order.
func runConcurrentBatch(ctx context.Context, queries []string, concurrency int, fn func(ctx context.Context, q string) ([]SearchResult, error)) []batchedSearchResultBlock {
	if concurrency < 1 {
		concurrency = 1
	}
	out := make([]batchedSearchResultBlock, len(queries))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func(i int, q string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := ctx.Err(); err != nil {
				out[i] = batchedSearchResultBlock{Query: q, Error: err.Error()}
				return
			}
			results, err := fn(ctx, q)
			blk := batchedSearchResultBlock{Query: q, Results: results}
			if err != nil {
				blk.Error = err.Error()
			}
			out[i] = blk
		}(i, q)
	}
	wg.Wait()
	return out
}

// (Internal helpers stop here. Tool-specific batched output envelopes
// live in each tool file because their per-query block carries
// slightly different fields - e.g. gh_search blocks include kind +
// count, web_search blocks include partial_failures.)
