package tools

import (
	"context"
	"encoding/json"
	"time"
)

// NotesRecentTool registers as `notes_recent` — the "what was I just
// working on" helper. Returns the N most-recently-modified notes,
// optionally restricted to a time window.
type NotesRecentTool struct {
	env *notesEnv
}

func NewNotesRecentTool(env *notesEnv) *NotesRecentTool { return &NotesRecentTool{env: env} }

func (*NotesRecentTool) Name() string { return "notes_recent" }

func (*NotesRecentTool) Description() string {
	return "Return the most-recently-modified notes in the vault. Optional `since` window (e.g. `24h`, `7d`). For \"what was I working on\" context recovery. Pass an optional `vault:` field to query a different markdown vault than carlos's default."
}

func (*NotesRecentTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"limit": {"type": "integer", "description": "Default 10."},
			"since": {"type": "string", "description": "Optional duration like '24h' or '7d'. Default: no cutoff (newest N)."},
			"vault": {"type": "string", "description": "Optional absolute or ~-relative path to a different Obsidian-flavored markdown vault. Defaults to carlos's configured vault."}
		}
	}`)
}

type notesRecentInput struct {
	Limit int    `json:"limit"`
	Since string `json:"since"`
	Vault string `json:"vault"`
}

type notesRecentResponse struct {
	Since string             `json:"since,omitempty"`
	Vault string             `json:"vault"`
	Notes []notesRecentEntry `json:"notes"`
}

type notesRecentEntry struct {
	Path     string   `json:"path"`
	Title    string   `json:"title"`
	Modified string   `json:"modified"`
	Tags     []string `json:"tags"`
}

func (t *NotesRecentTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in notesRecentInput
	// Empty input is allowed — `notes_recent {}` is a valid call.
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return jsonErr("parse input: %v", err)
		}
	}
	abs, v, envelope, err := t.env.resolveOrError(in.Vault)
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("notes_recent: %v", err)
	}

	var since time.Duration
	if in.Since != "" {
		d, perr := parseDuration(in.Since)
		if perr != nil {
			return jsonErr("invalid since=%q: %v", in.Since, perr)
		}
		since = d
	}
	hits := v.Recent(in.Limit, since)

	resp := notesRecentResponse{
		Since: in.Since,
		Vault: abs,
		Notes: make([]notesRecentEntry, 0, len(hits)),
	}
	for _, n := range hits {
		resp.Notes = append(resp.Notes, notesRecentEntry{
			Path:     n.Path,
			Title:    n.Title,
			Modified: n.ModTime.UTC().Format("2006-01-02T15:04:05Z"),
			Tags:     n.Tags,
		})
	}
	return jsonOK(resp)
}

// parseDuration accepts the Go-standard forms (`24h`, `90m`) and the
// extra `Nd` shorthand Obsidian users tend to write. We translate
// `d` to hours since `time.ParseDuration` doesn't recognise days.
func parseDuration(s string) (time.Duration, error) {
	if len(s) >= 2 && s[len(s)-1] == 'd' {
		// Parse the numeric prefix.
		var n int
		for i := 0; i < len(s)-1; i++ {
			c := s[i]
			if c < '0' || c > '9' {
				return time.ParseDuration(s) // fall through to standard parser
			}
			n = n*10 + int(c-'0')
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

var _ Tool = (*NotesRecentTool)(nil)
