package frame

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Paths is the per-frame directory layout under ~/.carlos/frames/<name>/.
// Phase F-17. Callers construct one via PathsFor and use the field they
// need; the directories are created lazily by the writer that owns them
// (research engine MkdirAll's ResearchDir on first save, usershell
// MkdirAll's JobsDir on first job, etc.).
//
// DigestDir is reserved for the daemon daily-digest feature that lands
// post-v1; included here so the layout is stable.
type Paths struct {
	Root         string
	ResearchDir  string
	JobsDir      string
	WorktreesDir string
	DigestDir    string
}

// PathsFor builds the per-frame layout for a single frame. Returns the
// zero value when home or frameName is empty so callers can compare
// against `Paths{}` rather than threading an error envelope.
func PathsFor(home, frameName string) Paths {
	if home == "" || frameName == "" {
		return Paths{}
	}
	root := filepath.Join(home, ".carlos", "frames", frameName)
	return Paths{
		Root:         root,
		ResearchDir:  filepath.Join(root, "research"),
		JobsDir:      filepath.Join(root, "usershell"),
		WorktreesDir: filepath.Join(root, "worktrees"),
		DigestDir:    filepath.Join(root, "digest"),
	}
}

// MigrationReport records what Migrate did. Each *Moved counter is the
// number of regular files moved from the legacy path into the personal
// frame's subtree; Skipped is files that already existed at the
// destination (idempotent re-run case). Errors are best-effort: one
// failed move does not abort the rest, but every failure is appended
// so the caller can surface them.
type MigrationReport struct {
	ResearchMoved   int
	JobsMoved       int
	WorktreesMoved  int
	ResearchSkipped int
	JobsSkipped     int
	WorktreesSkipped int
	Errors          []error
}

// HasMovement reports whether the migration actually did anything. Used
// by callers to suppress noise when the legacy layout is already gone.
func (r MigrationReport) HasMovement() bool {
	return r.ResearchMoved+r.JobsMoved+r.WorktreesMoved > 0
}

// Migrate is the one-shot move of legacy ~/.carlos/{research,usershell,
// worktrees}/ contents into ~/.carlos/frames/<personalFrameName>/.
// Idempotent: a second call with the legacy dirs already empty is a
// no-op. Files that already exist at the destination are skipped (the
// frame-scoped copy wins, since the next session has likely already
// written there).
//
// Migration is best-effort. A file we can't move is reported in
// Errors; the rest of the dir still gets processed. The legacy
// directory is only removed when fully drained AND empty.
func Migrate(home, personalFrameName string) (MigrationReport, error) {
	if home == "" {
		return MigrationReport{}, errors.New("frame: Migrate needs a home dir")
	}
	if personalFrameName == "" {
		personalFrameName = DefaultPersonalName
	}
	target := PathsFor(home, personalFrameName)
	if target.Root == "" {
		return MigrationReport{}, errors.New("frame: Migrate could not compute target paths")
	}

	report := MigrationReport{}
	legacy := map[string]string{
		filepath.Join(home, ".carlos", "research"):  target.ResearchDir,
		filepath.Join(home, ".carlos", "usershell"): target.JobsDir,
		filepath.Join(home, ".carlos", "worktrees"): target.WorktreesDir,
	}
	for src, dst := range legacy {
		moved, skipped, err := migrateDir(src, dst)
		switch src {
		case filepath.Join(home, ".carlos", "research"):
			report.ResearchMoved += moved
			report.ResearchSkipped += skipped
		case filepath.Join(home, ".carlos", "usershell"):
			report.JobsMoved += moved
			report.JobsSkipped += skipped
		case filepath.Join(home, ".carlos", "worktrees"):
			report.WorktreesMoved += moved
			report.WorktreesSkipped += skipped
		}
		if err != nil {
			report.Errors = append(report.Errors, err)
		}
	}
	return report, nil
}

// migrateDir moves every regular file (and one-level subdirectory) from
// src into dst. Returns (moved, skipped, err). Returns (0, 0, nil) when
// src is missing — the legacy layout simply didn't exist on this
// machine. Empty src after the walk is removed.
func migrateDir(src, dst string) (moved, skipped int, firstErr error) {
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("frame: stat %s: %w", src, err)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return 0, 0, fmt.Errorf("frame: mkdir %s: %w", dst, err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, 0, fmt.Errorf("frame: readdir %s: %w", src, err)
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if _, err := os.Lstat(dstPath); err == nil {
			skipped++
			continue
		}
		if err := os.Rename(srcPath, dstPath); err != nil {
			// Cross-device fallback (rename across mount points fails
			// with EXDEV). Fall back to copy+delete for regular files;
			// directories that hit EXDEV are surfaced as errors since a
			// recursive copy is heavier than this slice wants to own.
			if e.IsDir() {
				if cerr := firstError(firstErr, fmt.Errorf("frame: rename %s -> %s: %w", srcPath, dstPath, err)); cerr != nil {
					firstErr = cerr
				}
				continue
			}
			if cerr := copyFile(srcPath, dstPath); cerr != nil {
				if ferr := firstError(firstErr, fmt.Errorf("frame: copy %s -> %s: %w", srcPath, dstPath, cerr)); ferr != nil {
					firstErr = ferr
				}
				continue
			}
			_ = os.Remove(srcPath)
		}
		moved++
	}
	// If the directory is now empty, drain it so a re-run is a clean no-op.
	if remaining, err := os.ReadDir(src); err == nil && len(remaining) == 0 {
		_ = os.Remove(src)
	}
	return moved, skipped, firstErr
}

// copyFile is a small EXDEV fallback for the single-file regular case.
// Not used for directories — the migration is point-in-time and the
// expected legacy paths are flat (research/<files>, usershell/<files>).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// firstError returns prior when non-nil, else next. Tiny helper used by
// migrateDir's "keep going on error" loop.
func firstError(prior, next error) error {
	if prior != nil {
		return prior
	}
	return next
}
