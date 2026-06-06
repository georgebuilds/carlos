package tools

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/georgebuilds/carlos/internal/notes"
)

// NotesSearchTool registers as `notes_search`. Replaces `grep`-on-
// vault: returns paragraph-sized snippets ranked by a cheap heuristic.
//
// Phase F-11 makes the tool frame aware. With frames wired, an omitted
// `frame:` fans out across every configured frame's vault_subtree and
// labels each hit with its source frame; an explicit `frame:` arg
// restricts to that one frame's subtree. Legacy single shelf mode
// (no frames configured) behaves identically to the pre-F-11 surface:
// no prefix, no restriction.
type NotesSearchTool struct {
	env *notesEnv
}

func NewNotesSearchTool(env *notesEnv) *NotesSearchTool { return &NotesSearchTool{env: env} }

func (*NotesSearchTool) Name() string { return "notes_search" }

func (*NotesSearchTool) Description() string {
	return "Search your configured Obsidian vault by free-text query, returning paragraph snippets with the matching note's title and line number. Supports tag and frontmatter filters. Always operates on your default vault, use obsidian_search to query a different vault. Prefer this over `grep` for vault-scoped lookups: results are paragraph-bounded and ranked. When frames are configured an omitted `frame:` fans out across every frame's vault_subtree and labels each hit with its source frame name; pass `frame:` to restrict to that one frame's subtree."
}

func (*NotesSearchTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Free-text query. Case-insensitive substring match across body + frontmatter values."},
			"tag":   {"type": "string", "description": "Optional: only match notes carrying this tag (with or without #)."},
			"where": {"type": "object", "description": "Optional frontmatter filter, e.g. {\"status\": \"alpha\"}."},
			"limit": {"type": "integer", "description": "Max matches. Default 10, hard cap 50."},
			"frame": {"type": "string", "description": "Optional frame name. Restricts results to that frame's vault_subtree. When omitted, defaults to the active frame's subtree; with multiple frames configured, cross-frame results are labelled with their source frame in the frame field on each match."}
		},
		"required": ["query"]
	}`)
}

type notesSearchInput struct {
	Query string         `json:"query"`
	Tag   string         `json:"tag"`
	Where map[string]any `json:"where"`
	Limit int            `json:"limit"`
	Frame string         `json:"frame"`
}

type notesSearchResponse struct {
	Query     string             `json:"query"`
	Vault     string             `json:"vault"`
	Matches   []notesSearchMatch `json:"matches"`
	Total     int                `json:"total"`
	Truncated bool               `json:"truncated"`
}

type notesSearchMatch struct {
	// Frame is the source frame name when the env has frames wired and
	// the hit is being surfaced across multiple frames. Empty in
	// legacy single shelf mode (no frames configured) so older callers
	// see byte for byte the same JSON they did pre-F-11.
	Frame   string  `json:"frame,omitempty"`
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
	abs, v, envelope, err := t.env.resolveOrError("")
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("notes_search: %v", err)
	}

	targets, err := t.env.frameFanout(in.Frame)
	if err != nil {
		return jsonErr("notes_search: %v", err)
	}

	opts := notes.SearchOptions{
		Tag:   in.Tag,
		Where: in.Where,
		Limit: in.Limit,
	}

	// Fan out across the target frames. For each, we run the same
	// Search + filter results to its subtree. Results are merged and
	// re-sorted by score so the truncation cap is still meaningful.
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	// Wide-net options: ask the index for the hard cap so we have
	// enough room to filter per-frame without dropping the hit the
	// caller cares about.
	wideOpts := opts
	wideOpts.Limit = 50

	hits, err := v.Search(in.Query, wideOpts)
	if err != nil {
		return jsonErr("notes_search: %v", err)
	}
	total, err := v.SearchTotal(in.Query, opts)
	if err != nil {
		return jsonErr("notes_search: %v", err)
	}

	// Build a per-target match slice, with prefix labels when frames
	// are wired. A single hit may appear once per frame whose subtree
	// covers it, which in practice never happens because frame
	// subtrees are disjoint by design; even if a user nests one frame
	// inside another the dedup-by-path step below collapses it to the
	// first match.
	seen := map[string]bool{}
	combined := make([]notesSearchMatch, 0, len(hits))
	totalFiltered := 0
	for _, tg := range targets {
		for _, h := range hits {
			if !inSubtree(h.Path, tg.Subtree) {
				continue
			}
			if seen[h.Path] {
				continue
			}
			seen[h.Path] = true
			totalFiltered++
			combined = append(combined, notesSearchMatch{
				Frame:   tg.Name,
				Path:    h.Path,
				Title:   h.Title,
				Snippet: h.Snippet,
				Line:    h.Line,
				Score:   h.Score,
			})
		}
	}

	// Stable sort by score desc so the merged view across frames is
	// still ranked. Equal-score ties keep their fan-out order, which
	// is deterministic (config list order).
	sort.SliceStable(combined, func(i, j int) bool {
		return combined[i].Score > combined[j].Score
	})

	// When a single frame was targeted, the index's own total is a
	// tight upper bound on the filtered set; we take min(total,
	// totalFiltered) so the truncated flag stays honest even if the
	// wide-net cap dropped a tail entry.
	if in.Frame != "" || !t.env.hasFrames() {
		if totalFiltered < total {
			total = totalFiltered
		}
	} else {
		total = totalFiltered
	}

	if len(combined) > limit {
		combined = combined[:limit]
	}

	resp := notesSearchResponse{
		Query:     in.Query,
		Vault:     abs,
		Matches:   combined,
		Total:     total,
		Truncated: total > len(combined),
	}
	return jsonOK(resp)
}

var _ Tool = (*NotesSearchTool)(nil)
