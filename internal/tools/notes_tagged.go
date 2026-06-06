package tools

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/georgebuilds/carlos/internal/notes"
)

// NotesTaggedTool registers as `notes_tagged`. Returns every note
// carrying the given tag, sorted newest first.
//
// Phase F-11 frame aware: omitted `frame:` fans out across every
// configured frame and labels each hit; explicit `frame:` restricts to
// that frame's vault_subtree. Legacy single shelf mode is unchanged.
type NotesTaggedTool struct {
	env *notesEnv
}

func NewNotesTaggedTool(env *notesEnv) *NotesTaggedTool {
	return &NotesTaggedTool{env: env}
}

func (*NotesTaggedTool) Name() string { return "notes_tagged" }

func (*NotesTaggedTool) Description() string {
	return "List every note in your configured Obsidian vault carrying the given tag, with title + a one-line description. Sorted newest first. Use obsidian_tagged to query a different vault. When frames are configured, an omitted `frame:` fans out across every frame's vault_subtree and labels each hit with its source frame; pass `frame:` to restrict to that one frame's subtree."
}

func (*NotesTaggedTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"tag":   {"type": "string", "description": "Tag name with or without leading #."},
			"limit": {"type": "integer", "description": "Default 50."},
			"frame": {"type": "string", "description": "Optional frame name. Restricts results to that frame's vault_subtree. When omitted, defaults to the active frame's subtree; with multiple frames configured, cross-frame results are labelled with their source frame in the frame field on each entry."}
		},
		"required": ["tag"]
	}`)
}

type notesTaggedInput struct {
	Tag   string `json:"tag"`
	Limit int    `json:"limit"`
	Frame string `json:"frame"`
}

type notesTaggedResponse struct {
	Tag   string        `json:"tag"`
	Vault string        `json:"vault"`
	Notes []taggedEntry `json:"notes"`
	Total int           `json:"total"`
}

type taggedEntry struct {
	// Frame is the source frame name when the env has frames wired.
	// Empty in legacy single shelf mode.
	Frame       string `json:"frame,omitempty"`
	Path        string `json:"path"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Modified    string `json:"modified"`
}

func (t *NotesTaggedTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in notesTaggedInput
	if err := json.Unmarshal(input, &in); err != nil {
		return jsonErr("parse input: %v", err)
	}
	if in.Tag == "" {
		return jsonErr("missing required field: %q", "tag")
	}
	abs, v, envelope, err := t.env.resolveOrError("")
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("notes_tagged: %v", err)
	}

	targets, ferr := t.env.frameFanout(in.Frame)
	if ferr != nil {
		return jsonErr("notes_tagged: %v", ferr)
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}

	// Wide query so we have enough room to filter per-frame without
	// dropping the entries the caller cares about. We re-cap below.
	hits, err := v.Tagged(in.Tag, 0)
	if err != nil {
		return jsonErr("notes_tagged: %v", err)
	}

	seen := map[string]bool{}
	combined := make([]taggedEntry, 0, len(hits))
	for _, tg := range targets {
		for _, n := range hits {
			if !inSubtree(n.Path, tg.Subtree) {
				continue
			}
			if seen[n.Path] {
				continue
			}
			seen[n.Path] = true
			combined = append(combined, taggedEntry{
				Frame:       tg.Name,
				Path:        n.Path,
				Title:       n.Title,
				Description: notes.Description(n),
				Modified:    n.ModTime.UTC().Format("2006-01-02T15:04:05Z"),
			})
		}
	}

	// Re-sort merged set by modtime desc so the "newest first"
	// contract holds across the fan out. Same shape Tagged returns.
	sort.SliceStable(combined, func(i, j int) bool {
		return combined[i].Modified > combined[j].Modified
	})

	total := len(combined)
	if len(combined) > limit {
		combined = combined[:limit]
	}

	resp := notesTaggedResponse{
		Tag:   in.Tag,
		Vault: abs,
		Notes: combined,
		Total: total,
	}
	return jsonOK(resp)
}

var _ Tool = (*NotesTaggedTool)(nil)
