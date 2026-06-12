package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/georgebuilds/carlos/internal/notes"
)

// NotesBacklinksTool registers as `notes_backlinks`. Returns every
// note that wikilinks to the resolved target, with the sentence
// containing the link as context.
type NotesBacklinksTool struct {
	env *notesEnv
}

func NewNotesBacklinksTool(env *notesEnv) *NotesBacklinksTool {
	return &NotesBacklinksTool{env: env}
}

func (*NotesBacklinksTool) Name() string { return "notes_backlinks" }

func (*NotesBacklinksTool) Description() string {
	return "List every note in your configured Obsidian vault that wikilinks to the given target, with the line of context around each link. Replaces vault-wide grep for `[[target]]` style searches. Always operates on your default vault, use obsidian_backlinks to query a different vault. Pass `frame:` to restrict the target note to that frame's vault_subtree; when omitted, defaults to the active frame's subtree."
}

func (*NotesBacklinksTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"note":  {"type": "string", "description": "Target note name or relpath."},
			"limit": {"type": "integer", "description": "Default 50."},
			"frame": {"type": "string", "description": "Optional frame name. Restricts the target note to that frame's vault_subtree. When omitted, defaults to the active frame's subtree."}
		},
		"required": ["note"]
	}`)
}

type notesBacklinksInput struct {
	Note  string `json:"note"`
	Limit int    `json:"limit"`
	Frame string `json:"frame"`
}

type notesBacklinksResponse struct {
	Target    string          `json:"target"`
	Vault     string          `json:"vault"`
	Title     string          `json:"title"`
	Backlinks []backlinkEntry `json:"backlinks"`
	Total     int             `json:"total"`
}

type backlinkEntry struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Context string `json:"context"`
	Line    int    `json:"line"`
}

func (t *NotesBacklinksTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in notesBacklinksInput
	if err := json.Unmarshal(input, &in); err != nil {
		return jsonErr("parse input: %v", err)
	}
	if in.Note == "" {
		return jsonErr("missing required field: %q", "note")
	}
	abs, v, envelope, err := t.env.resolveOrError("")
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("notes_backlinks: %v", err)
	}
	_, subtree, ferr := t.env.resolveFrameArg(in.Frame)
	if ferr != nil {
		return jsonErr("notes_backlinks: %v", ferr)
	}
	resolved, _, _ := v.Resolve(in.Note)
	if resolved != "" && !inSubtree(resolved, subtree) {
		return notFoundResponse(in.Note)
	}

	bl, err := v.Backlinks(in.Note, in.Limit)
	if err != nil {
		if errors.Is(err, notes.ErrNotFound) {
			return notFoundResponse(in.Note)
		}
		return jsonErr("notes_backlinks: %v", err)
	}
	// Target for the response header - we already know it resolved
	// (Backlinks would have errored otherwise) and we computed
	// `resolved` above for the subtree gate.
	target, _ := v.Get(resolved)

	resp := notesBacklinksResponse{
		Target:    resolved,
		Vault:     abs,
		Title:     "",
		Backlinks: make([]backlinkEntry, 0, len(bl)),
		Total:     len(bl),
	}
	if target != nil {
		resp.Title = target.Title
	}
	for _, b := range bl {
		resp.Backlinks = append(resp.Backlinks, backlinkEntry{
			Path:    b.Path,
			Title:   b.Title,
			Context: b.Context,
			Line:    b.Line,
		})
	}
	return jsonOK(resp)
}

var _ Tool = (*NotesBacklinksTool)(nil)
