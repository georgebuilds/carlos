// Package projectctx discovers and loads per-project agent context files
// (AGENTS.md / CLAUDE.md and their `.agents/` `.claude/` namespaced variants)
// so the model sees a project's house rules without the user having to
// copy-paste them every session.
//
// # Why both AGENTS.md and CLAUDE.md?
//
// AGENTS.md is the open standard (agents.io) - provider-neutral, the one
// the user wants every tool to adopt. CLAUDE.md is Claude Code's
// convention; we load it too so projects that only ship CLAUDE.md still
// work, but AGENTS.md is the preferred name.
//
// # Walk semantics
//
// Discover walks UP from cwd until it hits either:
//   - the enclosing git root (a directory containing a `.git` entry), or
//   - $HOME (so we never escape into a sibling project the user owns),
//
// whichever is shallower. At each level it looks for, in priority order
// per-name (first found wins per name, but ALL four names are loaded):
//
//   - AGENTS.md            - open standard
//   - CLAUDE.md            - Claude Code convention
//   - .claude/CLAUDE.md    - Claude Code with .claude/ namespace
//   - .agents/AGENTS.md    - agents.io with .agents/ namespace
//
// Returned files are ordered Level=0 first (cwd) → deeper Levels (further
// from cwd). Load reverses this so that in the final concatenated output
// SHALLOWER levels appear first and DEEPER levels appear last - this way
// the closest-to-cwd conventions extend / override the parent-project
// ones in the model's reading order.
//
// # No execution
//
// Loaded files are read as plain text. `@<path>`-style includes are
// resolved recursively (see include.go) but nothing in the files is ever
// executed; this is a pure read+concat pipeline.
package projectctx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxTotalBytes caps the total bytes Load will assemble. Larger projects
// truncate with Context.Truncated=true. 256 KiB is generous for the kind
// of free-form prose AGENTS.md typically holds while still bounding the
// system-prompt token cost.
const MaxTotalBytes int64 = 256 * 1024

// MaxIncludeDepth caps recursive @-include resolution. 4 levels is more
// than enough for realistic chains (AGENTS.md → @global → @subdoc →
// @leaf) without blowing the stack on a cycle that slipped past the
// seenPaths guard.
const MaxIncludeDepth = 4

// candidateNames is the per-level filename list, in priority order. All
// four are loaded if present (priority only affects ordering inside a
// single level).
var candidateNames = []string{
	"AGENTS.md",
	"CLAUDE.md",
	filepath.Join(".claude", "CLAUDE.md"),
	filepath.Join(".agents", "AGENTS.md"),
}

// DiscoveredFile is one file located by the upward walker.
//
// Path is absolute; RelPath is relative to the walk-start dir (cwd) so
// the provenance header reads nicely. Level=0 means the file sits in
// cwd; each step toward the git root / $HOME increments Level by 1.
// Source is the bare candidate name (e.g. "AGENTS.md" or
// ".claude/CLAUDE.md") and is set verbatim from candidateNames.
type DiscoveredFile struct {
	Path      string
	RelPath   string
	Level     int
	Source    string
	SizeBytes int64
}

// LoadedFile pairs the discovery metadata with the file's expanded
// content (after @-include resolution).
//
// Partial=true means at least one @-include in this file (or one of its
// transitive includes) failed to resolve; the inline content includes
// `[project context: include ...]` stubs marking the failure and a
// trailing `[project context: include expand failed - ...]` summary
// stub. Callers that want to surface the degraded state in UI can key
// off this flag without reparsing Content.
type LoadedFile struct {
	DiscoveredFile
	Content string
	Partial bool
}

// Context is the assembled project-context bundle ready to prepend to
// the agent's system prompt.
//
// Combined is the empty string when no files were discovered, so callers
// can treat the loader as a no-op (`if ctx.Combined != "" { ... }`) on
// projects without any AGENTS.md / CLAUDE.md.
//
// Truncated=true means we hit MaxTotalBytes mid-assembly and dropped
// (or partially included) the remaining files. The Files slice still
// reflects what was discovered, but Combined may omit content past the
// cap.
type Context struct {
	Files      []LoadedFile
	Combined   string
	TotalBytes int64
	Truncated  bool

	// Warnings carries non-fatal load warnings (e.g. include-expansion
	// failures) for the caller to surface inside the UI. This package
	// must NOT write these to stderr - emitting them during context
	// loading can interleave with the TUI's first frame paint. The
	// caller owns when and how to display them.
	Warnings []string
}

// Discover walks from cwd upward and returns every project-context file
// found. Stops at the enclosing git root (any dir containing a `.git`
// entry) or at $HOME, whichever it reaches first.
//
// The returned slice is ordered Level=0 first (cwd), then Level=1
// (parent), and so on. Within a level, candidateNames priority order is
// preserved. Callers that want "parent first" output can sort by
// descending Level.
//
// Errors only on a fatal IO failure (e.g. cwd does not exist). A missing
// file at any candidate path is silently skipped; that's the common case.
func Discover(cwd string) ([]DiscoveredFile, error) {
	if cwd == "" {
		return nil, errors.New("projectctx: Discover called with empty cwd")
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("projectctx: abs(%s): %w", cwd, err)
	}
	if info, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("projectctx: stat cwd %s: %w", abs, err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("projectctx: cwd %s is not a directory", abs)
	}

	home, _ := os.UserHomeDir() // empty is fine - we just won't stop at $HOME

	var out []DiscoveredFile
	cur := abs
	level := 0
	for {
		// Hidden-directory skip: never look INSIDE .git or similar dot-dirs.
		// We're walking UP, so the only way a dot-dir ends up "cur" is if
		// the user literally cd'd into one - in which case we still respect
		// it as the walk start. We DO refuse to recurse into a .git as a
		// candidate location for AGENTS.md (the candidate paths above are
		// just .agents/ and .claude/, both intentional).
		for _, name := range candidateNames {
			full := filepath.Join(cur, name)
			info, err := os.Stat(full)
			if err != nil || info.IsDir() {
				continue
			}
			rel, relErr := filepath.Rel(abs, full)
			if relErr != nil {
				rel = full
			}
			out = append(out, DiscoveredFile{
				Path:      full,
				RelPath:   rel,
				Level:     level,
				Source:    name,
				SizeBytes: info.Size(),
			})
		}

		// Stop conditions, checked AFTER scanning this level so a file at
		// the git root itself is still picked up.
		if isGitRoot(cur) {
			break
		}
		if home != "" && cur == home {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Filesystem root.
			break
		}
		cur = parent
		level++
	}

	return out, nil
}

// isGitRoot returns true iff dir contains a `.git` entry (file OR dir -
// `.git` is a file inside git worktrees, pointing at the real gitdir).
func isGitRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// Load reads every discovered file, resolves @-includes (capped at
// MaxIncludeDepth), and returns the assembled Context.
//
// Output ordering: shallowest level first, deepest level last. Inside a
// level, candidateNames priority order is preserved (so AGENTS.md
// appears before CLAUDE.md when both are present at the same depth).
// This puts the most-specific (closest-to-cwd) conventions LAST in the
// model's reading order, where they can extend / override the
// parent-project conventions.
//
// Errors only on fatal IO. A failed @-include is replaced inline with a
// "[project context: include failed - <path>: <err>]" stub so a single
// missing file doesn't abort the whole load; the enclosing file is
// also marked LoadedFile.Partial=true, gets a trailing
// "[project context: include expand failed - ...]" summary stub, and
// the underlying warnings are collected on Context.Warnings for the
// caller to surface (never written to stderr by this package).
func Load(files []DiscoveredFile) (*Context, error) {
	// Sort for output ordering: shallowest level FIRST (parent context
	// comes before child context, so closest-to-cwd conventions land
	// LAST in the model's reading order and can override the broader
	// rules from further up the tree). Inside a level, candidateNames
	// priority order is preserved so AGENTS.md appears before CLAUDE.md
	// when both exist at the same depth.
	sorted := make([]DiscoveredFile, len(files))
	copy(sorted, files)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Level != sorted[j].Level {
			return sorted[i].Level > sorted[j].Level
		}
		return candidateNamePriority(sorted[i].Source) < candidateNamePriority(sorted[j].Source)
	})

	ctx := &Context{}
	var b strings.Builder

	for _, df := range sorted {
		if ctx.TotalBytes >= MaxTotalBytes {
			ctx.Truncated = true
			break
		}
		raw, err := os.ReadFile(df.Path)
		if err != nil {
			// A file vanishing between Discover and Load is rare but
			// shouldn't kill the whole load - emit a stub and continue.
			stub := fmt.Sprintf("# [project context: %s]\n\n[project context: read failed - %s: %v]\n\n", df.RelPath, df.RelPath, err)
			b.WriteString(stub)
			ctx.TotalBytes += int64(len(stub))
			continue
		}

		seen := map[string]bool{df.Path: true}
		expanded, warnings := expandIncludes(string(raw), filepath.Dir(df.Path), 0, MaxIncludeDepth, seen)

		// Surface include-expansion failures so the model sees a clearly-
		// marked stub instead of silent partial content. Each warning is
		// already mirrored as an inline `[project context: include ...]`
		// stub by expandIncludes (read failures, depth caps, cycles); we
		// add a trailing summary stub here so the failure is unmissable
		// in the model's reading order, and collect the warnings on the
		// returned Context so the caller can surface them in the UI
		// (never written to stderr - see Context.Warnings).
		partial := len(warnings) > 0
		var failureStub string
		if partial {
			for _, w := range warnings {
				ctx.Warnings = append(ctx.Warnings, fmt.Sprintf("projectctx: include expand %s: %s", df.RelPath, w))
			}
			// Join warnings into a single line so the trailing stub is one
			// compact marker rather than a multi-line dump.
			failureStub = fmt.Sprintf("[project context: include expand failed - %s: %s]\n", df.RelPath, strings.Join(warnings, "; "))
		}

		header := fmt.Sprintf("# [project context: %s]\n\n", df.RelPath)
		section := header + expanded
		if !strings.HasSuffix(section, "\n") {
			section += "\n"
		}
		if failureStub != "" {
			section += failureStub
		}
		section += "\n"

		// Per-file truncation: if appending would overshoot, write what
		// fits and mark truncated.
		remaining := MaxTotalBytes - ctx.TotalBytes
		if int64(len(section)) > remaining {
			b.WriteString(section[:remaining])
			ctx.TotalBytes += remaining
			ctx.Truncated = true
			ctx.Files = append(ctx.Files, LoadedFile{
				DiscoveredFile: df,
				Content:        expanded,
				Partial:        partial,
			})
			break
		}

		b.WriteString(section)
		ctx.TotalBytes += int64(len(section))
		ctx.Files = append(ctx.Files, LoadedFile{
			DiscoveredFile: df,
			Content:        expanded,
			Partial:        partial,
		})
	}

	ctx.Combined = b.String()
	return ctx, nil
}

// LoadFromCwd is the convenience entry point: Discover(cwd) → Load.
// Returns an empty (but non-nil) Context if no project-context files
// exist, so callers can unconditionally check ctx.Combined != "".
func LoadFromCwd(cwd string) (*Context, error) {
	files, err := Discover(cwd)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return &Context{}, nil
	}
	return Load(files)
}

// candidateNamePriority returns the position of name in candidateNames
// (lower = higher priority). Unknown names sort last so a future caller
// passing a hand-built DiscoveredFile doesn't break the sort.
func candidateNamePriority(name string) int {
	for i, n := range candidateNames {
		if n == name {
			return i
		}
	}
	return len(candidateNames)
}
