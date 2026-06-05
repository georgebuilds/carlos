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
	return "List every note in the vault that wikilinks to the given target, with the line of context around each link. Replaces vault-wide grep for `[[target]]` style searches. Pass an optional `vault:` field to query a different markdown vault than carlos's default."
}

func (*NotesBacklinksTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"note":  {"type": "string", "description": "Target note name or relpath."},
			"limit": {"type": "integer", "description": "Default 50."},
			"vault": {"type": "string", "description": "Optional absolute or ~-relative path to a different Obsidian-flavored markdown vault. Defaults to carlos's configured vault."}
		},
		"required": ["note"]
	}`)
}

type notesBacklinksInput struct {
	Note  string `json:"note"`
	Limit int    `json:"limit"`
	Vault string `json:"vault"`
}

type notesBacklinksResponse struct {
	Target    string           `json:"target"`
	Vault     string           `json:"vault"`
	Title     string           `json:"title"`
	Backlinks []backlinkEntry  `json:"backlinks"`
	Total     int              `json:"total"`
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
	abs, v, envelope, err := t.env.resolveOrError(in.Vault)
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("notes_backlinks: %v", err)
	}

	bl, err := v.Backlinks(in.Note, in.Limit)
	if err != nil {
		if errors.Is(err, notes.ErrNotFound) {
			return notFoundResponse(in.Note)
		}
		return jsonErr("notes_backlinks: %v", err)
	}
	// Resolve target for the response header — we already know it
	// resolved (Backlinks errored otherwise).
	resolved, _, _ := v.Resolve(in.Note)
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
