package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WriteTool atomically writes a file (temp + fsync + rename), mirroring
// internal/config/config.go's Save discipline. Defaults to "create" mode,
// i.e. fails if the file already exists - the model has to be explicit
// about overwriting, which protects existing artifacts during a partial
// or hallucinated tool call.
//
// Parent directories are created with mode 0700; file mode is 0644 (vs.
// config's 0600). Tool outputs are not secrets in the same way API keys
// are, and a coding agent's writes will frequently need to be readable
// by build tools that drop privileges (think CI).
type WriteTool struct {
	// BaseDir, when non-empty, resolves relative `path` inputs against
	// this directory. Absolute paths are honoured as-is. Used by
	// `carlos please --worktree` so writes land inside the sandbox
	// before propose-don't-publish review. Zero-value = current behaviour.
	BaseDir string
}

// NewWriteTool constructs a WriteTool. No knobs at construction time;
// behaviour is fully driven by the per-call JSON input.
func NewWriteTool() *WriteTool { return &WriteTool{} }

func (*WriteTool) Name() string { return "write" }

func (*WriteTool) Description() string {
	return "Atomically write a text file. Default mode is \"create\" - fails if the file already exists. Use mode \"overwrite\" to replace an existing file. Parent directories are created as needed. Use when creating new files; for surgical edits to an existing file, prefer the `edit` tool."
}

func (*WriteTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Absolute or working-directory-relative path to write."
			},
			"content": {
				"type": "string",
				"description": "Full file contents. UTF-8 only; no embedded NULs."
			},
			"mode": {
				"type": "string",
				"enum": ["create", "overwrite"],
				"description": "\"create\" (default) fails if path exists; \"overwrite\" replaces unconditionally."
			}
		},
		"required": ["path", "content"]
	}`)
}

type writeInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode"`
}

// Execute writes the requested file atomically and returns a short
// human-readable receipt ("wrote N bytes to /path").
func (t *WriteTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in writeInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("write: parse input: %w", err)
	}
	if in.Path == "" {
		return nil, errors.New("write: empty path")
	}
	if in.Mode == "" {
		in.Mode = "create"
	}
	if in.Mode != "create" && in.Mode != "overwrite" {
		return nil, fmt.Errorf("write: invalid mode %q (want create|overwrite)", in.Mode)
	}

	path, err := resolveBaseDir(t.BaseDir, in.Path)
	if err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	// For relative paths resolved into a sandbox BaseDir, reject writes that
	// resolve outside it through a symlink. resolveBaseDir's lexical check
	// only catches `..` escapes; a symlink planted inside the worktree
	// (e.g. `worktree/escape -> /etc`) would slip past it. Absolute paths
	// are a deliberate, user-visible policy escape (see resolveBaseDir) and
	// are intentionally left unchecked.
	if t.BaseDir != "" && !filepath.IsAbs(in.Path) {
		if err := withinBase(t.BaseDir, path); err != nil {
			return nil, fmt.Errorf("write: %w", err)
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("write: mkdir %s: %w", dir, err)
	}

	// create mode must not clobber an existing file. atomicCreate makes the
	// existence check and the creation a single link(2), closing the
	// stat-then-write TOCTOU window the old os.Stat pre-check left open.
	if in.Mode == "create" {
		if err := atomicCreate(path, []byte(in.Content), 0o644); err != nil {
			return nil, err
		}
	} else if err := atomicWrite(path, []byte(in.Content), 0o644); err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("wrote %d bytes to %s\n", len(in.Content), path)), nil
}

// withinBase verifies that target, after resolving symlinks in both it and
// base, stays inside base. It mirrors notes_write's symlink-containment
// check so the write and notes_write tools share one containment rule.
func withinBase(base, target string) error {
	canonBase, err := evalAncestor(base)
	if err != nil {
		return fmt.Errorf("resolve sandbox base %s: %w", base, err)
	}
	canonTarget, err := evalAncestor(target)
	if err != nil {
		return fmt.Errorf("resolve target %s: %w", target, err)
	}
	if !isInside(canonTarget, canonBase) {
		return fmt.Errorf("target %s resolves outside sandbox base %s (symlink containment)", target, base)
	}
	return nil
}

// writeTemp writes data to a freshly created temp file in dir and returns
// its path for the caller to rename or link into place. The temp name is
// randomized via os.CreateTemp (which opens with O_CREATE|O_EXCL), so an
// attacker cannot pre-plant a symlink at a predictable temp path and have
// our write follow it to clobber an out-of-tree target - the old fixed
// `path + ".tmp"` name with O_TRUNC was vulnerable to exactly that.
func writeTemp(dir, base string, data []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("atomicWrite: open tmp in %s: %w", dir, err)
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("atomicWrite: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("atomicWrite: fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("atomicWrite: close tmp: %w", err)
	}
	// CreateTemp forces 0600; restore the caller's intended mode.
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("atomicWrite: chmod tmp: %w", err)
	}
	return tmp, nil
}

// atomicWrite is the shared write primitive used by WriteTool and
// EditTool. It mirrors config.Save's discipline: temp file, fsync,
// rename. POSIX rename is atomic on Darwin and Linux, so a reader either
// sees the old file or the new file - never a torn write.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := writeTemp(filepath.Dir(path), filepath.Base(path), data, mode)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("atomicWrite: rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// atomicCreate writes data to path but fails if path already exists. The
// existence check and the creation are a single os.Link, so - unlike a
// stat-then-rename - there is no window in which a file or symlink that
// appears at path after the check can be silently clobbered.
func atomicCreate(path string, data []byte, mode os.FileMode) error {
	tmp, err := writeTemp(filepath.Dir(path), filepath.Base(path), data, mode)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("write: %s already exists (use mode=overwrite to replace)", path)
		}
		return fmt.Errorf("atomicCreate: link %s -> %s: %w", tmp, path, err)
	}
	return nil
}

var _ Tool = (*WriteTool)(nil)
