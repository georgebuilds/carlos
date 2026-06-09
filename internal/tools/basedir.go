package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// resolveBaseDir returns path joined onto baseDir when path is relative
// and baseDir is non-empty; otherwise returns path verbatim.
//
// Used by the read/write/edit/grep/glob/bash tools when the foreground
// has opened a worktree-per-coding-task sandbox and wants the model's
// file ops to land inside that sandbox instead of the user's cwd. The
// zero-value (empty baseDir) preserves the original "use cwd / honour
// absolute paths" behaviour - every existing call site stays
// bit-identical when BaseDir is left unset.
//
// Absolute paths are honoured as-is so a model that explicitly asks for
// /etc/hosts (or any path outside the worktree) is not silently
// redirected - that's a user-visible policy decision, not a stealthy
// reroute.
//
// Relative paths containing `..` segments that would escape baseDir
// are rejected with an error. Without that check, a path like
// `../../etc/passwd` cleans down to `/etc/passwd` inside filepath.Join
// and silently breaks containment - exactly the failure mode the
// sandbox is supposed to prevent.
func resolveBaseDir(baseDir, path string) (string, error) {
	if baseDir == "" {
		return path, nil
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	joined := filepath.Join(baseDir, path)
	rel, err := filepath.Rel(baseDir, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes sandbox base %q", path, baseDir)
	}
	return joined, nil
}
