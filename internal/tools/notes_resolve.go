package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/georgebuilds/carlos/internal/notes"
)

// NotesResolveTool registers as `notes_resolve` — tells the model what
// a `[[link]]` would resolve to before it calls `notes_get`. Useful
// when the same title appears in multiple folders.
type NotesResolveTool struct {
	env *notesEnv
}

func NewNotesResolveTool(env *notesEnv) *NotesResolveTool {
	return &NotesResolveTool{env: env}
}

func (*NotesResolveTool) Name() string { return "notes_resolve" }

func (*NotesResolveTool) Description() string {
	return "Resolve a wikilink (with or without `[[brackets]]`) to its target relpath in your configured Obsidian vault. Returns the candidate list when the target title appears in multiple folders. Use obsidian_resolve to query a different vault. Pass `frame:` to restrict resolution to that frame's vault_subtree; when omitted, defaults to the active frame's subtree."
}

func (*NotesResolveTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"link":  {"type": "string", "description": "Wikilink text, with or without [[brackets]]."},
			"frame": {"type": "string", "description": "Optional frame name. Restricts candidate resolution to that frame's vault_subtree. When omitted, defaults to the active frame's subtree."}
		},
		"required": ["link"]
	}`)
}

type notesResolveInput struct {
	Link  string `json:"link"`
	Frame string `json:"frame"`
}

type notesResolveResponse struct {
	Link       string             `json:"link"`
	Vault      string             `json:"vault"`
	Resolved   string             `json:"resolved"`
	Title      string             `json:"title,omitempty"`
	Ambiguous  bool               `json:"ambiguous"`
	Candidates []resolveCandidate `json:"candidates,omitempty"`
}

type resolveCandidate struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}

func (t *NotesResolveTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in notesResolveInput
	if err := json.Unmarshal(input, &in); err != nil {
		return jsonErr("parse input: %v", err)
	}
	if in.Link == "" {
		return jsonErr("missing required field: %q", "link")
	}
	abs, v, envelope, err := t.env.resolveOrError("")
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("notes_resolve: %v", err)
	}
	_, subtree, ferr := t.env.resolveFrameArg(in.Frame)
	if ferr != nil {
		return jsonErr("notes_resolve: %v", ferr)
	}

	resolved, cands, err := v.Resolve(in.Link)
	if err != nil {
		if errors.Is(err, notes.ErrNotFound) {
			return notFoundResponse(in.Link)
		}
		return jsonErr("notes_resolve: %v", err)
	}

	// Filter candidates to the subtree, then pick the new resolved
	// note as the shortest path among the remaining set (mirroring
	// VaultIndex.resolveLink's tiebreak). If nothing survives the
	// filter we report not-found so the model sees a deterministic
	// envelope instead of a cross-frame leak.
	if subtree != "" {
		filtered := cands[:0]
		for _, c := range cands {
			if inSubtree(c.Path, subtree) {
				filtered = append(filtered, c)
			}
		}
		cands = filtered
		if len(cands) == 0 {
			return notFoundResponse(in.Link)
		}
		resolved = cands[0].Path
	}

	target, _ := v.Get(resolved)

	resp := notesResolveResponse{
		Link:      in.Link,
		Vault:     abs,
		Resolved:  resolved,
		Ambiguous: len(cands) > 1,
	}
	if target != nil {
		resp.Title = target.Title
	}
	if len(cands) > 1 {
		resp.Candidates = make([]resolveCandidate, 0, len(cands))
		for _, c := range cands {
			resp.Candidates = append(resp.Candidates, resolveCandidate{Path: c.Path, Title: c.Title})
		}
	}
	return jsonOK(resp)
}

var _ Tool = (*NotesResolveTool)(nil)
