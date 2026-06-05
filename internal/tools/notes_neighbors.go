package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/georgebuilds/carlos/internal/notes"
)

// NotesNeighborsTool registers as `notes_neighbors`. Returns the
// outgoing + incoming neighbors of the named note in one call, plus
// the list of unresolved wikilinks (ghost links).
type NotesNeighborsTool struct {
	env *notesEnv
}

func NewNotesNeighborsTool(env *notesEnv) *NotesNeighborsTool {
	return &NotesNeighborsTool{env: env}
}

func (*NotesNeighborsTool) Name() string { return "notes_neighbors" }

func (*NotesNeighborsTool) Description() string {
	return "Return a note's outgoing + incoming neighbors in one call, plus the list of unresolved (`ghost`) wikilinks. For \"what's connected to this note\" without two separate queries. Pass an optional `vault:` field to query a different markdown vault than carlos's default."
}

func (*NotesNeighborsTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"note":  {"type": "string"},
			"vault": {"type": "string", "description": "Optional absolute or ~-relative path to a different Obsidian-flavored markdown vault. Defaults to carlos's configured vault."}
		},
		"required": ["note"]
	}`)
}

type notesNeighborsInput struct {
	Note  string `json:"note"`
	Vault string `json:"vault"`
}

type notesNeighborsResponse struct {
	Note          string           `json:"note"`
	Vault         string           `json:"vault"`
	Outgoing      []neighborEntry  `json:"outgoing"`
	Incoming      []neighborEntry  `json:"incoming"`
	UnresolvedOut []unresolvedLink `json:"unresolved_out"`
}

type neighborEntry struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}

type unresolvedLink struct {
	Display string `json:"display"`
	Line    int    `json:"line"`
}

func (t *NotesNeighborsTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in notesNeighborsInput
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
		return jsonErr("notes_neighbors: %v", err)
	}

	out, incoming, unres, err := v.Neighbors(in.Note)
	if err != nil {
		if errors.Is(err, notes.ErrNotFound) {
			return notFoundResponse(in.Note)
		}
		return jsonErr("notes_neighbors: %v", err)
	}
	resolved, _, _ := v.Resolve(in.Note)

	resp := notesNeighborsResponse{
		Note:          resolved,
		Vault:         abs,
		Outgoing:      make([]neighborEntry, 0, len(out)),
		Incoming:      make([]neighborEntry, 0, len(incoming)),
		UnresolvedOut: make([]unresolvedLink, 0, len(unres)),
	}
	for _, n := range out {
		resp.Outgoing = append(resp.Outgoing, neighborEntry{Path: n.Path, Title: n.Title})
	}
	for _, n := range incoming {
		resp.Incoming = append(resp.Incoming, neighborEntry{Path: n.Path, Title: n.Title})
	}
	for _, u := range unres {
		resp.UnresolvedOut = append(resp.UnresolvedOut, unresolvedLink{
			Display: u.Display,
			Line:    u.Line,
		})
	}
	return jsonOK(resp)
}

var _ Tool = (*NotesNeighborsTool)(nil)
