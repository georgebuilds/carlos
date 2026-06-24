package todo

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// This file holds the small filesystem + path helpers the Obsidian backend
// needs. They mirror the conventions in internal/tools/notes_write.go (atomic
// write, vault-relative slash paths, containment) so todo writes behave
// exactly like note writes.

// cleanSubtree normalises a frame's vault_subtree to "no leading/trailing
// slash, forward slashes, no `.`" form. Empty in, empty out. Kept local to the
// todo package so it does not depend on internal/tools.
func cleanSubtree(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "/")
	s = path.Clean(s)
	if s == "." {
		return ""
	}
	return s
}

// isInside reports whether p is root or a descendant of it, separator-anchored
// so "/v/a" does not match "/v/a-extra".
func isInside(p, root string) bool {
	if p == root {
		return true
	}
	if !strings.HasPrefix(p, root) {
		return false
	}
	rest := p[len(root):]
	return rest == "" || rest[0] == filepath.Separator
}

// relSlash returns the slash-separated path of abs relative to vault. Falls
// back to the basename if the relative computation fails (it should not for
// paths produced by WalkDir under vault).
func relSlash(vault, abs string) string {
	rel, err := filepath.Rel(vault, abs)
	if err != nil {
		return filepath.Base(abs)
	}
	return filepath.ToSlash(rel)
}

// readLines reads a file and splits it into lines (without the trailing
// newline characters). A trailing newline does NOT produce a final empty
// element being meaningful; callers trim as needed.
func readLines(p string) ([]string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("todo: read %s: %w", p, err)
	}
	return splitLines(string(b)), nil
}

// readLinesAllowMissing is readLines but treats a missing file as an empty
// body, so Add can create the inbox on first use.
func readLinesAllowMissing(p string) ([]string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("todo: read %s: %w", p, err)
	}
	return splitLines(string(b)), nil
}

// splitLines splits on "\n" and strips a trailing "\r" from each line so CRLF
// files round-trip as LF.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	for i := range parts {
		parts[i] = strings.TrimRight(parts[i], "\r")
	}
	// A file ending in "\n" yields a final "" element; keep it so appends can
	// detect and collapse the trailing blank deliberately (callers trim it).
	return parts
}

// writeFileAtomic writes content to target via temp + fsync + rename, creating
// parent directories as needed. The body always ends in exactly one trailing
// newline so the vault file is POSIX-clean.
func writeFileAtomic(target, content string) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("todo: mkdir %s: %w", dir, err)
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	tmp := target + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("todo: open tmp: %w", err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("todo: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("todo: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("todo: close tmp: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("todo: rename: %w", err)
	}
	return nil
}
