package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/georgebuilds/carlos/internal/notes"
)

// NotesWriteTool registers as `notes_write`. Writes a markdown file
// into the configured vault, scoped to the active frame's
// `vault_subtree`. Relative paths join with the active subtree;
// absolute paths must be inside the vault root + subtree or the call
// rejects without touching disk.
//
// Auto-approved by `DefaultBuiltinAllow` because the trust anchor is
// the same as the read-only `notes_*` family: the user explicitly
// configured cfg.Vault.Path during onboarding, AND the write is
// confined to the active frame's slice of that vault. Cross-frame
// writes are rejected here so the model has to use the generic
// `write` tool (which trips Phase F-12's cross-frame approval prompt).
type NotesWriteTool struct {
	env *notesEnv
}

// NewNotesWriteTool ties the tool to the shared cache. Constructed by
// NewDefaultRegistryWithBaseDir{,AndFrames}.
func NewNotesWriteTool(env *notesEnv) *NotesWriteTool { return &NotesWriteTool{env: env} }

func (*NotesWriteTool) Name() string { return "notes_write" }

func (*NotesWriteTool) Description() string {
	return "Atomically write a markdown note into the configured Obsidian vault, scoped to the active frame's vault_subtree. Relative paths join with the active subtree. Mode `create` (default) fails if the file exists; `overwrite` replaces. Cross-frame writes are rejected here; use the generic `write` tool with an absolute path for those (it will prompt you for confirmation). Use this for journaling, capturing decisions, and any note that belongs in the user's current frame."
}

func (*NotesWriteTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"path":    {"type": "string", "description": "Relative path inside the active frame's vault_subtree (e.g. \"notes/devices.md\" or \"journal/2026-06-07.md\"). Absolute paths must resolve inside the vault + subtree or the call rejects."},
			"content": {"type": "string", "description": "Full markdown body. UTF-8 only; no embedded NULs."},
			"mode":    {"type": "string", "enum": ["create", "overwrite"], "description": "\"create\" (default) fails if the file exists; \"overwrite\" replaces unconditionally."}
		},
		"required": ["path", "content"]
	}`)
}

type notesWriteInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode"`
}

type notesWriteResponse struct {
	Path  string `json:"path"`
	Vault string `json:"vault"`
	Frame string `json:"frame,omitempty"`
	Bytes int    `json:"bytes"`
	Mode  string `json:"mode"`
}

// Execute resolves the target path against the active frame's
// vault_subtree, validates that it stays inside the vault, and writes
// atomically. Cache for this vault is invalidated so subsequent
// notes_search hits reflect the new file.
func (t *NotesWriteTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in notesWriteInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("notes_write: parse input: %w", err)
	}
	in.Path = strings.TrimSpace(in.Path)
	if in.Path == "" {
		return nil, errors.New("notes_write: empty path")
	}
	if in.Content == "" {
		return nil, errors.New("notes_write: empty content")
	}
	if in.Mode == "" {
		in.Mode = "create"
	}
	if in.Mode != "create" && in.Mode != "overwrite" {
		return nil, fmt.Errorf("notes_write: invalid mode %q (want create or overwrite)", in.Mode)
	}

	vaultPath := strings.TrimSpace(t.env.cfg.Path)
	if vaultPath == "" {
		return nil, notes.ErrNoVaultConfigured
	}
	absVault, err := filepath.Abs(vaultPath)
	if err != nil {
		return nil, fmt.Errorf("notes_write: vault path: %w", err)
	}
	absVault = filepath.Clean(absVault)

	// Resolve the frame subtree. Empty when no frames are wired
	// (legacy single shelf mode) so writes land at vault root.
	var frameName, subtree string
	if t.env.hasFrames() {
		af := t.env.activeFrame()
		if af == nil {
			return nil, errors.New("notes_write: active frame did not resolve")
		}
		frameName = af.Name
		subtree = cleanSubtree(af.VaultSubtree)
	}

	target, err := resolveNotesWritePath(absVault, subtree, in.Path)
	if err != nil {
		return nil, err
	}

	if in.Mode == "create" {
		if _, err := os.Stat(target); err == nil {
			return nil, fmt.Errorf("notes_write: %s already exists (use mode=overwrite to replace)", relativeToVault(absVault, target))
		}
	}

	if err := writeNotesFileAtomic(target, in.Content); err != nil {
		return nil, err
	}
	t.env.cache.ResetPath(absVault)

	resp := notesWriteResponse{
		Path:  relativeToVault(absVault, target),
		Vault: absVault,
		Frame: frameName,
		Bytes: len(in.Content),
		Mode:  in.Mode,
	}
	return json.Marshal(resp)
}

// resolveNotesWritePath builds the absolute target path. Rules:
//
//  1. Absolute input -> must equal or be a descendant of vault+subtree.
//  2. Relative input -> joined with vault+subtree.
//
// Either way the result is cleaned and re-checked to be inside the
// allowed root so `..` shenanigans are denied. The check is done both
// lexically AND after EvalSymlinks so a symlink planted inside the
// vault that points outside it cannot redirect the write past the
// containment fence.
func resolveNotesWritePath(vault, subtree, in string) (string, error) {
	root := vault
	if subtree != "" {
		root = filepath.Join(vault, subtree)
	}
	var target string
	if filepath.IsAbs(in) {
		target = filepath.Clean(in)
	} else {
		target = filepath.Clean(filepath.Join(root, in))
	}
	if !isInside(target, root) {
		return "", fmt.Errorf("notes_write: target %s is outside the active frame's vault_subtree %s", target, root)
	}
	if filepath.Ext(target) == "" {
		// Default extension so the model doesn't have to remember.
		target += ".md"
	}
	// Symlink-aware containment check: resolve every existing component
	// in both the root and the target, then re-test isInside. Both
	// paths may have not-yet-created suffixes (the frame subtree
	// directory and the target file are commonly auto-created on first
	// write), so we resolve the deepest existing ancestor in each and
	// stitch on the unrealized tail. A symlink inside the vault that
	// points outside it (e.g. `vault/escape -> /etc`) trips this check
	// even though the lexical isInside above would have passed.
	canonicalRoot, err := evalAncestor(root)
	if err != nil {
		return "", fmt.Errorf("notes_write: resolve vault root %s: %w", root, err)
	}
	canonicalTarget, err := evalAncestor(target)
	if err != nil {
		return "", fmt.Errorf("notes_write: resolve target %s: %w", target, err)
	}
	if !isInside(canonicalTarget, canonicalRoot) {
		return "", fmt.Errorf("notes_write: target %s resolves outside the active frame's vault_subtree %s (symlink containment)", target, root)
	}
	return target, nil
}

// evalAncestor returns the canonical (symlink-resolved) path for `p`.
// `p` may not exist yet — we walk up the path component-by-component,
// EvalSymlinks the deepest existing ancestor, and append the remaining
// unrealized suffix. This lets us validate containment for a path
// that's about to be created without requiring the file to exist first.
func evalAncestor(p string) (string, error) {
	p = filepath.Clean(p)
	suffix := ""
	cur := p
	for {
		if cur == "" || cur == string(filepath.Separator) || cur == filepath.VolumeName(cur)+string(filepath.Separator) {
			// Reached filesystem root - resolve what we have and prepend.
			resolved, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			return filepath.Join(resolved, suffix), nil
		}
		if _, err := os.Lstat(cur); err == nil {
			resolved, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			return filepath.Join(resolved, suffix), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		// cur doesn't exist - peel one component and try again.
		parent, last := filepath.Split(cur)
		last = strings.TrimRight(last, string(filepath.Separator))
		parent = strings.TrimRight(parent, string(filepath.Separator))
		if last == "" {
			// Defensive: shouldn't happen after filepath.Clean.
			return p, nil
		}
		if suffix == "" {
			suffix = last
		} else {
			suffix = filepath.Join(last, suffix)
		}
		if parent == "" {
			parent = string(filepath.Separator)
		}
		cur = parent
	}
}

// isInside reports whether path is the same as root or a descendant of
// it. Separator-anchored so "/root/a" doesn't match "/root/a-extra".
func isInside(path, root string) bool {
	if path == root {
		return true
	}
	if !strings.HasPrefix(path, root) {
		return false
	}
	rest := path[len(root):]
	if rest == "" {
		return true
	}
	return rest[0] == filepath.Separator
}

// relativeToVault returns the slash-separated path relative to the
// vault root, for response display. Falls back to the absolute path
// when the rel computation fails.
func relativeToVault(vault, target string) string {
	rel, err := filepath.Rel(vault, target)
	if err != nil {
		return target
	}
	return filepath.ToSlash(rel)
}

// writeNotesFileAtomic mirrors the recipe used by internal/config/config.go
// (temp + fsync + rename) so a ctrl-c mid-write never leaves a half-
// written note. File mode is 0644 to match the rest of the vault.
func writeNotesFileAtomic(target, content string) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("notes_write: mkdir %s: %w", dir, err)
	}
	tmp := target + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("notes_write: open tmp: %w", err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("notes_write: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("notes_write: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("notes_write: close tmp: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("notes_write: rename: %w", err)
	}
	return nil
}
