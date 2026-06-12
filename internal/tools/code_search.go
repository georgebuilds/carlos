package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// CodeSearchTool registers as `code_search` - a thin fan-out fetcher
// for the three public code-doc indexers carlos's own repo is
// published on (Codewiki / Context7 / DeepWiki). The model can use it
// to look up architectural questions about carlos itself ("how does
// the cross-frame approval reason work" → DeepWiki overview) without
// having to read the source tree.
//
// The same tool works against any GitHub repo the indexers cover, not
// just carlos's. Pass `repo: "owner/repo"` to override the default.
// Default repo is "georgebuilds/carlos" so the no-arg call surfaces
// carlos's own docs first - that's the self-reference path the model
// reaches for when the user asks how something works internally.
//
// Network egress: the tool prompts via LayeredApprover (no auto-
// approve). The user can elect "Always" for the session once they're
// comfortable; subsequent calls then go through silently.
type CodeSearchTool struct {
	// Client is injected by tests; production wires the same shape
	// WebFetchTool uses (per-request timeout, redirect cap).
	Client *http.Client
	// Endpoints maps service name to its URL template. {owner} and
	// {repo} are substituted from the input. Empty = use built-in
	// defaults below.
	Endpoints map[string]string
	// Default timeout per service. Zero falls back to
	// defaultCodeSearchTimeout below.
	Timeout time.Duration
}

const (
	// defaultCodeSearchTimeout caps one indexer round-trip. Each
	// service is fetched in its own goroutine so a slow indexer
	// doesn't block the others past this budget.
	defaultCodeSearchTimeout = 5 * time.Second
	// defaultCodeSearchRepo is the self-reference target: omit `repo`
	// in the input and the tool queries carlos's own pages on the
	// indexers.
	defaultCodeSearchRepo = "georgebuilds/carlos"
	// codeSearchUserAgent identifies carlos to the indexers so they
	// can attribute traffic. Distinct from webFetchUserAgent so an
	// indexer's robots.txt can grant code_search a different policy
	// than the general fetcher.
	codeSearchUserAgent = "carlos-code_search/1.0 (+https://georgebuilds.github.io/carlos)"
)

// defaultEndpoints are the URL templates for the three indexers
// carlos's repo was submitted to. {owner}/{repo} are substituted at
// fetch time. Each service has its own conventions:
//
//   - Codewiki: code documentation site, repo-rooted at /{owner}/{repo}
//   - Context7: hosts an llms.txt manifest per repo at /{owner}/{repo}/llms.txt
//   - DeepWiki: AI-generated wiki, repo-rooted at /{owner}/{repo}
//
// The tool fetches each base URL; the indexers' HTML responses are
// crawled into plain text by the same WebFetchTool helpers
// (extractFromContentType) so the model sees uniform output.
var defaultCodeSearchEndpoints = map[string]string{
	"codewiki": "https://codewiki.dev/{owner}/{repo}",
	"context7": "https://context7.com/{owner}/{repo}/llms.txt",
	"deepwiki": "https://deepwiki.com/{owner}/{repo}",
}

// NewCodeSearchTool constructs the tool with default endpoints.
// Callers can override Endpoints + Timeout + Client after construction
// for tests or for custom indexer configurations.
func NewCodeSearchTool() *CodeSearchTool {
	return &CodeSearchTool{}
}

func (*CodeSearchTool) Name() string { return "code_search" }

func (*CodeSearchTool) Description() string {
	return "Fan-out fetch of carlos's own architecture documentation (and any other repo's) from Codewiki, Context7, and DeepWiki. Use when the user asks how carlos works internally, where a feature is implemented, or for any code-research question against a public repo carlos has been indexed under. Default repo is georgebuilds/carlos so a no-arg call returns carlos's own pages. Pass `repo: \"owner/repo\"` to query a different one, or `services: [\"deepwiki\"]` to skip the slower indexers. Network egress; prompts on first use."
}

func (*CodeSearchTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Optional question or topic. Used as a hint in the response framing but not (yet) as a server-side filter - the indexers return repo-rooted overviews. Pass to help the model focus its read of the merged output."
			},
			"repo": {
				"type": "string",
				"description": "GitHub-style owner/repo. Defaults to georgebuilds/carlos so a no-arg call returns carlos's own indexed pages."
			},
			"services": {
				"type": "array",
				"items": {"type": "string", "enum": ["codewiki", "context7", "deepwiki"]},
				"description": "Optional subset filter. Empty / omitted = all three. Use to skip a service that's slow or known-broken for a given repo."
			}
		}
	}`)
}

type codeSearchInput struct {
	Query    string   `json:"query"`
	Repo     string   `json:"repo"`
	Services []string `json:"services"`
}

// codeSearchResult is the aggregated envelope returned to the model.
// Each per-service result carries its URL + outcome so the model can
// decide which entries to read and which to ignore (e.g. a 404 on
// Codewiki when the repo wasn't indexed yet).
type codeSearchResult struct {
	Query    string                 `json:"query,omitempty"`
	Repo     string                 `json:"repo"`
	Services []codeSearchServiceHit `json:"services"`
	Note     string                 `json:"note,omitempty"`
}

type codeSearchServiceHit struct {
	Service string `json:"service"`
	URL     string `json:"url"`
	Status  int    `json:"status"`
	Title   string `json:"title,omitempty"`
	Excerpt string `json:"excerpt,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Execute fans out to the configured indexers concurrently. Each
// fetch runs in its own goroutine with a per-request context timeout
// so a slow service can't stall the whole call past defaultTimeout.
func (t *CodeSearchTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	var in codeSearchInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fmt.Errorf("code_search: parse input: %w", err)
		}
	}
	repo := strings.TrimSpace(in.Repo)
	if repo == "" {
		repo = defaultCodeSearchRepo
	}
	owner, repoName, ok := splitOwnerRepo(repo)
	if !ok {
		return nil, fmt.Errorf("code_search: repo must be owner/repo, got %q", repo)
	}

	endpoints := t.Endpoints
	if len(endpoints) == 0 {
		endpoints = defaultCodeSearchEndpoints
	}
	filter := filterServices(in.Services, endpoints)

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = defaultCodeSearchTimeout
	}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	results := make([]codeSearchServiceHit, len(filter))
	var wg sync.WaitGroup
	for i, name := range filter {
		i, name := i, name
		tmpl := endpoints[name]
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = t.fetchOne(ctx, client, timeout, name, tmpl, owner, repoName)
		}()
	}
	wg.Wait()

	out := codeSearchResult{
		Query:    in.Query,
		Repo:     repo,
		Services: results,
	}
	if anyOK := anyHitOK(results); !anyOK {
		out.Note = "no indexer returned content for this repo. The repo may not be indexed yet on these services."
	}
	return json.Marshal(out)
}

// fetchOne issues one request, captures the outcome, and returns the
// per-service envelope. Never panics; per-service errors land in the
// .Error field so the caller can decide whether to skip or report.
func (t *CodeSearchTool) fetchOne(
	ctx context.Context,
	client *http.Client,
	timeout time.Duration,
	service, tmpl, owner, repoName string,
) codeSearchServiceHit {
	u := fillCodeSearchTemplate(tmpl, owner, repoName)
	hit := codeSearchServiceHit{Service: service, URL: u}

	if _, err := url.Parse(u); err != nil {
		hit.Error = "bad URL template: " + err.Error()
		return hit
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		hit.Error = "build request: " + err.Error()
		return hit
	}
	req.Header.Set("User-Agent", codeSearchUserAgent)
	req.Header.Set("Accept", "text/html,text/plain,text/markdown,text/*;q=0.9,*/*;q=0.1")
	resp, err := client.Do(req)
	if err != nil {
		hit.Error = err.Error()
		return hit
	}
	defer closeBody(resp)
	hit.Status = resp.StatusCode
	if resp.StatusCode >= 400 {
		hit.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return hit
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/html"
	}
	if err := requireTextContentType(ct); err != nil {
		hit.Error = err.Error()
		return hit
	}
	limited := io.LimitReader(resp.Body, defaultWebFetchMaxBodyBytes+1)
	bodyBuf, err := io.ReadAll(limited)
	if err != nil {
		hit.Error = "read body: " + err.Error()
		return hit
	}
	if int64(len(bodyBuf)) > defaultWebFetchMaxBodyBytes {
		bodyBuf = bodyBuf[:defaultWebFetchMaxBodyBytes]
	}
	title, text := extractFromContentType(ct, bodyBuf)
	hit.Title = title
	hit.Excerpt = excerpt(text, 4096)
	return hit
}

// fillCodeSearchTemplate substitutes {owner} and {repo} in the URL
// template. Repeated placeholders are all replaced (some indexers
// might use {owner}/{repo}/{owner}-style canonical URLs in the
// future). Empty template returns "".
func fillCodeSearchTemplate(tmpl, owner, repo string) string {
	if tmpl == "" {
		return ""
	}
	tmpl = strings.ReplaceAll(tmpl, "{owner}", owner)
	tmpl = strings.ReplaceAll(tmpl, "{repo}", repo)
	return tmpl
}

// splitOwnerRepo accepts "owner/repo" or the legacy github URL form
// "github.com/owner/repo". Trailing slashes are tolerated.
//
// Rejects any input that carries trailing path segments beyond
// owner/repo (e.g. "owner/repo/issues/12"). The previous SplitN(..., 3)
// silently dropped extras; that would let a GitHub deep-link feed the
// fan-out fetcher a repo name it never typed and produce nonsense
// indexer URLs.
func splitOwnerRepo(s string) (owner, repo string, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "github.com/")
	// Strip trailing slashes before counting so "owner/repo/" still
	// reads as 2 segments.
	s = strings.TrimRight(s, "/")
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// filterServices picks which services to query given the optional
// services filter. Empty filter = all configured endpoints. Unknown
// names in the filter are silently dropped so a stale model that
// passes "google" doesn't blow up the whole call.
func filterServices(want []string, endpoints map[string]string) []string {
	if len(want) == 0 {
		out := make([]string, 0, len(endpoints))
		for k := range endpoints {
			out = append(out, k)
		}
		sortStringsCS(out)
		return out
	}
	have := make(map[string]bool, len(want))
	for _, s := range want {
		s = strings.ToLower(strings.TrimSpace(s))
		if _, ok := endpoints[s]; ok {
			have[s] = true
		}
	}
	out := make([]string, 0, len(have))
	for k := range have {
		out = append(out, k)
	}
	sortStringsCS(out)
	return out
}

// anyHitOK reports whether at least one service returned content.
// Used to attach a "no indexer hit" hint to the response so the model
// doesn't try to read a wall of error rows.
func anyHitOK(hits []codeSearchServiceHit) bool {
	for _, h := range hits {
		if h.Error == "" && h.Excerpt != "" {
			return true
		}
	}
	return false
}

// excerpt trims body text to a max byte budget, preserving rune
// boundaries. Adds a truncation marker when the cut applies.
func excerpt(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "\n\n(... excerpt truncated)"
}

// sortStringsCS is the package-local string sorter so this file
// doesn't import "sort" for one call. Stable insertion sort is fine
// for the tiny inputs (≤ 3 services).
func sortStringsCS(s []string) {
	for i := 1; i < len(s); i++ {
		j := i
		for j > 0 && s[j-1] > s[j] {
			s[j-1], s[j] = s[j], s[j-1]
			j--
		}
	}
}
