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
	"strings"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/notes"
)

// notesEnv is the shared dependency every notes_* tool holds. The same
// *notes.Cache flows through all seven tools so a vault opened by
// notes_get is reused by notes_search etc. — the lazy build cost is
// paid at most once per (vault, process) tuple.
type notesEnv struct {
	cache *notes.Cache
	cfg   config.VaultConfig
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
