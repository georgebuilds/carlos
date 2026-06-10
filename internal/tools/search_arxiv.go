// Phase 11 slice 11c - arxiv search backend + tool.
//
// arxiv.org publishes an open Atom API at export.arxiv.org/api/query.
// No key, no quota - but the operators ask for at most one request
// every 3 seconds. We enforce that internally with a mutex + last-call
// timestamp so a runaway model can't burn through arxiv's politeness
// budget.
//
// Architectural notes:
//   - Backend speaks the existing SearchBackend interface so it slots
//     into the same tool surface as Brave / SearXNG / DuckDuckGo.
//   - The model-facing tool is exposed as `arxiv_search` (distinct
//     from `web_search`) because the result domain is narrower and the
//     rate limit makes blending it into the general search backend
//     pool dangerous - one bad query and the next general search blocks
//     for 3s.
//   - All snippet/title text is whitespace-normalized via strings.Fields
//     so the model never sees the multi-line indented blobs that come
//     out of <summary> tags.
package tools

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ArxivBackend implements SearchBackend against the arxiv.org Atom API.
// Free, no key. Per arxiv guidance, issue at most 1 request every 3
// seconds - enforced internally with a sync.Mutex + lastCallAt
// timestamp (no new deps).
type ArxivBackend struct {
	Client      *http.Client
	Endpoint    string        // default https://export.arxiv.org/api/query
	UserAgent   string        // default "carlos/web_search (https://github.com/georgebuilds/carlos)"
	MinInterval time.Duration // default 3*time.Second, the arxiv guideline
	// RetryPolicy overrides the shared default. Zero value uses
	// defaultRetryPolicy(); tests inject a zero-jitter policy.
	RetryPolicy *retryPolicy

	mu         sync.Mutex
	lastCallAt time.Time
}

const (
	defaultArxivEndpoint    = "https://export.arxiv.org/api/query"
	defaultArxivUserAgent   = "carlos/web_search (https://github.com/georgebuilds/carlos)"
	defaultArxivMinInterval = 3 * time.Second
	arxivSnippetMaxRunes    = 280
	arxivErrorBodyLimit     = 256
)

// NewArxivBackend returns an ArxivBackend with the documented defaults
// filled in. Callers may override any field before the first Search
// call (e.g. tests swap Endpoint + MinInterval).
func NewArxivBackend() *ArxivBackend {
	return &ArxivBackend{
		Endpoint:    defaultArxivEndpoint,
		UserAgent:   defaultArxivUserAgent,
		MinInterval: defaultArxivMinInterval,
	}
}

// Name implements SearchBackend.
func (*ArxivBackend) Name() string { return "arxiv" }

// Search hits the arxiv Atom API and decodes up to max entries. The
// rate-limit gate at the top blocks (or honors ctx cancellation) so
// concurrent calls serialize through MinInterval.
func (a *ArxivBackend) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	// Rate-limit gate: at most one in-flight call every MinInterval.
	// The lock-wait-relock dance lets us honor ctx during the wait
	// without holding the mutex while sleeping.
	minInterval := a.MinInterval
	if minInterval <= 0 {
		minInterval = defaultArxivMinInterval
	}
	a.mu.Lock()
	wait := time.Until(a.lastCallAt.Add(minInterval))
	if wait > 0 {
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		a.mu.Lock()
	}
	a.lastCallAt = time.Now()
	a.mu.Unlock()

	if max <= 0 {
		return []SearchResult{}, nil
	}

	endpoint := a.Endpoint
	if endpoint == "" {
		endpoint = defaultArxivEndpoint
	}
	ua := a.UserAgent
	if ua == "" {
		ua = defaultArxivUserAgent
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}
	q := u.Query()
	q.Set("search_query", "all:"+query)
	q.Set("max_results", fmt.Sprintf("%d", max))
	q.Set("sortBy", "relevance")
	q.Set("sortOrder", "descending")
	u.RawQuery = q.Encode()

	cli := a.Client
	if cli == nil {
		cli = http.DefaultClient
	}
	policy := defaultRetryPolicy()
	if a.RetryPolicy != nil {
		policy = *a.RetryPolicy
	}
	resp, err := doWithRetry(ctx, policy, func(rctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(rctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Accept", "application/atom+xml")
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, arxivErrorBodyLimit))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var feed arxivFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("decode atom: %w", err)
	}

	out := make([]SearchResult, 0, len(feed.Entries))
	for i, e := range feed.Entries {
		if i >= max {
			break
		}
		// strings.Fields collapses every flavor of whitespace (tabs,
		// newlines, multi-space) into single spaces. Atom <title> and
		// <summary> from arxiv routinely contain wrapped lines.
		title := strings.Join(strings.Fields(e.Title), " ")
		summary := strings.Join(strings.Fields(e.Summary), " ")
		out = append(out, SearchResult{
			Rank:        i + 1,
			Title:       title,
			URL:         strings.TrimSpace(e.ID),
			Snippet:     truncateRunes(summary, arxivSnippetMaxRunes),
			PublishedAt: strings.TrimSpace(e.Published),
			Source:      "arxiv",
		})
	}
	return out, nil
}

// arxivFeed / arxivEntry are the minimal XML shape we need from the
// Atom response. encoding/xml is namespace-tolerant when no tag prefix
// is supplied, so we don't have to spell out the atom: namespace.
type arxivFeed struct {
	XMLName xml.Name     `xml:"feed"`
	Entries []arxivEntry `xml:"entry"`
}

type arxivEntry struct {
	Title     string `xml:"title"`
	ID        string `xml:"id"`
	Summary   string `xml:"summary"`
	Published string `xml:"published"`
}

// truncateRunes returns s capped to maxRunes runes; longer strings get
// the last rune replaced with U+2026 (…) so the model can see the
// truncation. Operates on runes so we never slice a multi-byte
// codepoint in half.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}

// === ArxivSearchTool (model-facing wrapper) =================================

// ArxivSearchTool wraps an ArxivBackend (or any SearchBackend) as a
// Tool the model can call. Backend is an interface for testability;
// production is *ArxivBackend.
type ArxivSearchTool struct {
	Backend SearchBackend
	Timeout time.Duration // default 30s (rate-limited so generous)
}

const (
	defaultArxivSearchTimeout    = 30 * time.Second
	defaultArxivSearchMaxResults = 10
	maxArxivSearchMaxResults     = 20
)

// NewArxivSearchTool wires the process-shared arxiv backend behind the
// tool. Sharing matters because arxiv's 1-req-per-3s rate-limit
// guidance is per-IP, not per-instance — a separate backend here would
// race with the MultiBackend-wrapped web_search and risk a 429.
func NewArxivSearchTool() *ArxivSearchTool {
	return &ArxivSearchTool{Backend: sharedArxivBackend()}
}

func (*ArxivSearchTool) Name() string { return "arxiv_search" }

func (*ArxivSearchTool) Description() string {
	return "Search arxiv.org for scientific papers / preprints. Returns ranked title + arxiv URL + abstract snippet results. Use for ML/CS/physics/math research and preprint hunts. Slow (~3s/call due to arxiv's rate limit). Prefer batched queries (`queries: [\"a\", \"b\", \"c\"]`) when researching multiple terms - they are serialized internally with the required 3s spacing so the wall time is roughly N*3s. Follow up with web_fetch to read the PDF or HTML version."
}

func (*ArxivSearchTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Single arxiv search query. Use natural language or arxiv field prefixes (e.g. 'ti:transformer', 'au:hinton'). Plain queries match across all fields. Pass either query or queries, not both."
			},
			"queries": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Batch of arxiv search queries. Each runs sequentially with arxiv's required 3s spacing between calls and results come back keyed by query. Prefer this when researching multiple terms."
			},
			"max_results": {
				"type": "integer",
				"description": "1-20 per query, default 10. Each call is rate-limited to 1 request per 3 seconds against arxiv."
			}
		}
	}`)
}

type arxivSearchInput struct {
	Query      string   `json:"query"`
	Queries    []string `json:"queries"`
	MaxResults int      `json:"max_results"`
}

type arxivSearchOutput struct {
	Backend string         `json:"backend"`
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
}

// arxivBatchedOutput is the response shape when `queries` is used.
type arxivBatchedOutput struct {
	Backend string                     `json:"backend"`
	Queries []string                   `json:"queries"`
	Blocks  []batchedSearchResultBlock `json:"blocks"`
}

// Execute validates input, calls the backend under a generous ctx
// timeout (the rate limit alone can eat ~3s per call), wraps results
// into the JSON the model sees. Batched mode runs queries serially
// because the backend's own rate-limit gate would serialize them
// anyway - making that visible at this layer documents the
// constraint.
func (t *ArxivSearchTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	if t.Backend == nil {
		return nil, errors.New("arxiv_search: no backend configured")
	}
	queries, isBatch, err := parseQueries("arxiv_search", input)
	if err != nil {
		return nil, err
	}
	var in arxivSearchInput
	_ = json.Unmarshal(input, &in) // already validated; this just pulls max_results
	max := in.MaxResults
	if max <= 0 {
		max = defaultArxivSearchMaxResults
	}
	if max > maxArxivSearchMaxResults {
		max = maxArxivSearchMaxResults
	}

	timeout := t.Timeout
	if timeout == 0 {
		timeout = defaultArxivSearchTimeout
	}
	// In batch mode, scale the budget with the number of queries plus
	// the per-call 3s gate so a long batch doesn't trip the timeout
	// before the gate releases.
	if isBatch && len(queries) > 1 {
		timeout = timeout + time.Duration(len(queries))*defaultArxivMinInterval
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if !isBatch {
		q := queries[0]
		results, err := t.Backend.Search(cctx, q, max)
		if err != nil {
			return nil, fmt.Errorf("arxiv_search/%s: %w", t.Backend.Name(), err)
		}
		out := arxivSearchOutput{
			Backend: t.Backend.Name(),
			Query:   q,
			Results: results,
		}
		return json.Marshal(out)
	}

	// Serial batch. The backend's own MinInterval gate enforces the
	// 3s spacing internally; the helper just sequences the calls.
	blocks := runSerialBatch(cctx, queries, func(rctx context.Context, q string) ([]SearchResult, error) {
		return t.Backend.Search(rctx, q, max)
	})
	out := arxivBatchedOutput{
		Backend: t.Backend.Name(),
		Queries: queries,
		Blocks:  blocks,
	}
	return json.Marshal(out)
}
