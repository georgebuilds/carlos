package tools

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

// NotesRecentTool registers as `notes_recent` — the "what was I just
// working on" helper. Returns the N most-recently-modified notes,
// optionally restricted to a time window.
//
// Phase F-11 frame aware: omitted `frame:` fans out across every
// configured frame and labels each hit; explicit `frame:` restricts to
// that frame's vault_subtree. Legacy single shelf mode is unchanged.
type NotesRecentTool struct {
	env *notesEnv
}

func NewNotesRecentTool(env *notesEnv) *NotesRecentTool { return &NotesRecentTool{env: env} }

func (*NotesRecentTool) Name() string { return "notes_recent" }

func (*NotesRecentTool) Description() string {
	return "Return the most-recently-modified notes in your configured Obsidian vault. Optional `since` window (e.g. `24h`, `7d`). For \"what was I working on\" context recovery. Use obsidian_recent to query a different vault. When frames are configured, an omitted `frame:` fans out across every frame's vault_subtree and labels each hit with its source frame; pass `frame:` to restrict to that one frame's subtree."
}

func (*NotesRecentTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"limit": {"type": "integer", "description": "Default 10."},
			"since": {"type": "string", "description": "Optional duration like '24h' or '7d'. Default: no cutoff (newest N)."},
			"frame": {"type": "string", "description": "Optional frame name. Restricts results to that frame's vault_subtree. When omitted, defaults to the active frame's subtree; with multiple frames configured, cross-frame results are labelled with their source frame in the frame field on each entry."}
		}
	}`)
}

type notesRecentInput struct {
	Limit int    `json:"limit"`
	Since string `json:"since"`
	Frame string `json:"frame"`
}

type notesRecentResponse struct {
	Since string             `json:"since,omitempty"`
	Vault string             `json:"vault"`
	Notes []notesRecentEntry `json:"notes"`
}

type notesRecentEntry struct {
	// Frame is the source frame name when the env has frames wired.
	// Empty in legacy single shelf mode.
	Frame    string   `json:"frame,omitempty"`
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
	abs, v, envelope, err := t.env.resolveOrError("")
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("notes_recent: %v", err)
	}

	targets, ferr := t.env.frameFanout(in.Frame)
	if ferr != nil {
		return jsonErr("notes_recent: %v", ferr)
	}

	var since time.Duration
	if in.Since != "" {
		d, perr := parseDuration(in.Since)
		if perr != nil {
			return jsonErr("invalid since=%q: %v", in.Since, perr)
		}
		since = d
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}

	// Pull every note in the cutoff window (Recent with limit=0
	// returns up to its default cap of 10; we pass a high cap so
	// per-frame filtering doesn't lose entries).
	hits := v.Recent(1<<31-1, since)

	seen := map[string]bool{}
	combined := make([]notesRecentEntry, 0, len(hits))
	for _, tg := range targets {
		for _, n := range hits {
			if !inSubtree(n.Path, tg.Subtree) {
				continue
			}
			if seen[n.Path] {
				continue
			}
			seen[n.Path] = true
			combined = append(combined, notesRecentEntry{
				Frame:    tg.Name,
				Path:     n.Path,
				Title:    n.Title,
				Modified: n.ModTime.UTC().Format("2006-01-02T15:04:05Z"),
				Tags:     n.Tags,
			})
		}
	}
	// Re-sort by modtime desc across frames (Recent returns desc per
	// scan but our fan out interleaves them).
	sort.SliceStable(combined, func(i, j int) bool {
		return combined[i].Modified > combined[j].Modified
	})
	if len(combined) > limit {
		combined = combined[:limit]
	}

	resp := notesRecentResponse{
		Since: in.Since,
		Vault: abs,
		Notes: combined,
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
