// Phase 11 slice 11d - gh_search tool.
//
// GHSearchTool wraps the `gh search` CLI so the model can query
// GitHub's code / repos / issues / prs indexes. It is intentionally
// NOT a SearchBackend (web_search.go) because the shape is different:
// language + owner filters, four distinct kinds with their own field
// sets, and the snippets/titles each kind needs.
//
// Why shell out instead of calling the REST API directly:
//
//   - Auth: the user already has gh authenticated locally
//     (AGENTS.md: georgebuilds). No PAT plumbing, no token-in-env
//     leakage paths.
//   - Rate-limit handling: gh transparently uses the authenticated
//     quota (5000/hr) rather than the anonymous 60/hr ceiling.
//   - Forward compat: if GitHub's search API endpoints shift, gh
//     adapts; we don't.
//
// The Runner seam is the test surface - a fakeGHRunner records args
// and replays canned bytes, so the tests never touch the network or
// the real gh binary.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GHRunner is the seam between GHSearchTool and the actual gh
// process. Production wires realGHRunner{}; tests inject a fake that
// records args and returns canned bytes.
type GHRunner interface {
	Run(ctx context.Context, args []string) ([]byte, error)
}

// realGHRunner is the production implementation: spawn `gh` with the
// supplied args, capture stdout, surface stderr in error text so
// "rate limit exceeded" or "authentication required" reads at a
// glance.
type realGHRunner struct{}

func (realGHRunner) Run(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh: %w (stderr: %s)", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil
}

// GHSearchTool wraps the 'gh search' CLI subcommands so the model can
// search GitHub's code / repo / issue / PR indexes. Returns ranked
// results with snippets (for code), descriptions (for repos), or
// titles (for issues/prs).
//
// Auth: gh's local credential store (the user is already logged in;
// the tool never sees a token).
//
// Index limitation: GitHub's code search index covers only public
// repositories with sufficient activity. Not every public repo is
// reachable via `gh search code`; if the model misses an obvious hit
// it should fall back to web_search or web_fetch on the raw URL.
type GHSearchTool struct {
	// Runner lets tests inject a fake. Production: realGHRunner{}.
	Runner GHRunner
	// Timeout caps each gh invocation. Default ghSearchDefaultTimeout.
	Timeout time.Duration
}

const (
	ghSearchDefaultTimeout = 15 * time.Second
	ghSearchDefaultLimit   = 10
	ghSearchMaxLimit       = 30
)

// validGHKinds enumerates the four `gh search` subcommands this tool
// exposes. Anything else is an input-validation error.
var validGHKinds = map[string]bool{
	"code":   true,
	"repos":  true,
	"issues": true,
	"prs":    true,
}

// fieldsForKind maps each search kind to the --json field selector
// passed to gh. Keep these in sync with the JSON unmarshalling structs
// in parseGH* below.
var fieldsForKind = map[string]string{
	"code":   "path,repository,url,textMatches",
	"repos":  "fullName,description,url,stargazersCount,updatedAt",
	"issues": "title,url,state,number,repository,updatedAt",
	"prs":    "title,url,state,number,repository,updatedAt",
}

// NewGHSearchTool returns a GHSearchTool wired to the real gh binary.
func NewGHSearchTool() *GHSearchTool {
	return &GHSearchTool{Runner: realGHRunner{}}
}

func (*GHSearchTool) Name() string { return "gh_search" }

func (*GHSearchTool) Description() string {
	return "Search GitHub via the local gh CLI. Use for: finding example implementations across public repos (kind=code), discovering libraries by topic or keyword (kind=repos), locating issues or pull requests by text (kind=issues / kind=prs). Filters: language (e.g. \"go\") and owner (a user or org) narrow the index. Auth comes from the locally-authenticated gh - no token plumbing. Limitation: GitHub's code search index only covers public repositories with sufficient activity, so a small or new repo may be invisible to kind=code; in that case web_search or a direct web_fetch on github.com is a better fallback."
}

func (*GHSearchTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "The search query. Required. Plain keywords work; gh handles quoting in argv so spaces are safe."
			},
			"kind": {
				"type": "string",
				"enum": ["code", "repos", "issues", "prs"],
				"description": "Which GitHub index to hit. Default \"code\"."
			},
			"language": {
				"type": "string",
				"description": "Filter by programming language, e.g. \"go\". Most useful with kind=code or kind=repos."
			},
			"owner": {
				"type": "string",
				"description": "Restrict to a single user or org, e.g. \"georgebuilds\"."
			},
			"limit": {
				"type": "integer",
				"description": "Max results, 1-30. Default 10. Values >30 are clamped, <=0 falls back to the default."
			}
		},
		"required": ["query"]
	}`)
}

// ghSearchInput is the JSON the model passes to gh_search.
type ghSearchInput struct {
	Query    string `json:"query"`
	Kind     string `json:"kind"`
	Language string `json:"language"`
	Owner    string `json:"owner"`
	Limit    int    `json:"limit"`
}

// ghSearchResult is one normalized entry in the output envelope -
// uniform across all four kinds so the model doesn't have to branch
// on shape.
type ghSearchResult struct {
	Rank    int                    `json:"rank"`
	Title   string                 `json:"title"`
	URL     string                 `json:"url"`
	Snippet string                 `json:"snippet"`
	Repo    string                 `json:"repo,omitempty"`
	Extra   map[string]interface{} `json:"extra,omitempty"`
}

// ghSearchOutput is the envelope returned from Execute. `Backend` is
// always "github" - it mirrors the field web_search uses so a model
// that already knows that shape doesn't have to learn a second one.
type ghSearchOutput struct {
	Kind    string           `json:"kind"`
	Query   string           `json:"query"`
	Count   int              `json:"count"`
	Results []ghSearchResult `json:"results"`
	Backend string           `json:"backend"`
}

// Execute validates the input, builds the gh args, runs the binary
// via the Runner seam, parses kind-specific JSON, and normalizes
// into a uniform envelope.
func (t *GHSearchTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	if t.Runner == nil {
		return nil, errors.New("gh_search: no runner configured")
	}
	var in ghSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("gh_search: parse input: %w", err)
	}
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return nil, errors.New("gh_search: empty query")
	}
	kind := in.Kind
	if kind == "" {
		kind = "code"
	}
	if !validGHKinds[kind] {
		return nil, fmt.Errorf("gh_search: invalid kind %q (want one of code, repos, issues, prs)", kind)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = ghSearchDefaultLimit
	}
	if limit > ghSearchMaxLimit {
		limit = ghSearchMaxLimit
	}

	args := []string{
		"search", kind, ghEscapeQuery(q),
		"--json", fieldsForKind[kind],
		"--limit", strconv.Itoa(limit),
	}
	if lang := strings.TrimSpace(in.Language); lang != "" {
		args = append(args, "--language", lang)
	}
	if owner := strings.TrimSpace(in.Owner); owner != "" {
		args = append(args, "--owner", owner)
	}

	timeout := t.Timeout
	if timeout == 0 {
		timeout = ghSearchDefaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	raw, err := t.Runner.Run(cctx, args)
	if err != nil {
		return nil, fmt.Errorf("gh_search/%s: %w", kind, err)
	}

	results, err := parseGHResults(kind, raw)
	if err != nil {
		return nil, fmt.Errorf("gh_search/%s: %w", kind, err)
	}

	out := ghSearchOutput{
		Kind:    kind,
		Query:   q,
		Count:   len(results),
		Results: results,
		Backend: "github",
	}
	return json.Marshal(out)
}

// parseGHResults dispatches to the kind-specific JSON shape and
// returns a uniform []ghSearchResult.
func parseGHResults(kind string, raw []byte) ([]ghSearchResult, error) {
	// gh prints "[]" (or "null") for an empty result set; handle
	// both uniformly so the caller sees a clean count=0.
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []ghSearchResult{}, nil
	}
	switch kind {
	case "code":
		return parseGHCode(raw)
	case "repos":
		return parseGHRepos(raw)
	case "issues", "prs":
		return parseGHIssuesOrPRs(raw)
	default:
		// Already validated upstream; defensive.
		return nil, fmt.Errorf("unsupported kind %q", kind)
	}
}

// --- code ------------------------------------------------------------------

type ghCodeRow struct {
	Path       string `json:"path"`
	URL        string `json:"url"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	TextMatches []struct {
		Fragment string `json:"fragment"`
	} `json:"textMatches"`
}

func parseGHCode(raw []byte) ([]ghSearchResult, error) {
	var rows []ghCodeRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode code results: %w", err)
	}
	out := make([]ghSearchResult, 0, len(rows))
	for i, r := range rows {
		snippet := ""
		if len(r.TextMatches) > 0 {
			snippet = strings.TrimSpace(r.TextMatches[0].Fragment)
		}
		out = append(out, ghSearchResult{
			Rank:    i + 1,
			Title:   r.Path,
			URL:     r.URL,
			Snippet: snippet,
			Repo:    r.Repository.NameWithOwner,
		})
	}
	return out, nil
}

// --- repos -----------------------------------------------------------------

type ghRepoRow struct {
	FullName        string `json:"fullName"`
	Description     string `json:"description"`
	URL             string `json:"url"`
	StargazersCount int    `json:"stargazersCount"`
	UpdatedAt       string `json:"updatedAt"`
}

func parseGHRepos(raw []byte) ([]ghSearchResult, error) {
	var rows []ghRepoRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode repos results: %w", err)
	}
	out := make([]ghSearchResult, 0, len(rows))
	for i, r := range rows {
		out = append(out, ghSearchResult{
			Rank:    i + 1,
			Title:   r.FullName,
			URL:     r.URL,
			Snippet: r.Description,
			Extra: map[string]interface{}{
				"stars": r.StargazersCount,
			},
		})
	}
	return out, nil
}

// --- issues / prs ----------------------------------------------------------

type ghIssueRow struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	State      string `json:"state"`
	Number     int    `json:"number"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	UpdatedAt string `json:"updatedAt"`
}

func parseGHIssuesOrPRs(raw []byte) ([]ghSearchResult, error) {
	var rows []ghIssueRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode issues/prs results: %w", err)
	}
	out := make([]ghSearchResult, 0, len(rows))
	for i, r := range rows {
		snippet := strings.TrimSpace(r.State + " #" + strconv.Itoa(r.Number))
		out = append(out, ghSearchResult{
			Rank:    i + 1,
			Title:   r.Title,
			URL:     r.URL,
			Snippet: snippet,
			Repo:    r.Repository.NameWithOwner,
		})
	}
	return out, nil
}

// ghEscapeQuery is a placeholder for any future query-rewriting we
// want to do before handing the string to gh. gh receives the query
// as an argv element, so shell-escaping isn't necessary; this helper
// exists so the call site reads intentionally and so a future hook
// (e.g. stripping disallowed qualifiers) has a single landing spot.
func ghEscapeQuery(q string) string {
	return q
}
