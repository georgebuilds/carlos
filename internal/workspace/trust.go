// Package workspace owns carlos's persistent workspace-trust model.
//
// "Trust" here is a per-directory boolean: when the user has marked
// the current working tree as trusted, carlos's LayeredApprover skips
// the y/N prompt for a small, hardcoded set of read-only operations
// (notes_* against the configured vault; bash invocations of git
// status/diff/log/show/blame, ls, pwd, cat, head, tail, wc, file,
// which, echo). Anything else still prompts.
//
// Untrusted is the default. Trust is opt-in via a first-launch modal
// or the /trust slash, and cleared via /untrust. The on-disk file
// lives at ~/.carlos/trusted-workspaces.json with mode 0600 so the
// file inherits the same secrecy as carlos's API-keyed config; the
// directory is created 0700 on first write.
//
// Persisted state is intentionally minimal: path + ISO-8601 trusted-at
// timestamp. We do NOT persist per-workspace command allowlists in v1
// - the allowlist is global (see bash.go) and applies uniformly
// across trusted dirs. Per-workspace overrides are a future slice.
//
// # Why a separate package
//
// internal/agent owns the Approver interface + LayeredApprover; it
// can't import the disk schema without dragging file I/O into the
// chat loop. cmd/carlos wires a workspace.Policy into LayeredApprover
// via SetWorkspacePolicy, keeping the layered-decision engine pure
// and the disk-touching logic linted out of the agent package.
package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry is one record in the on-disk trust file.
type Entry struct {
	// Path is the absolute, symlink-resolved workspace root the user
	// trusted. We resolve via filepath.EvalSymlinks at write time so
	// /tmp and /private/tmp on macOS collapse to one entry.
	Path string `json:"path"`
	// TrustedAt is when the user accepted the prompt. Reserved for
	// future "trust expires after N days" policies.
	TrustedAt time.Time `json:"trusted_at"`
}

// Store is a thread-safe handle to the on-disk trusted-workspaces
// file. Reads cache the loaded set in memory; writes go through Save
// atomically (temp + fsync + rename) so a ctrl-c mid-write never
// leaves a corrupt file.
type Store struct {
	path string

	mu      sync.RWMutex
	loaded  bool
	entries map[string]Entry // key: Entry.Path
}

// DefaultPath returns ~/.carlos/trusted-workspaces.json. Falls back
// to ".carlos/trusted-workspaces.json" (relative) if HOME is unset -
// matches config.DefaultPath's behavior so the two stay parallel.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".carlos", "trusted-workspaces.json")
	}
	return filepath.Join(home, ".carlos", "trusted-workspaces.json")
}

// NewStore returns a Store anchored at path. The file does NOT have
// to exist; the first Save() creates it (and ~/.carlos/ if needed).
func NewStore(path string) *Store {
	return &Store{path: path, entries: map[string]Entry{}}
}

// Load reads the JSON file into the in-memory cache. An absent file
// is not an error - it just means no workspace has been trusted yet.
// Safe to call multiple times; only the first read hits disk.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return nil
	}
	s.loaded = true
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("workspace: read %s: %w", s.path, err)
	}
	if len(raw) == 0 {
		return nil
	}
	var entries []Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("workspace: parse %s: %w", s.path, err)
	}
	for _, e := range entries {
		if e.Path == "" {
			continue
		}
		s.entries[e.Path] = e
	}
	return nil
}

// IsTrusted reports whether path is in the trusted set. path is
// normalized (absolute + symlink-resolved) before lookup so callers
// can pass the literal cwd they got from os.Getwd. Returns (false,
// nil) for an unloaded store - call Load first.
func (s *Store) IsTrusted(path string) (bool, error) {
	norm, err := normalize(path)
	if err != nil {
		return false, err
	}
	if err := s.Load(); err != nil {
		return false, err
	}
	s.mu.RLock()
	_, ok := s.entries[norm]
	s.mu.RUnlock()
	return ok, nil
}

// Trust adds path to the trusted set and persists to disk. Idempotent
// - re-trusting an existing path just refreshes the TrustedAt stamp.
func (s *Store) Trust(path string) error {
	norm, err := normalize(path)
	if err != nil {
		return err
	}
	if err := s.Load(); err != nil {
		return err
	}
	s.mu.Lock()
	s.entries[norm] = Entry{Path: norm, TrustedAt: time.Now().UTC()}
	s.mu.Unlock()
	return s.save()
}

// Untrust removes path from the trusted set and persists. Idempotent
// - removing an absent path is a no-op.
func (s *Store) Untrust(path string) error {
	norm, err := normalize(path)
	if err != nil {
		return err
	}
	if err := s.Load(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.entries, norm)
	s.mu.Unlock()
	return s.save()
}

// List returns every trusted entry in deterministic order (sorted by
// path) so callers rendering tables / overlays get a stable view.
func (s *Store) List() ([]Entry, error) {
	if err := s.Load(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// save writes the entries to disk atomically: temp file in the same
// directory, fsync, rename. Parent dir is created 0700, file is
// 0600 - same secrecy as config.yaml.
func (s *Store) save() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("workspace: mkdir %s: %w", dir, err)
	}
	s.mu.RLock()
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("workspace: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".trusted-workspaces-*.tmp")
	if err != nil {
		return fmt.Errorf("workspace: tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("workspace: chmod tempfile: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("workspace: write tempfile: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("workspace: fsync tempfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("workspace: close tempfile: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("workspace: rename: %w", err)
	}
	return nil
}

// normalize returns the absolute, symlink-resolved form of path. Used
// by every Store entry-key path so /tmp == /private/tmp on macOS and
// the user's ./project == /Users/.../project regardless of cwd at
// /trust time.
func normalize(path string) (string, error) {
	if path == "" {
		return "", errors.New("workspace: empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("workspace: abs: %w", err)
	}
	if eval, err := filepath.EvalSymlinks(abs); err == nil {
		return eval, nil
	}
	// Symlink-resolution failure (e.g. dir doesn't exist yet) shouldn't
	// block the trust write - fall back to the lexical absolute path.
	return abs, nil
}
