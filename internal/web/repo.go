package web

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// repo.go: the git-repo resolver + the web-owned per-thread repo table
// (plan WR-0). A thread's repo identity is the git root of its cwd; the
// roster groups conversation cards by it. The table is backend-agnostic
// (no agents join), so a cc:<uuid> id stores a repo row exactly like a
// carlos thread.

// RepoRef is the repo a thread belongs to: the absolute git root and its
// display basename. Carried on ThreadSummary (omitempty) when resolvable.
type RepoRef struct {
	Root string `json:"root"`
	Name string `json:"name"`
}

// gitRepoRoot walks up from cwd looking for a .git entry and returns the
// repository's MAIN worktree root plus its basename. A .git directory is
// the ordinary case (root = the dir containing it). A .git FILE marks a
// linked worktree: it holds a "gitdir: <path>/.git/worktrees/<name>" line,
// and the main root is the directory that owns that .git (strip the
// "/.git/worktrees/<name>" suffix) so a sub-agent worktree folds into its
// parent repo. An empty cwd or no .git found anywhere yields ok=false.
func gitRepoRoot(cwd string) (root, name string, ok bool) {
	if cwd == "" {
		return "", "", false
	}
	dir := filepath.Clean(cwd)
	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Lstat(gitPath)
		if err == nil {
			if info.IsDir() {
				return dir, filepath.Base(dir), true
			}
			// A .git FILE: resolve the linked worktree to its main root.
			if main, ok := mainWorktreeRoot(gitPath); ok {
				return main, filepath.Base(main), true
			}
			// Unreadable/garbage .git file: treat the holding dir as root.
			return dir, filepath.Base(dir), true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false // reached the filesystem root
		}
		dir = parent
	}
}

// mainWorktreeRoot reads a linked worktree's .git file and resolves it to
// the main repository's working directory. The file's "gitdir:" line points
// at "<main>/.git/worktrees/<name>"; the main root is the directory holding
// that .git. Returns ok=false when the line is absent or not of that shape.
func mainWorktreeRoot(gitFile string) (string, bool) {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return "", false
	}
	var gitDir string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "gitdir:"); ok {
			gitDir = strings.TrimSpace(v)
			break
		}
	}
	if gitDir == "" {
		return "", false
	}
	// gitDir = <main>/.git/worktrees/<name>; locate the ".git" segment and
	// take its parent as the main worktree root.
	marker := string(filepath.Separator) + ".git" + string(filepath.Separator) + "worktrees" + string(filepath.Separator)
	clean := filepath.Clean(gitDir)
	if i := strings.Index(clean, marker); i > 0 {
		return clean[:i], true
	}
	return "", false
}

const repoSchema = `
-- web_thread_repo records the git repo a thread's cwd resolved to, written
-- at create/import. Backend agnostic (no agents join): a cc:<uuid> stores a
-- row exactly like a carlos thread. repo_root is an absolute path, so moving
-- a repo orphans the mapping until the thread is touched again (acceptable).
CREATE TABLE IF NOT EXISTS web_thread_repo (
  thread_id TEXT PRIMARY KEY,
  repo_root TEXT NOT NULL,
  repo_name TEXT NOT NULL
);
`

// SetThreadRepo records (upsert) the repo a thread belongs to. Idempotent.
func (s *GroupStore) SetThreadRepo(ctx context.Context, threadID, root, name string) error {
	if threadID == "" {
		return fmt.Errorf("web: thread id is required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO web_thread_repo(thread_id, repo_root, repo_name) VALUES(?, ?, ?)
		ON CONFLICT(thread_id) DO UPDATE SET repo_root = excluded.repo_root, repo_name = excluded.repo_name`,
		threadID, root, name)
	if err != nil {
		return fmt.Errorf("set thread repo: %w", err)
	}
	return nil
}

// RepoMap returns thread_id -> RepoRef for the roster overlay, mirroring
// MembershipMap. Backend agnostic; rows for vanished threads match nothing.
func (s *GroupStore) RepoMap(ctx context.Context) (map[string]RepoRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT thread_id, repo_root, repo_name FROM web_thread_repo`)
	if err != nil {
		return nil, fmt.Errorf("repo map: %w", err)
	}
	defer rows.Close()
	out := map[string]RepoRef{}
	for rows.Next() {
		var id, root, name string
		if err := rows.Scan(&id, &root, &name); err != nil {
			return nil, err
		}
		out[id] = RepoRef{Root: root, Name: name}
	}
	return out, rows.Err()
}
