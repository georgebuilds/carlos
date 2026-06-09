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

	if in.Mode == "create" {
		if _, err := os.Stat(path); err == nil {
			return nil, fmt.Errorf("write: %s already exists (use mode=overwrite to replace)", path)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("write: stat %s: %w", path, err)
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("write: mkdir %s: %w", dir, err)
	}

	if err := atomicWrite(path, []byte(in.Content), 0o644); err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("wrote %d bytes to %s\n", len(in.Content), path)), nil
}

// atomicWrite is the shared write primitive used by WriteTool and
// EditTool. It mirrors config.Save's discipline: temp file, fsync,
// rename. POSIX rename is atomic on Darwin and Linux, so a reader either
// sees the old file or the new file - never a torn write.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("atomicWrite: open tmp %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("atomicWrite: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("atomicWrite: fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("atomicWrite: close tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("atomicWrite: rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

var _ Tool = (*WriteTool)(nil)
