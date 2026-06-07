// obsidian_* tool family (Phase T-1).
//
// Mirror of notes_*, but every tool REQUIRES a `vault:` field
// instead of defaulting to the configured vault. That separation is
// what makes the permission story honest:
//
//   - notes_* - operates on the user's configured vault only. The
//     LayeredApprover auto-approves these because the trust came
//     from the configuration boundary.
//   - obsidian_* - operates on whatever vault the model passes per
//     call. Always prompts the user; the model has to convince them
//     that THIS particular vault path is one they want carlos to
//     read.
//
// Same underlying notesEnv + notes.Cache + notes.VaultIndex. Each
// tool here is a 30-line wrapper that adapts the schema + delegates
// to the same response shape the notes_* tool emits.
//
// Tests live alongside the notes_* tests where it makes sense to
// share fixtures; new obsidian-specific assertions get their own
// file (obsidian_tools_test.go).
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/georgebuilds/carlos/internal/notes"
)

// vaultRequired is the shared schema fragment for the `vault:` property
// VALUE (not the key). Each obsidian_* tool writes the key in its
// schema literal and interpolates this const as the value, keeping the
// wording consistent across the seven schemas.
//
// Phase F-11 adds the parallel `frame:` shorthand. When both are
// passed the explicit `vault:` wins (explicit beats sugar). When only
// `frame:` is set the obsidian_* tool resolves it against the
// configured frame list and uses that frame's vault_subtree joined
// against the configured vault path.
const vaultRequired = `{"type": "string", "description": "Absolute or ~-relative path to the target Obsidian-flavored markdown vault. Required unless the frame field is set, use notes_* tools to query the user's configured vault."}`

// frameShorthand is the schema fragment for the optional `frame:` field
// on obsidian_*. Lives here so every obsidian_* tool can interpolate
// the same wording without going out of sync.
const frameShorthand = `{"type": "string", "description": "Optional frame name. When set without vault, resolves to the configured vault path joined with that frame's vault_subtree. When vault is also set, the explicit path wins; frame then only labels results in cross-frame fan outs."}`

// resolveObsidianVault picks the effective (path, subtree, name) trio
// for an obsidian_* tool call given the optional vault + frame args.
// Returns:
//
//   - explicit `vault:` -> (vault, "", "") regardless of frame.
//   - `frame:` only -> (cfg.Vault.Path, frame.VaultSubtree, frame.Name).
//   - neither -> error envelope is delegated to the caller via the
//     standard "missing required field" check (we return zeros + nil
//     err so the caller's existing `if in.Vault == ""` short-circuit
//     still fires).
//
// Unknown frame name surfaces as an error so the model sees a clean
// envelope.
func (e *notesEnv) resolveObsidianVault(vaultArg, frameArg string) (vault, subtree, name string, err error) {
	vaultArg = strings.TrimSpace(vaultArg)
	frameArg = strings.TrimSpace(frameArg)
	if vaultArg != "" {
		// Explicit vault wins. Still resolve the frame name for
		// downstream prefix labelling if the caller threaded it.
		if frameArg != "" {
			n, sub, ferr := e.resolveFrameArg(frameArg)
			if ferr != nil {
				return "", "", "", ferr
			}
			return vaultArg, sub, n, nil
		}
		return vaultArg, "", "", nil
	}
	if frameArg == "" {
		return "", "", "", nil
	}
	// Frame-only path: use the configured vault + frame's subtree.
	n, sub, ferr := e.resolveFrameArg(frameArg)
	if ferr != nil {
		return "", "", "", ferr
	}
	return e.cfg.Path, sub, n, nil
}

// --- obsidian_get -------------------------------------------------

type ObsidianGetTool struct{ env *notesEnv }

func NewObsidianGetTool(env *notesEnv) *ObsidianGetTool { return &ObsidianGetTool{env: env} }

func (*ObsidianGetTool) Name() string { return "obsidian_get" }

func (*ObsidianGetTool) Description() string {
	return "Fetch a note's structure from an arbitrary Obsidian vault: frontmatter, outline, link counts, modtime. Requires `vault:` (or `frame:` shorthand to target the configured vault's frame subtree). For the user's configured vault use notes_get instead."
}

func (*ObsidianGetTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"vault":   ` + vaultRequired + `,
			"note":    {"type": "string", "description": "Note name or relpath."},
			"section": {"type": "string"},
			"body":    {"type": "boolean"},
			"frame":   ` + frameShorthand + `
		},
		"required": ["note"]
	}`)
}

type obsidianGetInput struct {
	Vault   string `json:"vault"`
	Note    string `json:"note"`
	Section string `json:"section"`
	Body    bool   `json:"body"`
	Frame   string `json:"frame"`
}

func (t *ObsidianGetTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in obsidianGetInput
	if err := json.Unmarshal(input, &in); err != nil {
		return jsonErr("parse input: %v", err)
	}
	vault, subtree, _, verr := t.env.resolveObsidianVault(in.Vault, in.Frame)
	if verr != nil {
		return jsonErr("obsidian_get: %v", verr)
	}
	if vault == "" {
		return jsonErr("missing required field: %q", "vault")
	}
	if in.Note == "" {
		return jsonErr("missing required field: %q", "note")
	}
	abs, v, envelope, err := t.env.resolveOrError(vault)
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("obsidian_get: %v", err)
	}
	n, err := v.Get(in.Note)
	if err != nil {
		if errors.Is(err, notes.ErrNotFound) {
			return notFoundResponse(in.Note)
		}
		return jsonErr("obsidian_get: %v", err)
	}
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

var _ Tool = (*ObsidianGetTool)(nil)

// --- obsidian_search ----------------------------------------------

type ObsidianSearchTool struct{ env *notesEnv }

func NewObsidianSearchTool(env *notesEnv) *ObsidianSearchTool {
	return &ObsidianSearchTool{env: env}
}

func (*ObsidianSearchTool) Name() string { return "obsidian_search" }

func (*ObsidianSearchTool) Description() string {
	return "Search an arbitrary Obsidian vault by free-text query, returning paragraph snippets. Requires `vault:` (or `frame:` shorthand to target the configured vault's frame subtree). For the user's configured vault use notes_search instead."
}

func (*ObsidianSearchTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"vault": ` + vaultRequired + `,
			"query": {"type": "string"},
			"tag":   {"type": "string"},
			"where": {"type": "object"},
			"limit": {"type": "integer"},
			"frame": ` + frameShorthand + `
		},
		"required": ["query"]
	}`)
}

type obsidianSearchInput struct {
	Vault string         `json:"vault"`
	Query string         `json:"query"`
	Tag   string         `json:"tag"`
	Where map[string]any `json:"where"`
	Limit int            `json:"limit"`
	Frame string         `json:"frame"`
}

func (t *ObsidianSearchTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in obsidianSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return jsonErr("parse input: %v", err)
	}
	vault, subtree, frameName, verr := t.env.resolveObsidianVault(in.Vault, in.Frame)
	if verr != nil {
		return jsonErr("obsidian_search: %v", verr)
	}
	if vault == "" {
		return jsonErr("missing required field: %q", "vault")
	}
	if in.Query == "" {
		return jsonErr("missing required field: %q", "query")
	}
	abs, v, envelope, err := t.env.resolveOrError(vault)
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("obsidian_search: %v", err)
	}
	opts := notes.SearchOptions{Tag: in.Tag, Where: in.Where, Limit: in.Limit}
	hits, err := v.Search(in.Query, opts)
	if err != nil {
		return jsonErr("obsidian_search: %v", err)
	}
	total, err := v.SearchTotal(in.Query, opts)
	if err != nil {
		return jsonErr("obsidian_search: %v", err)
	}
	matches := make([]notesSearchMatch, 0, len(hits))
	filteredTotal := 0
	for _, h := range hits {
		if !inSubtree(h.Path, subtree) {
			continue
		}
		filteredTotal++
		matches = append(matches, notesSearchMatch{
			Frame:   frameName,
			Path:    h.Path,
			Title:   h.Title,
			Snippet: h.Snippet,
			Line:    h.Line,
			Score:   h.Score,
		})
	}
	if subtree != "" {
		// Subtree restricted: trust the filtered count, the index's
		// total over-counts here.
		total = filteredTotal
	}
	resp := notesSearchResponse{
		Query:     in.Query,
		Vault:     abs,
		Matches:   matches,
		Total:     total,
		Truncated: total > len(matches),
	}
	return jsonOK(resp)
}

var _ Tool = (*ObsidianSearchTool)(nil)

// --- obsidian_backlinks -------------------------------------------

type ObsidianBacklinksTool struct{ env *notesEnv }

func NewObsidianBacklinksTool(env *notesEnv) *ObsidianBacklinksTool {
	return &ObsidianBacklinksTool{env: env}
}

func (*ObsidianBacklinksTool) Name() string { return "obsidian_backlinks" }

func (*ObsidianBacklinksTool) Description() string {
	return "List every note in an arbitrary Obsidian vault that wikilinks to the given target. Requires `vault:`."
}

func (*ObsidianBacklinksTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"vault": ` + vaultRequired + `,
			"note":  {"type": "string"},
			"limit": {"type": "integer"},
			"frame": ` + frameShorthand + `
		},
		"required": ["note"]
	}`)
}

type obsidianBacklinksInput struct {
	Vault string `json:"vault"`
	Note  string `json:"note"`
	Limit int    `json:"limit"`
	Frame string `json:"frame"`
}

func (t *ObsidianBacklinksTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in obsidianBacklinksInput
	if err := json.Unmarshal(input, &in); err != nil {
		return jsonErr("parse input: %v", err)
	}
	vault, subtree, _, verr := t.env.resolveObsidianVault(in.Vault, in.Frame)
	if verr != nil {
		return jsonErr("obsidian_backlinks: %v", verr)
	}
	if vault == "" {
		return jsonErr("missing required field: %q", "vault")
	}
	if in.Note == "" {
		return jsonErr("missing required field: %q", "note")
	}
	abs, v, envelope, err := t.env.resolveOrError(vault)
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("obsidian_backlinks: %v", err)
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
		return jsonErr("obsidian_backlinks: %v", err)
	}
	target, _ := v.Get(resolved)
	resp := notesBacklinksResponse{
		Target:    resolved,
		Vault:     abs,
		Backlinks: make([]backlinkEntry, 0, len(bl)),
		Total:     len(bl),
	}
	if target != nil {
		resp.Title = target.Title
	}
	for _, b := range bl {
		resp.Backlinks = append(resp.Backlinks, backlinkEntry{
			Path: b.Path, Title: b.Title, Context: b.Context, Line: b.Line,
		})
	}
	return jsonOK(resp)
}

var _ Tool = (*ObsidianBacklinksTool)(nil)

// --- obsidian_tagged ----------------------------------------------

type ObsidianTaggedTool struct{ env *notesEnv }

func NewObsidianTaggedTool(env *notesEnv) *ObsidianTaggedTool {
	return &ObsidianTaggedTool{env: env}
}

func (*ObsidianTaggedTool) Name() string { return "obsidian_tagged" }

func (*ObsidianTaggedTool) Description() string {
	return "List every note in an arbitrary Obsidian vault carrying the given tag. Requires `vault:`."
}

func (*ObsidianTaggedTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"vault": ` + vaultRequired + `,
			"tag":   {"type": "string"},
			"limit": {"type": "integer"},
			"frame": ` + frameShorthand + `
		},
		"required": ["tag"]
	}`)
}

type obsidianTaggedInput struct {
	Vault string `json:"vault"`
	Tag   string `json:"tag"`
	Limit int    `json:"limit"`
	Frame string `json:"frame"`
}

func (t *ObsidianTaggedTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in obsidianTaggedInput
	if err := json.Unmarshal(input, &in); err != nil {
		return jsonErr("parse input: %v", err)
	}
	vault, subtree, frameName, verr := t.env.resolveObsidianVault(in.Vault, in.Frame)
	if verr != nil {
		return jsonErr("obsidian_tagged: %v", verr)
	}
	if vault == "" {
		return jsonErr("missing required field: %q", "vault")
	}
	if in.Tag == "" {
		return jsonErr("missing required field: %q", "tag")
	}
	abs, v, envelope, err := t.env.resolveOrError(vault)
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("obsidian_tagged: %v", err)
	}
	hits, err := v.Tagged(in.Tag, 0)
	if err != nil {
		return jsonErr("obsidian_tagged: %v", err)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	entries := make([]taggedEntry, 0, len(hits))
	for _, n := range hits {
		if !inSubtree(n.Path, subtree) {
			continue
		}
		entries = append(entries, taggedEntry{
			Frame: frameName,
			Path:  n.Path, Title: n.Title,
			Description: notes.Description(n),
			Modified:    n.ModTime.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	total := len(entries)
	if len(entries) > limit {
		entries = entries[:limit]
	}
	resp := notesTaggedResponse{
		Tag:   in.Tag,
		Vault: abs,
		Notes: entries,
		Total: total,
	}
	return jsonOK(resp)
}

var _ Tool = (*ObsidianTaggedTool)(nil)

// --- obsidian_neighbors -------------------------------------------

type ObsidianNeighborsTool struct{ env *notesEnv }

func NewObsidianNeighborsTool(env *notesEnv) *ObsidianNeighborsTool {
	return &ObsidianNeighborsTool{env: env}
}

func (*ObsidianNeighborsTool) Name() string { return "obsidian_neighbors" }

func (*ObsidianNeighborsTool) Description() string {
	return "Return a note's outgoing + incoming neighbors in one call in an arbitrary Obsidian vault. Requires `vault:`."
}

func (*ObsidianNeighborsTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"vault": ` + vaultRequired + `,
			"note":  {"type": "string"},
			"frame": ` + frameShorthand + `
		},
		"required": ["note"]
	}`)
}

type obsidianNeighborsInput struct {
	Vault string `json:"vault"`
	Note  string `json:"note"`
	Frame string `json:"frame"`
}

func (t *ObsidianNeighborsTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in obsidianNeighborsInput
	if err := json.Unmarshal(input, &in); err != nil {
		return jsonErr("parse input: %v", err)
	}
	vault, subtree, _, verr := t.env.resolveObsidianVault(in.Vault, in.Frame)
	if verr != nil {
		return jsonErr("obsidian_neighbors: %v", verr)
	}
	if vault == "" {
		return jsonErr("missing required field: %q", "vault")
	}
	if in.Note == "" {
		return jsonErr("missing required field: %q", "note")
	}
	abs, v, envelope, err := t.env.resolveOrError(vault)
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("obsidian_neighbors: %v", err)
	}
	resolved, _, _ := v.Resolve(in.Note)
	if resolved != "" && !inSubtree(resolved, subtree) {
		return notFoundResponse(in.Note)
	}
	out, incoming, unres, err := v.Neighbors(in.Note)
	if err != nil {
		if errors.Is(err, notes.ErrNotFound) {
			return notFoundResponse(in.Note)
		}
		return jsonErr("obsidian_neighbors: %v", err)
	}
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
			Display: u.Display, Line: u.Line,
		})
	}
	return jsonOK(resp)
}

var _ Tool = (*ObsidianNeighborsTool)(nil)

// --- obsidian_recent ----------------------------------------------

type ObsidianRecentTool struct{ env *notesEnv }

func NewObsidianRecentTool(env *notesEnv) *ObsidianRecentTool { return &ObsidianRecentTool{env: env} }

func (*ObsidianRecentTool) Name() string { return "obsidian_recent" }

func (*ObsidianRecentTool) Description() string {
	return "Return the most-recently-modified notes from an arbitrary Obsidian vault. Requires `vault:`."
}

func (*ObsidianRecentTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"vault": ` + vaultRequired + `,
			"limit": {"type": "integer"},
			"since": {"type": "string"},
			"frame": ` + frameShorthand + `
		}
	}`)
}

type obsidianRecentInput struct {
	Vault string `json:"vault"`
	Limit int    `json:"limit"`
	Since string `json:"since"`
	Frame string `json:"frame"`
}

func (t *ObsidianRecentTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in obsidianRecentInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return jsonErr("parse input: %v", err)
		}
	}
	vault, subtree, frameName, verr := t.env.resolveObsidianVault(in.Vault, in.Frame)
	if verr != nil {
		return jsonErr("obsidian_recent: %v", verr)
	}
	if vault == "" {
		return jsonErr("missing required field: %q", "vault")
	}
	abs, v, envelope, err := t.env.resolveOrError(vault)
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("obsidian_recent: %v", err)
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
	hits := v.Recent(1<<31-1, since)
	entries := make([]notesRecentEntry, 0, len(hits))
	for _, n := range hits {
		if !inSubtree(n.Path, subtree) {
			continue
		}
		entries = append(entries, notesRecentEntry{
			Frame: frameName,
			Path:  n.Path, Title: n.Title,
			Modified: n.ModTime.UTC().Format("2006-01-02T15:04:05Z"),
			Tags:     n.Tags,
		})
		if len(entries) >= limit {
			break
		}
	}
	resp := notesRecentResponse{
		Since: in.Since,
		Vault: abs,
		Notes: entries,
	}
	return jsonOK(resp)
}

var _ Tool = (*ObsidianRecentTool)(nil)

// --- obsidian_resolve ---------------------------------------------

type ObsidianResolveTool struct{ env *notesEnv }

func NewObsidianResolveTool(env *notesEnv) *ObsidianResolveTool {
	return &ObsidianResolveTool{env: env}
}

func (*ObsidianResolveTool) Name() string { return "obsidian_resolve" }

func (*ObsidianResolveTool) Description() string {
	return "Resolve a wikilink to its target relpath in an arbitrary Obsidian vault. Requires `vault:`."
}

func (*ObsidianResolveTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"vault": ` + vaultRequired + `,
			"link":  {"type": "string"},
			"frame": ` + frameShorthand + `
		},
		"required": ["link"]
	}`)
}

type obsidianResolveInput struct {
	Vault string `json:"vault"`
	Link  string `json:"link"`
	Frame string `json:"frame"`
}

func (t *ObsidianResolveTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in obsidianResolveInput
	if err := json.Unmarshal(input, &in); err != nil {
		return jsonErr("parse input: %v", err)
	}
	vault, subtree, _, verr := t.env.resolveObsidianVault(in.Vault, in.Frame)
	if verr != nil {
		return jsonErr("obsidian_resolve: %v", verr)
	}
	if vault == "" {
		return jsonErr("missing required field: %q", "vault")
	}
	if in.Link == "" {
		return jsonErr("missing required field: %q", "link")
	}
	abs, v, envelope, err := t.env.resolveOrError(vault)
	if envelope != nil {
		return envelope, err
	}
	if err != nil {
		return jsonErr("obsidian_resolve: %v", err)
	}
	resolved, cands, err := v.Resolve(in.Link)
	if err != nil {
		if errors.Is(err, notes.ErrNotFound) {
			return notFoundResponse(in.Link)
		}
		return jsonErr("obsidian_resolve: %v", err)
	}
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
			resp.Candidates = append(resp.Candidates, resolveCandidate{
				Path: c.Path, Title: c.Title,
			})
		}
	}
	return jsonOK(resp)
}

var _ Tool = (*ObsidianResolveTool)(nil)
