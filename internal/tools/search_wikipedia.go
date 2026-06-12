// Wikipedia search backend + tool.
//
// Free, no-key, no-rate-limit (beyond a polite UA) backend for the
// SearchBackend interface defined in web_search.go. Hits the public
// REST search endpoint:
//
//	https://{lang}.wikipedia.org/w/rest.php/v1/search/page
//
// The response is well-shaped JSON (no HTML scraping), and excerpts
// come pre-marked with <span class="searchmatch"> wrappers around the
// matched terms - we strip those for snippet text the model can read
// without HTML noise.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WikipediaBackend implements SearchBackend against Wikipedia's REST
// search API. Zero configuration required - the zero value is a
// usable English Wikipedia client.
//
// Throttling note: Wikipedia's REST search API has no formal rate
// limit. Their etiquette guidance (mediawiki.org "API:Etiquette") asks
// callers to identify themselves with a descriptive UA and avoid
// hammering the cluster. The standalone tool caps batched calls at
// three concurrent in-flight requests, which is well within the
// implicit budget for a single-user agent.
type WikipediaBackend struct {
	// Client is the HTTP client. Nil → http.DefaultClient.
	Client *http.Client
	// Endpoint overrides the full URL. Empty → built from Lang.
	// Tests inject the httptest server URL here.
	Endpoint string
	// Lang is the Wikipedia language subdomain. Empty → "en".
	Lang string
	// UserAgent is sent with each request. Wikipedia's user-agent
	// policy asks identifiable apps to send a descriptive UA.
	UserAgent string
	// RetryPolicy overrides the shared default. Zero value uses
	// defaultRetryPolicy(); tests inject a zero-jitter policy with an
	// injectable sleeper.
	RetryPolicy *retryPolicy
}

const (
	defaultWikipediaLang      = "en"
	defaultWikipediaUserAgent = "carlos/web_search (https://github.com/georgebuilds/carlos)"
)

// NewWikipediaBackend returns a backend with the defaults filled in.
func NewWikipediaBackend() *WikipediaBackend {
	return &WikipediaBackend{
		Lang:      defaultWikipediaLang,
		UserAgent: defaultWikipediaUserAgent,
	}
}

func (*WikipediaBackend) Name() string { return "wikipedia" }

// Search hits the REST search/page endpoint and converts pages to
// SearchResult entries. Returns an empty slice (not nil-error) when
// the endpoint returns zero pages.
func (w *WikipediaBackend) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	if max <= 0 {
		return []SearchResult{}, nil
	}
	lang := w.Lang
	if lang == "" {
		lang = defaultWikipediaLang
	}
	endpoint := w.Endpoint
	if endpoint == "" {
		endpoint = "https://" + lang + ".wikipedia.org/w/rest.php/v1/search/page"
	}
	ua := w.UserAgent
	if ua == "" {
		ua = defaultWikipediaUserAgent
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("limit", fmt.Sprintf("%d", max))
	u.RawQuery = q.Encode()

	cli := w.Client
	if cli == nil {
		cli = http.DefaultClient
	}
	policy := defaultRetryPolicy()
	if w.RetryPolicy != nil {
		policy = *w.RetryPolicy
	}
	resp, err := doWithRetry(ctx, policy, func(rctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(rctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", ua)
		return cli.Do(req)
	})
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("wikipedia: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Pages []struct {
			ID          int    `json:"id"`
			Key         string `json:"key"`
			Title       string `json:"title"`
			Excerpt     string `json:"excerpt"`
			Description string `json:"description"`
		} `json:"pages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("wikipedia: decode response: %w", err)
	}

	out := make([]SearchResult, 0, len(payload.Pages))
	for i, p := range payload.Pages {
		if i >= max {
			break
		}
		snippet := stripSearchMatchSpans(p.Excerpt)
		if snippet == "" {
			snippet = strings.TrimSpace(p.Description)
		}
		out = append(out, SearchResult{
			Rank:    i + 1,
			Title:   p.Title,
			URL:     "https://" + lang + ".wikipedia.org/wiki/" + url.PathEscape(p.Key),
			Snippet: snippet,
			Source:  "wikipedia",
		})
	}
	return out, nil
}

// stripSearchMatchSpans removes <span class="searchmatch">...</span>
// wrappers from an excerpt while keeping the inner text. Wikipedia's
// search excerpt format wraps matched terms in these spans; we let
// the existing HTML helper do the actual parsing so we don't
// reimplement HTML escaping / nesting.
func stripSearchMatchSpans(s string) string {
	if s == "" {
		return ""
	}
	return strings.TrimSpace(stripHTMLTags(s))
}

// === WikipediaSearchTool =====================================================

// WikipediaSearchTool exposes WikipediaBackend as a standalone Tool so
// the model can target Wikipedia specifically without going through
// the generic web_search aggregator. Useful for definitions,
// biographies, historical events, topic overviews.
type WikipediaSearchTool struct {
	// Backend is the concrete WikipediaBackend. Nil → constructed
	// with defaults on first Execute call.
	Backend *WikipediaBackend
	// Timeout caps each backend call. Default 15s.
	Timeout time.Duration
}

const (
	defaultWikipediaSearchTimeout    = 15 * time.Second
	defaultWikipediaSearchMaxResults = 10
	maxWikipediaSearchMaxResults     = 20
)

// NewWikipediaSearchTool returns a tool wired to a default backend.
func NewWikipediaSearchTool() *WikipediaSearchTool {
	// Process-shared backend so the standalone tool and any
	// MultiBackend-wrapped web_search route through the same instance
	// (consistent UA, no duplicate request volume against the same
	// IP).
	return &WikipediaSearchTool{Backend: sharedWikipediaBackend()}
}

func (*WikipediaSearchTool) Name() string { return "wikipedia_search" }

func (*WikipediaSearchTool) Description() string {
	return "Search Wikipedia for encyclopedia articles. Returns ranked title + Wikipedia URL + excerpt snippet. Use for definitions, biographies, historical events, topic overviews. Prefer batched queries (`queries: [\"a\", \"b\", \"c\"]`) when researching multiple terms in one call: the tool fans them out concurrently within a courtesy cap. Follow up with web_fetch to read the full article. (lang param reserved for future; uses English Wikipedia today.)"
}

func (*WikipediaSearchTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Single search query. Wikipedia matches on titles + article text. Pass either query or queries, not both."
			},
			"queries": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Batch of search queries. Each runs against the same backend and results are returned keyed by query. Prefer this when researching multiple terms."
			},
			"max_results": {
				"type": "integer",
				"description": "1-20 per query, default 10."
			},
			"lang": {
				"type": "string",
				"description": "Wikipedia language code (e.g. 'en', 'fr', 'de'). Reserved for future; currently ignored - defaults to English."
			}
		}
	}`)
}

type wikipediaSearchInput struct {
	Query      string   `json:"query"`
	Queries    []string `json:"queries"`
	MaxResults int      `json:"max_results"`
	Lang       string   `json:"lang"`
}

type wikipediaSearchOutput struct {
	Backend string         `json:"backend"`
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
}

// wikipediaBatchedOutput is the response shape when `queries` is used.
// Backend is shared; per-query results live in Blocks. Empty
// Blocks is impossible (validation rejects an empty queries list).
type wikipediaBatchedOutput struct {
	Backend string                     `json:"backend"`
	Queries []string                   `json:"queries"`
	Blocks  []batchedSearchResultBlock `json:"blocks"`
}

// wikipediaBatchConcurrency is the in-flight cap for batched
// wikipedia_search. Wikipedia has no formal rate limit; 3 keeps us
// well below any reasonable courtesy budget while still cutting batch
// wall time roughly to 1/N for N queries.
const wikipediaBatchConcurrency = 3

// Execute validates input, runs the backend, returns the JSON the
// model sees. Single-query and batched paths share the same backend
// call shape; the response envelope differs (flat vs blocks) so the
// model can route on the input shape it used. The lang input field
// is parsed but currently ignored (the backend's configured Lang
// wins) - documented in Description.
func (t *WikipediaSearchTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	queries, isBatch, err := parseQueries("wikipedia_search", input)
	if err != nil {
		return nil, err
	}
	var in wikipediaSearchInput
	_ = json.Unmarshal(input, &in) // already validated; this just pulls max_results / lang
	max := in.MaxResults
	if max <= 0 {
		max = defaultWikipediaSearchMaxResults
	}
	if max > maxWikipediaSearchMaxResults {
		max = maxWikipediaSearchMaxResults
	}

	backend := t.Backend
	if backend == nil {
		backend = NewWikipediaBackend()
	}

	timeout := t.Timeout
	if timeout == 0 {
		timeout = defaultWikipediaSearchTimeout
	}
	// In batch mode the overall budget scales with the number of
	// queries so a slow single query doesn't doom the whole batch.
	if isBatch && len(queries) > 1 {
		timeout = timeout * time.Duration(len(queries))
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if !isBatch {
		q := queries[0]
		results, err := backend.Search(cctx, q, max)
		if err != nil {
			return nil, fmt.Errorf("wikipedia_search: %w", err)
		}
		out := wikipediaSearchOutput{
			Backend: backend.Name(),
			Query:   q,
			Results: results,
		}
		return json.Marshal(out)
	}

	blocks := runConcurrentBatch(cctx, queries, wikipediaBatchConcurrency, func(rctx context.Context, q string) ([]SearchResult, error) {
		return backend.Search(rctx, q, max)
	})
	out := wikipediaBatchedOutput{
		Backend: backend.Name(),
		Queries: queries,
		Blocks:  blocks,
	}
	return json.Marshal(out)
}
