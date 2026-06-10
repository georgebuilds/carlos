// Phase 11 slice 11b - web_search tool.
//
// The model can now run actual web searches, not just fetch a known
// URL (slice 11a). Three pluggable backends; the factory picks
// based on environment / config:
//
//   1. Brave Search API (env BRAVE_API_KEY) - best quality + has an
//      explicit "we welcome non-commercial use" stance.
//   2. SearXNG (env SEARXNG_URL) - self-hosted metasearch; the
//      privacy-respecting option for users who run their own.
//   3. DuckDuckGo HTML scrape - no API key, best-effort fallback.
//      Documented as fragile; HTML can change.
//
// The tool stays a thin adapter over the SearchBackend interface so a
// future Bing / Kagi / Tavily backend slots in by writing one type.
//
// Architectural commitments (mirroring web_fetch):
//   - Pure-Go, no CGO, minimal deps.
//   - Result cap (default 10, hard cap 20) so a runaway model can't
//     pull a hundred snippets into context.
//   - Per-request timeout (default 10s) - search APIs are usually
//     <500ms; a slow one is almost always a hung connection.
//   - Errors carry the backend name so the user / model knows which
//     route failed.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

// SearchResult is one entry in a search response. Fields stay
// JSON-stable so model prompts can rely on the shape.
//
// Source names the contributing backend ("brave", "arxiv", "wikipedia", …)
// when the result came up through a MultiBackend fan-out. Single-backend
// flows leave it set to the backend name too — uniform shape.
type SearchResult struct {
	Rank        int    `json:"rank"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	PublishedAt string `json:"published_at,omitempty"`
	Source      string `json:"source,omitempty"`
}

// SearchBackend is the seam between the tool and the concrete search
// service. Implementations: Brave, SearXNG, DuckDuckGo.
type SearchBackend interface {
	// Name identifies the backend in error messages + the tool's JSON
	// response so users / models know where the results came from.
	Name() string
	// Search returns up to max results. The implementation is free to
	// return fewer (e.g. the backend returned 6 hits for a niche
	// query); never more.
	Search(ctx context.Context, query string, max int) ([]SearchResult, error)
}

// WebSearchTool implements tools.Tool for `web_search`.
type WebSearchTool struct {
	// Backend is the concrete search service. Required; the factory
	// (NewWebSearchTool) picks based on env / config.
	Backend SearchBackend
	// Timeout caps each backend call. Default 10s.
	Timeout time.Duration
}

const (
	defaultWebSearchTimeout    = 10 * time.Second
	defaultWebSearchMaxResults = 10
	maxWebSearchMaxResults     = 20
)

// Process-shared singletons for the specialty backends. arxiv's 1-req-
// per-3s guidance is per-IP, so two backend instances in the same
// process would race and stand a real chance of getting 429'd. Both
// the MultiBackend wrapping web_search and the standalone arxiv_search
// / wikipedia_search tools route through these singletons.
var (
	sharedArxivOnce     sync.Once
	sharedArxivInstance *ArxivBackend
	sharedWikipediaOnce sync.Once
	sharedWikipediaIns  *WikipediaBackend
)

func sharedArxivBackend() *ArxivBackend {
	sharedArxivOnce.Do(func() { sharedArxivInstance = NewArxivBackend() })
	return sharedArxivInstance
}

func sharedWikipediaBackend() *WikipediaBackend {
	sharedWikipediaOnce.Do(func() { sharedWikipediaIns = NewWikipediaBackend() })
	return sharedWikipediaIns
}

// NewWebSearchTool builds the tool's backend tree. The factory picks
// the primary general-web backend by env precedence (Brave > SearXNG >
// DuckDuckGo), then layers in optional specialty backends on top:
// arxiv (default on) and wikipedia (default on). When at least one
// specialty backend is enabled the primary + specialties get wrapped
// in a MultiBackend that fans out concurrently and merges by
// interleaved rank with URL dedup.
//
// Opt-outs: CARLOS_DISABLE_ARXIV=1, CARLOS_DISABLE_WIKIPEDIA=1. With
// both set, the factory returns the bare primary as before — byte
// identical to the pre-multi behavior.
func NewWebSearchTool() *WebSearchTool {
	primary := selectPrimaryBackend()
	var aux []SearchBackend
	if os.Getenv("CARLOS_DISABLE_ARXIV") != "1" {
		aux = append(aux, sharedArxivBackend())
	}
	if os.Getenv("CARLOS_DISABLE_WIKIPEDIA") != "1" {
		aux = append(aux, sharedWikipediaBackend())
	}
	var backend SearchBackend = primary
	if len(aux) > 0 {
		backend = NewMultiBackend(primary, aux...)
	}
	return &WebSearchTool{Backend: backend}
}

// selectPrimaryBackend resolves the general-web backend per the
// long-standing env precedence. Extracted so the factory above can
// stay focused on the multi-wiring shape.
func selectPrimaryBackend() SearchBackend {
	switch {
	case os.Getenv("BRAVE_API_KEY") != "":
		return &BraveBackend{APIKey: os.Getenv("BRAVE_API_KEY")}
	case os.Getenv("SEARXNG_URL") != "":
		return &SearXNGBackend{InstanceURL: os.Getenv("SEARXNG_URL")}
	default:
		return &DuckDuckGoBackend{}
	}
}

func (*WebSearchTool) Name() string { return "web_search" }

func (*WebSearchTool) Description() string {
	return "Search the web across multiple sources (general web + arxiv + wikipedia by default) and return ranked title + URL + snippet results. Each result includes a `source` field naming the backend that produced it. Use for current events, fact-checking claims, finding documentation, locating canonical sources. Pass `backends: [\"arxiv\"]` (or similar subset) to restrict the query when you know which source you want — saves time and avoids noise. The actual content of pages requires a follow-up web_fetch."
}

func (*WebSearchTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "The search query. Be specific; phrase as you'd type it into Google."
			},
			"max_results": {
				"type": "integer",
				"description": "1-20, default 10. Smaller is usually better - pick the top few and follow up with web_fetch."
			},
			"backends": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Optional subset of backends to query, e.g. [\"arxiv\"] or [\"brave\",\"wikipedia\"]. Names: brave, searxng, duckduckgo, arxiv, wikipedia. Omit or empty = all configured backends. Unknown names are silently ignored."
			},
			"per_backend_max": {
				"type": "integer",
				"description": "Optional per-backend result cap. 0 or absent uses max_results for each backend (then interleave-merges and trims). Set lower to keep any one backend from dominating."
			}
		},
		"required": ["query"]
	}`)
}

type webSearchInput struct {
	Query         string   `json:"query"`
	MaxResults    int      `json:"max_results"`
	Backends      []string `json:"backends,omitempty"`
	PerBackendMax int      `json:"per_backend_max,omitempty"`
}

type webSearchOutput struct {
	Backend         string            `json:"backend"`
	Backends        []string          `json:"backends,omitempty"`
	Query           string            `json:"query"`
	Results         []SearchResult    `json:"results"`
	PartialFailures map[string]string `json:"partial_failures,omitempty"`
}

// Execute validates input, calls the backend, returns the JSON the
// model sees. Backend errors propagate with the backend name in the
// wrapped error so a "brave: rate limited" or "duckduckgo: parse
// failure" reads at a glance.
func (t *WebSearchTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	if t.Backend == nil {
		return nil, errors.New("web_search: no backend configured (set BRAVE_API_KEY, SEARXNG_URL, or accept the DuckDuckGo fallback)")
	}
	var in webSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("web_search: parse input: %w", err)
	}
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return nil, errors.New("web_search: empty query")
	}
	max := in.MaxResults
	if max <= 0 {
		max = defaultWebSearchMaxResults
	}
	if max > maxWebSearchMaxResults {
		max = maxWebSearchMaxResults
	}

	timeout := t.Timeout
	if timeout == 0 {
		timeout = defaultWebSearchTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		results []SearchResult
		err     error
	)
	// Multi backend honors the `backends` subset + `per_backend_max`
	// fields. A single non-multi backend silently ignores them — the
	// model can still pass them when it doesn't know which shape the
	// install ended up with, and the call still works.
	if multi, ok := t.Backend.(*MultiBackend); ok {
		results, err = multi.SearchSubset(cctx, q, max, in.Backends, in.PerBackendMax)
	} else {
		results, err = t.Backend.Search(cctx, q, max)
	}
	if err != nil {
		return nil, fmt.Errorf("web_search/%s: %w", t.Backend.Name(), err)
	}

	out := webSearchOutput{
		Backend: t.Backend.Name(),
		Query:   q,
		Results: results,
	}
	if multi, ok := t.Backend.(*MultiBackend); ok {
		out.Backends = multi.Names()
		if errs := multi.LastErrors(); len(errs) > 0 {
			out.PartialFailures = make(map[string]string, len(errs))
			for name, e := range errs {
				out.PartialFailures[name] = e.Error()
			}
		}
	}
	return json.Marshal(out)
}

// === Brave backend ==========================================================

// BraveBackend hits the Brave Search API. Pricing / quota at
// https://brave.com/search/api/ - non-commercial tier is free.
type BraveBackend struct {
	APIKey string
	// Endpoint overrides the API URL. Empty → default. Set by tests.
	Endpoint string
	// Client overrides the HTTP client. Empty → http.DefaultClient.
	Client *http.Client
}

func (*BraveBackend) Name() string { return "brave" }

func (b *BraveBackend) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	endpoint := b.Endpoint
	if endpoint == "" {
		endpoint = "https://api.search.brave.com/res/v1/web/search"
	}
	u, _ := url.Parse(endpoint)
	q := u.Query()
	q.Set("q", query)
	q.Set("count", fmt.Sprintf("%d", max))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.APIKey)

	cli := b.Client
	if cli == nil {
		cli = http.DefaultClient
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				Age         string `json:"age,omitempty"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	out := make([]SearchResult, 0, len(payload.Web.Results))
	for i, r := range payload.Web.Results {
		if i >= max {
			break
		}
		out = append(out, SearchResult{
			Rank:        i + 1,
			Title:       r.Title,
			URL:         r.URL,
			Snippet:     stripHTMLTags(r.Description),
			PublishedAt: r.Age,
			Source:      "brave",
		})
	}
	return out, nil
}

// === SearXNG backend ========================================================

// SearXNGBackend hits a user-supplied SearXNG instance. The format=json
// endpoint is built into vanilla SearXNG; most public instances allow it.
type SearXNGBackend struct {
	InstanceURL string // e.g. "https://searx.example.com"
	Client      *http.Client
}

func (*SearXNGBackend) Name() string { return "searxng" }

func (s *SearXNGBackend) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	if s.InstanceURL == "" {
		return nil, errors.New("InstanceURL not set")
	}
	u, err := url.Parse(strings.TrimRight(s.InstanceURL, "/") + "/search")
	if err != nil {
		return nil, fmt.Errorf("parse instance URL: %w", err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// SearXNG is sensitive to bot UAs; identify ourselves honestly.
	req.Header.Set("User-Agent", "carlos/web_search (https://github.com/georgebuilds/carlos)")

	cli := s.Client
	if cli == nil {
		cli = http.DefaultClient
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			PublishedDate string `json:"publishedDate,omitempty"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	out := make([]SearchResult, 0, max)
	for i, r := range payload.Results {
		if i >= max {
			break
		}
		out = append(out, SearchResult{
			Rank:        i + 1,
			Title:       r.Title,
			URL:         r.URL,
			Snippet:     r.Content,
			PublishedAt: r.PublishedDate,
			Source:      "searxng",
		})
	}
	return out, nil
}

// === DuckDuckGo HTML backend ================================================

// DuckDuckGoBackend scrapes duckduckgo.com/html/. No API key needed.
// Documented as fragile - HTML structure can change without notice;
// this backend ships with explicit "best-effort" semantics.
type DuckDuckGoBackend struct {
	Endpoint string // override for tests
	Client   *http.Client
}

func (*DuckDuckGoBackend) Name() string { return "duckduckgo" }

func (d *DuckDuckGoBackend) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	endpoint := d.Endpoint
	if endpoint == "" {
		endpoint = "https://duckduckgo.com/html/"
	}
	u, _ := url.Parse(endpoint)
	q := u.Query()
	q.Set("q", query)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	// DuckDuckGo blocks blank UAs; use a believable browser UA.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; carlos/1.0)")
	req.Header.Set("Accept", "text/html")

	cli := d.Client
	if cli == nil {
		cli = http.DefaultClient
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	results, err := parseDuckDuckGoHTML(string(body), max)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return results, nil
}

// parseDuckDuckGoHTML walks the rendered SERP looking for
// <a class="result__a"> entries (title + URL) paired with
// <a class="result__snippet"> entries. DDG's HTML changes
// occasionally; if structure breaks we surface a clean parse error
// and the caller falls through to a different backend.
func parseDuckDuckGoHTML(body string, max int) ([]SearchResult, error) {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	var out []SearchResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(out) >= max {
			return
		}
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "result") {
			r := extractDuckDuckGoResult(n)
			if r.URL != "" && r.Title != "" {
				r.Rank = len(out) + 1
				r.Source = "duckduckgo"
				out = append(out, r)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(out) == 0 {
		return nil, errors.New("no results parsed (HTML may have changed)")
	}
	return out, nil
}

func extractDuckDuckGoResult(n *html.Node) SearchResult {
	var r SearchResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch {
			case n.Data == "a" && hasClass(n, "result__a"):
				r.Title = collectText(n)
				if href := attrVal(n, "href"); href != "" {
					r.URL = normalizeDuckDuckGoURL(href)
				}
			case (n.Data == "a" && hasClass(n, "result__snippet")) ||
				(n.Data == "div" && hasClass(n, "result__snippet")):
				if r.Snippet == "" {
					r.Snippet = strings.TrimSpace(collectText(n))
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return r
}

// normalizeDuckDuckGoURL handles DDG's "uddg" redirect wrapping:
// `//duckduckgo.com/l/?uddg=ENCODED&...`. Extract the wrapped target.
// Leave plain URLs untouched.
func normalizeDuckDuckGoURL(href string) string {
	if !strings.Contains(href, "/l/?") {
		return href
	}
	if i := strings.Index(href, "uddg="); i >= 0 {
		raw := href[i+len("uddg="):]
		if amp := strings.Index(raw, "&"); amp >= 0 {
			raw = raw[:amp]
		}
		if decoded, err := url.QueryUnescape(raw); err == nil {
			return decoded
		}
	}
	return href
}

// === HTML helpers (small; used by Brave snippet stripping + DDG parse) =====

// stripHTMLTags removes tags from a string. Brave's description field
// occasionally contains <strong> wrappers around matched terms; the
// model doesn't want HTML in its prompt.
func stripHTMLTags(s string) string {
	if !strings.ContainsAny(s, "<>") {
		return s
	}
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	return strings.TrimSpace(collectText(doc))
}

// collectText concatenates every TextNode descendant of n with single
// spaces. Drops script/style content.
func collectText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	// Collapse runs of whitespace.
	return strings.Join(strings.Fields(b.String()), " ")
}

func hasClass(n *html.Node, name string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == name {
					return true
				}
			}
		}
	}
	return false
}

func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
