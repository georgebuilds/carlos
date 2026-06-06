// Phase 12 slices 12a/12b — Obsidian-aware vault tools.
//
// This file holds the shared scaffolding for the seven notes_* tools:
//
//   - notesEnv: a tiny pinning struct so every tool gets the same
//     *notes.Cache + the configured-vault default without each tool
//     reaching into globals.
//   - openVault: the per-call vault resolver. Honors the optional
//     `vault:` field on the tool input, falls back to cfg.Vault.Path,
//     surfaces ErrNoVaultConfigured as the documented error envelope.
//   - jsonErr / jsonOK: the response-shape helpers — every tool
//     returns either a typed success blob or `{"error": "msg"}`.
//
// Each individual tool file (notes_get.go etc.) is small + focused:
// schema, input/output structs, and a single Execute method.
package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/notes"
)

// notesEnv is the shared dependency every notes_* tool holds. The same
// *notes.Cache flows through all seven tools so a vault opened by
// notes_get is reused by notes_search etc., the lazy build cost is
// paid at most once per (vault, process) tuple.
//
// Phase F-11 adds the frame slice: frames is the configured set of
// frames the session knows about, active names which one is currently
// in focus. When frames.List is empty the env is in "legacy single
// shelf mode" and the per-tool frame plumbing degrades to a no-op so
// older call sites keep their pre-frames behavior byte for byte.
type notesEnv struct {
	cache  *notes.Cache
	cfg    config.VaultConfig
	frames frame.Config
	active string
}

// newNotesEnv wires the cache + cfg pair. Constructed once by
// NewDefaultRegistryWithBaseDir and shared across the seven tool
// structs.
func newNotesEnv(cfg config.VaultConfig) *notesEnv {
	return &notesEnv{
		cache: notes.NewCache(cfg.Exclude),
		cfg:   cfg,
	}
}

// newNotesEnvWithFrames is the Phase F-11 constructor: same as
// newNotesEnv but also carries the configured frame list + the active
// frame name. Used by NewDefaultRegistryWithBaseDirAndFrames; the
// older constructor still works for call sites that never wired
// frames.
func newNotesEnvWithFrames(cfg config.VaultConfig, frames frame.Config, active string) *notesEnv {
	return &notesEnv{
		cache:  notes.NewCache(cfg.Exclude),
		cfg:    cfg,
		frames: frames,
		active: active,
	}
}

// hasFrames reports whether the env was wired with at least one frame.
// Tools branch on this to decide between legacy single shelf mode (no
// prefix, no subtree restriction) and the new frame aware paths.
func (e *notesEnv) hasFrames() bool {
	return e != nil && len(e.frames.List) > 0
}

// activeFrame returns the active frame pointer or nil. nil means the
// active frame name didn't resolve OR no frames are wired. The fan-out
// helpers fall back to "no restriction" in that case.
func (e *notesEnv) activeFrame() *frame.Frame {
	if !e.hasFrames() {
		return nil
	}
	name := e.active
	if name == "" {
		name = e.frames.Active
	}
	if name == "" {
		name = e.frames.Default
	}
	return e.frames.Find(name)
}

// resolveFrameArg picks the subtree filter for a tool call given the
// optional `frame:` arg. Rules:
//
//  1. No frames wired ANYWHERE in the env -> ("", "", nil). Caller skips
//     any filtering. This is the legacy single shelf path.
//  2. `frameArg` non-empty -> look it up. Unknown name returns an
//     error so the tool surfaces a clean envelope instead of falling
//     back to the active frame and confusing the model.
//  3. `frameArg` empty + a single resolved active frame -> default to
//     that frame's name + subtree.
//  4. `frameArg` empty + frames wired but the active didn't resolve
//     (config mid-edit, picker not yet run) -> ("", "", nil). Treat as
//     no restriction so READ stays free; the model can re-issue with
//     an explicit frame: if it wants targeting.
//
// The returned `name` is the frame's name (for prefix labels); the
// returned `subtree` is the cleaned forward-slash relpath inside the
// vault root, "" for whole-vault frames.
func (e *notesEnv) resolveFrameArg(frameArg string) (name, subtree string, err error) {
	frameArg = strings.TrimSpace(frameArg)
	if frameArg == "" {
		if !e.hasFrames() {
			return "", "", nil
		}
		af := e.activeFrame()
		if af == nil {
			return "", "", nil
		}
		return af.Name, cleanSubtree(af.VaultSubtree), nil
	}
	// Explicit frame: arg. Must exist; we don't fall back so the model
	// gets a deterministic error rather than silently querying the
	// wrong subtree.
	f := e.frames.Find(frameArg)
	if f == nil {
		return "", "", fmt.Errorf("unknown frame: %q", frameArg)
	}
	return f.Name, cleanSubtree(f.VaultSubtree), nil
}

// frameFanout returns the ordered (name, subtree) pairs a cross-frame
// query should sweep. Empty frame list -> a single ("", "") pair so the
// caller's loop body still runs once without prefixing or filtering
// (legacy single shelf mode). Honors the explicit `frameArg` first
// (single-entry slice) before falling back to all frames.
func (e *notesEnv) frameFanout(frameArg string) ([]frameTarget, error) {
	frameArg = strings.TrimSpace(frameArg)
	if frameArg != "" {
		name, subtree, err := e.resolveFrameArg(frameArg)
		if err != nil {
			return nil, err
		}
		return []frameTarget{{Name: name, Subtree: subtree}}, nil
	}
	if !e.hasFrames() {
		// Legacy single shelf: one empty target so the caller's loop
		// runs once without prefix or restriction.
		return []frameTarget{{}}, nil
	}
	out := make([]frameTarget, 0, len(e.frames.List))
	for _, f := range e.frames.List {
		out = append(out, frameTarget{
			Name:    f.Name,
			Subtree: cleanSubtree(f.VaultSubtree),
		})
	}
	return out, nil
}

// frameTarget is one row in a fan-out plan. Name labels prefix-bearing
// results; Subtree is the relpath filter applied to result paths.
type frameTarget struct {
	Name    string
	Subtree string
}

// cleanSubtree normalises a frame.VaultSubtree to the canonical "no
// trailing slash, forward slashes only, no leading slash" form Notes
// paths use. Empty in, empty out.
func cleanSubtree(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// path.Clean normalises ./, //, trailing slash; we also strip a
	// leading slash so users who write "/work" in YAML get the same
	// result as "work".
	s = strings.TrimPrefix(s, "/")
	s = path.Clean(s)
	if s == "." {
		return ""
	}
	return s
}

// inSubtree reports whether relpath sits inside subtree. Empty subtree
// matches everything. Treats subtree as a directory prefix (so
// "work/notes.md" matches subtree "work" but "workshop/notes.md" does
// NOT).
func inSubtree(relpath, subtree string) bool {
	if subtree == "" {
		return true
	}
	if relpath == subtree {
		return true
	}
	return strings.HasPrefix(relpath, subtree+"/")
}

// openVault resolves the effective vault path for a tool call + opens
// it via the shared cache. The returned (path, *VaultIndex) pair
// carries the canonicalised absolute path the tool puts in its
// response's `vault:` field so the model knows which vault each
// result came from.
//
// perCallVault is the optional `vault:` field from the tool input —
// empty when omitted, in which case cfg.Vault.Path is used.
//
// Errors are returned typed (notes.ErrNoVaultConfigured, fs errors)
// so callers can decide between the configured-vault envelope vs a
// generic indexing failure. In practice every caller routes the error
// to jsonErr so the model sees a clean envelope.
func (e *notesEnv) openVault(perCallVault string) (string, *notes.VaultIndex, error) {
	abs, err := notes.ResolveVaultPath(e.cfg.Path, perCallVault)
	if err != nil {
		return "", nil, err
	}
	v, err := e.cache.Open(abs)
	if err != nil {
		return "", nil, err
	}
	// Cheap mtime poll on every call. For warm vaults this is a
	// stat per .md file (microseconds on a 1000-note vault); for
	// changed vaults it triggers a full re-walk. A real fsnotify
	// watcher (slice 12-future) replaces this.
	if rerr := v.MaybeRefresh(); rerr != nil {
		// Refresh failure shouldn't bring down the tool call — the
		// last-good index is still valid. Surface it as a hint in
		// the response would be nice but the existing envelope
		// doesn't have a slot for warnings; swallow + continue.
		_ = rerr
	}
	return abs, v, nil
}

// jsonErr returns the standard `{"error": "<msg>"}` envelope as the
// tool result. We return (out, nil) rather than (nil, err) so the
// model sees the error inside its tool_result block and can adapt;
// the agent provider treats (nil, err) as a transport failure which
// is heavier-weight than the model needs for "no vault configured".
func jsonErr(format string, args ...any) ([]byte, error) {
	msg := fmt.Sprintf(format, args...)
	return json.Marshal(struct {
		Error string `json:"error"`
	}{Error: msg})
}

// jsonOK marshals v as the tool success response. Marshal errors are
// returned to the caller (the agent provider will surface them as a
// tool-result error which is the correct outcome for a struct that
// can't be encoded — that's an implementation bug, not a model-visible
// state).
func jsonOK(v any) ([]byte, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("notes: marshal response: %w", err)
	}
	return out, nil
}

// noVaultEnvelope is the documented response when neither cfg nor the
// per-call override has set a vault path. Kept as a constant so the
// 7 tools stay byte-identical in this branch.
const noVaultMessage = "vault not configured (run carlos onboard or pass vault: path)"

// resolveOrError is a tiny dispatcher used by tools that take an
// optional `vault:` field + need to surface the configured-vault
// envelope when both cfg + per-call are empty. Returns (path, index,
// nil, nil) on success; (_, _, err-envelope, nil) on the documented
// no-vault case; (_, _, nil, err) for unexpected errors.
func (e *notesEnv) resolveOrError(perCallVault string) (string, *notes.VaultIndex, []byte, error) {
	abs, v, err := e.openVault(perCallVault)
	if err == nil {
		return abs, v, nil, nil
	}
	if errors.Is(err, notes.ErrNoVaultConfigured) {
		env, merr := jsonErr("%s", noVaultMessage)
		return "", nil, env, merr
	}
	env, merr := jsonErr("notes: open vault: %v", err)
	return "", nil, env, merr
}

// notFoundResponse is a small struct used by tools that resolve a
// specific note (notes_get, notes_backlinks, notes_neighbors,
// notes_resolve). Keeps the response shape uniform.
func notFoundResponse(name string) ([]byte, error) {
	return jsonErr("note not found: %q", name)
}

// asString unmarshals input + returns the string value of field.
// Used by a couple of tools that take only a single string field;
// avoids declaring a one-field struct per tool.
func asString(input []byte, field string) (string, error) {
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	v, ok := m[field]
	if !ok {
		return "", fmt.Errorf("missing required field: %q", field)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("field %q must be a string", field)
	}
	return strings.TrimSpace(s), nil
}
