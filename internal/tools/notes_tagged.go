package tools

import (
	"context"
	"encoding/json"

	"github.com/georgebuilds/carlos/internal/notes"
)

// NotesTaggedTool registers as `notes_tagged`. Returns every note
// carrying the given tag, sorted newest first.
type NotesTaggedTool struct {
	env *notesEnv
}

func NewNotesTaggedTool(env *notesEnv) *NotesTaggedTool {
	return &NotesTaggedTool{env: env}
}

func (*NotesTaggedTool) Name() string { return "notes_tagged" }

func (*NotesTaggedTool) Description() string {
	return "List every note in the vault carrying the given tag, with title + a one-line description (from frontmatter `description:` or the first body paragraph). Sorted newest first. Pass an optional `vault:` field to query a different markdown vault than carlos's default."
}

func (*NotesTaggedTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"tag":   {"type": "string", "description": "Tag name with or without leading #."},
			"limit": {"type": "integer", "description": "Default 50."},
			"vault": {"type": "string", "description": "Optional absolute or ~-relative path to a different Obsidian-flavored markdown vault. Defaults to carlos's configured vault."}
		},
		"required": ["tag"]
	}`)
}

type notesTaggedInput struct {
	Tag   string `json:"tag"`
	Limit int    `json:"limit"`
	Vault string `json:"vault"`
}

type notesTaggedResponse struct {
	Tag   string             `json:"tag"`
	Vault string             `json:"vault"`
	Notes []taggedEntry      `json:"notes"`
	Total int                `json:"total"`
}

type taggedEntry struct {
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
	abs, v, envelope, err := t.env.resolveOrError(in.Vault)
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("notes_tagged: %v", err)
	}

	hits, err := v.Tagged(in.Tag, in.Limit)
	if err != nil {
		return jsonErr("notes_tagged: %v", err)
	}
	resp := notesTaggedResponse{
		Tag:   in.Tag,
		Vault: abs,
		Notes: make([]taggedEntry, 0, len(hits)),
		Total: len(hits),
	}
	for _, n := range hits {
		resp.Notes = append(resp.Notes, taggedEntry{
			Path:        n.Path,
			Title:       n.Title,
			Description: notes.Description(n),
			Modified:    n.ModTime.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return jsonOK(resp)
}

var _ Tool = (*NotesTaggedTool)(nil)
