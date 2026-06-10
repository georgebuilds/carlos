// Wikipedia search backend + tool.
//
// Free, no-key, no-rate-limit (beyond a polite UA) backend for the
// SearchBackend interface defined in web_search.go. Hits the public
// REST search endpoint:
//
//   https://{lang}.wikipedia.org/w/rest.php/v1/search/page
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", ua)

	cli := w.Client
	if cli == nil {
		cli = http.DefaultClient
	}
	resp, err := cli.Do(req)
	if err != nil {
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
	return "Search Wikipedia for encyclopedia articles. Returns ranked title + Wikipedia URL + excerpt snippet. Use for definitions, biographies, historical events, topic overviews. Follow up with web_fetch to read the full article. (lang param reserved for future; uses English Wikipedia today.)"
}

func (*WikipediaSearchTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "The search query. Wikipedia matches on titles + article text."
			},
			"max_results": {
				"type": "integer",
				"description": "1-20, default 10."
			},
			"lang": {
				"type": "string",
				"description": "Wikipedia language code (e.g. 'en', 'fr', 'de'). Reserved for future; currently ignored - defaults to English."
			}
		},
		"required": ["query"]
	}`)
}

type wikipediaSearchInput struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
	Lang       string `json:"lang"`
}

type wikipediaSearchOutput struct {
	Backend string         `json:"backend"`
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
}

// Execute validates input, runs the backend, returns the JSON the
// model sees. The lang input field is parsed but currently ignored
// (the backend's configured Lang wins) - documented in Description.
func (t *WikipediaSearchTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	var in wikipediaSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("wikipedia_search: parse input: %w", err)
	}
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return nil, fmt.Errorf("wikipedia_search: empty query")
	}
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
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

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
