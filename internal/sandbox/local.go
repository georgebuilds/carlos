package sandbox

import (
	"context"
	"io"
)

// Local runs commands in the user's actual working directory — whatever
// the carlos process was launched with. No isolation, no setup cost. Used
// for read-only sub-agents (search, inspect, summarize) where the
// worktree machinery would just be friction.
//
// Local has no per-instance state today; the zero value is usable. The
// struct exists (vs a free function) so it satisfies the [Backend]
// interface and to leave room for future per-instance config (e.g., a
// chroot, a different cwd, env overrides) without breaking the surface.
type Local struct {
	// Dir, if non-empty, overrides the inherited working directory. Used
	// by tests; production callers leave it empty so the child inherits
	// the carlos process's cwd.
	Dir string
}

// Name returns "local".
func (*Local) Name() string { return "local" }

// Exec runs cmd in l.Dir (or the inherited cwd if empty). Combined cap
// at 8 KiB per stream; ctx-cancel kills the process tree.
func (l *Local) Exec(ctx context.Context, cmd []string, stdin io.Reader) ([]byte, []byte, int, error) {
	return runCommand(ctx, l.Dir, cmd, stdin)
}

// Close is a no-op for Local — there's nothing to release. Always
// returns nil so the caller's defer is harmless.
func (*Local) Close() error { return nil }

// Compile-time check.
var _ Backend = (*Local)(nil)
