package tools

import "path/filepath"

// resolveBaseDir returns path joined onto baseDir when path is relative
// and baseDir is non-empty; otherwise returns path verbatim.
//
// Used by the read/write/edit/grep/glob/bash tools when the foreground
// has opened a worktree-per-coding-task sandbox and wants the model's
// file ops to land inside that sandbox instead of the user's cwd. The
// zero-value (empty baseDir) preserves the original "use cwd / honour
// absolute paths" behaviour — every existing call site stays
// bit-identical when BaseDir is left unset.
//
// Absolute paths are honoured as-is so a model that explicitly asks for
// /etc/hosts (or any path outside the worktree) is not silently
// redirected — that's a user-visible policy decision, not a stealthy
// reroute.
func resolveBaseDir(baseDir, path string) string {
	if baseDir == "" {
		return path
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}
