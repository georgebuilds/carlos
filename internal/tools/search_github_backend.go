// GHSearchBackend wraps GHSearchTool as a SearchBackend so MultiBackend
// (and through it, the research engine + routing layer) can include
// GitHub code search as one of the fan-out sources. Backend.Name() is
// "github" so the route phase can address it by name.
//
// The wrapper specializes the underlying tool to kind=code: research
// routing is about source discovery, not issue triage. If callers want
// repos/issues/PRs they go through the standalone gh_search Tool.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// GHSearchBackend is a SearchBackend adapter around GHSearchTool that
// always issues kind=code queries. Construct via NewGHSearchBackend for
// the production-wired version; tests typically build the struct
// literally so they can inject a fake GHRunner via Tool.Runner.
type GHSearchBackend struct {
	// Tool is the underlying gh_search wrapper. Required; nil → Search
	// returns a clear error rather than panicking.
	Tool *GHSearchTool

	// Language is the GitHub code-search language filter ("" = any).
	// Set per-instance to pin every research-routed call from this
	// backend to one language. Default empty.
	Language string
}

// NewGHSearchBackend returns a backend wired to the real gh binary.
func NewGHSearchBackend() *GHSearchBackend {
	return &GHSearchBackend{Tool: NewGHSearchTool()}
}

// Name implements SearchBackend.
func (*GHSearchBackend) Name() string { return "github" }

// ghBackendInput mirrors ghSearchInput but lives here so we can build
// the input JSON without depending on a freshly-typed struct in the
// caller. The fields match the JSON tags on ghSearchInput.
type ghBackendInput struct {
	Query    string `json:"query"`
	Kind     string `json:"kind"`
	Language string `json:"language,omitempty"`
	Limit    int    `json:"limit"`
}

// ghBackendResult / ghBackendOutput decode the envelope produced by
// GHSearchTool.Execute. Field shapes track ghSearchResult /
// ghSearchOutput in search_github.go.
type ghBackendResult struct {
	Rank    int    `json:"rank"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Repo    string `json:"repo"`
}

type ghBackendOutput struct {
	Results []ghBackendResult `json:"results"`
}

// Search implements SearchBackend by delegating to GHSearchTool with
// kind=code, then mapping the envelope into []SearchResult with
// Source="github". The Title is "<repo>: <path>" when repo is non-
// empty, otherwise just the path — matching how the model already
// sees code-search results in the gh_search tool envelope.
func (b *GHSearchBackend) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	if b.Tool == nil {
		return []SearchResult{}, errors.New("github: no tool configured")
	}

	limit := max
	if limit <= 0 {
		limit = ghSearchDefaultLimit
	}
	if limit > ghSearchMaxLimit {
		limit = ghSearchMaxLimit
	}

	in := ghBackendInput{
		Query:    query,
		Kind:     "code",
		Language: b.Language,
		Limit:    limit,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("github: marshal input: %w", err)
	}

	out, err := b.Tool.Execute(ctx, raw)
	if err != nil {
		// Underlying tool already prefixes errors with "gh_search/...";
		// add our backend prefix so the multi-fanout error map keys are
		// consistent with Name().
		return nil, fmt.Errorf("github: %w", err)
	}

	var env ghBackendOutput
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("github: decode envelope: %w", err)
	}

	results := make([]SearchResult, 0, len(env.Results))
	for i, r := range env.Results {
		title := r.Title
		if r.Repo != "" {
			title = r.Repo + ": " + r.Title
		}
		results = append(results, SearchResult{
			Rank:    i + 1,
			Title:   title,
			URL:     r.URL,
			Snippet: r.Snippet,
			Source:  "github",
		})
	}
	return results, nil
}
