package tools

import (
	"context"
	"encoding/json"

	"github.com/georgebuilds/carlos/internal/notes"
)

// NotesSearchTool registers as `notes_search`. Replaces `grep`-on-
// vault: returns paragraph-sized snippets ranked by a cheap heuristic.
type NotesSearchTool struct {
	env *notesEnv
}

func NewNotesSearchTool(env *notesEnv) *NotesSearchTool { return &NotesSearchTool{env: env} }

func (*NotesSearchTool) Name() string { return "notes_search" }

func (*NotesSearchTool) Description() string {
	return "Search the Obsidian vault by free-text query, returning paragraph snippets with the matching note's title and line number. Supports tag and frontmatter filters. Pass an optional `vault:` field to query a different markdown vault than carlos's default. Prefer this over `grep` for vault-scoped lookups: results are paragraph-bounded and ranked."
}

func (*NotesSearchTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Free-text query. Case-insensitive substring match across body + frontmatter values."},
			"tag":   {"type": "string", "description": "Optional: only match notes carrying this tag (with or without #)."},
			"where": {"type": "object", "description": "Optional frontmatter filter, e.g. {\"status\": \"alpha\"}."},
			"limit": {"type": "integer", "description": "Max matches. Default 10, hard cap 50."},
			"vault": {"type": "string", "description": "Optional absolute or ~-relative path to a different Obsidian-flavored markdown vault. Defaults to carlos's configured vault."}
		},
		"required": ["query"]
	}`)
}

type notesSearchInput struct {
	Query string         `json:"query"`
	Tag   string         `json:"tag"`
	Where map[string]any `json:"where"`
	Limit int            `json:"limit"`
	Vault string         `json:"vault"`
}

type notesSearchResponse struct {
	Query     string             `json:"query"`
	Vault     string             `json:"vault"`
	Matches   []notesSearchMatch `json:"matches"`
	Total     int                `json:"total"`
	Truncated bool               `json:"truncated"`
}

type notesSearchMatch struct {
	Path    string  `json:"path"`
	Title   string  `json:"title"`
	Snippet string  `json:"snippet"`
	Line    int     `json:"line"`
	Score   float64 `json:"score"`
}

func (t *NotesSearchTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in notesSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return jsonErr("parse input: %v", err)
	}
	if in.Query == "" {
		return jsonErr("missing required field: %q", "query")
	}
	abs, v, envelope, err := t.env.resolveOrError(in.Vault)
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("notes_search: %v", err)
	}

	opts := notes.SearchOptions{
		Tag:   in.Tag,
		Where: in.Where,
		Limit: in.Limit,
	}
	hits, err := v.Search(in.Query, opts)
	if err != nil {
		return jsonErr("notes_search: %v", err)
	}
	// Compute total for the `truncated` flag. Cheap: same scoring
	// pass, just no snippet build. The duplication is fine for v0;
	// we can fold them later if it becomes a hotspot.
	total, err := v.SearchTotal(in.Query, opts)
	if err != nil {
		return jsonErr("notes_search: %v", err)
	}

	resp := notesSearchResponse{
		Query:     in.Query,
		Vault:     abs,
		Matches:   make([]notesSearchMatch, 0, len(hits)),
		Total:     total,
		Truncated: total > len(hits),
	}
	for _, h := range hits {
		resp.Matches = append(resp.Matches, notesSearchMatch{
			Path:    h.Path,
			Title:   h.Title,
			Snippet: h.Snippet,
			Line:    h.Line,
			Score:   h.Score,
		})
	}
	return jsonOK(resp)
}

var _ Tool = (*NotesSearchTool)(nil)
