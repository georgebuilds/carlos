package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/georgebuilds/carlos/internal/notes"
)

// NotesGetTool registers as `notes_get` - the "what's in this note"
// query. Returns frontmatter + outline by default; body is opt-in.
type NotesGetTool struct {
	env *notesEnv
}

// NewNotesGetTool ties the tool to the shared cache. Constructed by
// NewDefaultRegistryWithBaseDir; tests can wire their own.
func NewNotesGetTool(env *notesEnv) *NotesGetTool { return &NotesGetTool{env: env} }

func (*NotesGetTool) Name() string { return "notes_get" }

func (*NotesGetTool) Description() string {
	return "Fetch a note's structure from your configured Obsidian vault: frontmatter, outline, link counts, modtime. Optionally pull the full body or a single section. Resolves `note` via Obsidian's shortest-unique-path. Always operates on your default vault, use obsidian_get to query a different vault. Pass `frame:` to restrict resolution to that frame's vault_subtree; when omitted, defaults to the active frame's subtree."
}

func (*NotesGetTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"note":    {"type": "string", "description": "Note name or relpath; resolved via Obsidian's shortest-unique-path."},
			"section": {"type": "string", "description": "Optional heading text. When set, body is the section's content only (skipping nested sub-sections)."},
			"body":    {"type": "boolean", "description": "Include the full body. Default false: returns just frontmatter + outline."},
			"frame":   {"type": "string", "description": "Optional frame name. Restricts note resolution to that frame's vault_subtree. When omitted, defaults to the active frame's subtree."}
		},
		"required": ["note"]
	}`)
}

type notesGetInput struct {
	Note    string `json:"note"`
	Section string `json:"section"`
	Body    bool   `json:"body"`
	Frame   string `json:"frame"`
}

// notesGetResponse is the success envelope. We omit Body when empty so
// the default-no-body response stays small.
type notesGetResponse struct {
	Path        string           `json:"path"`
	Vault       string           `json:"vault"`
	Title       string           `json:"title"`
	Frontmatter map[string]any   `json:"frontmatter"`
	Outline     []outlineEntry   `json:"outline"`
	Tags        []string         `json:"tags"`
	LinksOut    int              `json:"links_out"`
	LinksIn     int              `json:"links_in"`
	Size        int64            `json:"size"`
	Modified    string           `json:"modified"`
	Section     string           `json:"section,omitempty"`
	Body        string           `json:"body,omitempty"`
}

type outlineEntry struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
	Line  int    `json:"line"`
}

// Execute resolves the note, applies the optional `section:` and
// `body:` flags, and emits the typed response. Errors flow through
// the jsonErr envelope.
func (t *NotesGetTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in notesGetInput
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
		return jsonErr("notes_get: %v", err)
	}
	_, subtree, ferr := t.env.resolveFrameArg(in.Frame)
	if ferr != nil {
		return jsonErr("notes_get: %v", ferr)
	}

	n, err := v.Get(in.Note)
	if err != nil {
		if errors.Is(err, notes.ErrNotFound) {
			return notFoundResponse(in.Note)
		}
		return jsonErr("notes_get: %v", err)
	}
	// Frame restriction: a hit outside the requested subtree is
	// reported as not-found so the model gets a clean envelope rather
	// than a surprise cross-frame leak.
	if !inSubtree(n.Path, subtree) {
		return notFoundResponse(in.Note)
	}

	resp := notesGetResponse{
		Path:        n.Path,
		Vault:       abs,
		Title:       n.Title,
		Frontmatter: n.Frontmatter,
		Outline:     buildOutline(n.Headings),
		Tags:        n.Tags,
		LinksOut:    len(n.Links),
		LinksIn:     len(n.Backlinks),
		Size:        n.Size,
		Modified:    n.ModTime.UTC().Format("2006-01-02T15:04:05Z"),
	}

	switch {
	case in.Section != "":
		body := notes.SectionBody(n, in.Section)
		if body == "" {
			return jsonErr("section not found in %q: %q", n.Path, in.Section)
		}
		resp.Section = in.Section
		resp.Body = body
	case in.Body:
		resp.Body = notes.BodyRaw(n)
	}
	return jsonOK(resp)
}

// buildOutline lifts notes.Heading into the wire-shape outlineEntry.
// Kept in the tool layer so notes.Heading can be reshaped (e.g. add a
// byte-offset) without touching the model-visible JSON.
func buildOutline(headings []notes.Heading) []outlineEntry {
	out := make([]outlineEntry, 0, len(headings))
	for _, h := range headings {
		out = append(out, outlineEntry{Level: h.Level, Text: h.Text, Line: h.Line})
	}
	return out
}

// Sanity assertion: NotesGetTool implements the Tool interface.
var _ Tool = (*NotesGetTool)(nil)

// silence unused-import false-positive in environments where fmt isn't
// referenced after refactors.
var _ = fmt.Sprintf
