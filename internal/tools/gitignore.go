package tools

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Ignorer decides whether a path should be skipped during recursive walks.
// Paths are tested relative to the Ignorer's root.
type Ignorer interface {
	// IsIgnored reports whether the path (relative or absolute) should be
	// skipped. Directories should be passed with no trailing slash; the
	// Ignorer will apply directory-specific patterns appropriately.
	IsIgnored(path string) bool
	// IsDirIgnored is the same as IsIgnored but treats the path as a
	// directory for trailing-slash patterns. Walkers should prefer this
	// for directory entries so they can prune the descent.
	IsDirIgnored(path string) bool
	// Root returns the root the Ignorer was loaded from. All paths are
	// resolved relative to this root for matching.
	Root() string
}

// gitignoreRule is a parsed pattern from a .gitignore file. Rules are stored
// in walk order - the *last* matching rule wins (gitignore semantics).
type gitignoreRule struct {
	// base is the directory the .gitignore lives in, expressed as a
	// path relative to the Ignorer root. Patterns rooted with leading `/`
	// match relative to base; non-rooted patterns can match anywhere
	// under base.
	base string
	// pattern is the cleaned pattern body (no `!` prefix; trailing `/`
	// stripped - see dirOnly).
	pattern string
	// negate flips the match into an "explicitly include" rule.
	negate bool
	// dirOnly means the pattern only matches directories (trailing `/`).
	dirOnly bool
	// rooted means the pattern is anchored to base (leading `/` or the
	// pattern contains a `/` somewhere other than the trailing slash).
	rooted bool
}

// fsIgnorer is the in-memory Ignorer implementation. Rules from nested
// .gitignore files are concatenated in walk order; matching scans rules
// last-to-first to honour gitignore's "last match wins" rule.
type fsIgnorer struct {
	root  string
	rules []gitignoreRule
}

// Supported gitignore syntax subset:
//
//   - blank lines and `#`-prefixed comments are skipped
//   - `*` glob (matches anything except `/`)
//   - `**` glob (matches any number of path segments, including zero)
//   - `?` glob (single non-`/` char)
//   - leading `/` anchors the pattern to the .gitignore's directory
//   - trailing `/` restricts the pattern to directories
//   - `!` prefix negates (re-includes) previously-ignored paths
//   - nested .gitignore files in subdirectories are additive; each rule
//     is interpreted relative to its own .gitignore directory
//   - `.git/` is ALWAYS ignored regardless of any .gitignore content
//
// Deferred (not supported):
//
//   - `[abc]` character classes (rare in practice; can be added later)
//   - `\` escapes (rare; can be added later)
//   - the `core.excludesFile` global gitignore
//   - per-user `~/.config/git/ignore`
//
// The deferral is conservative: a pattern that uses an unsupported feature
// will simply not match, meaning carlos may *over-read* files (less
// dangerous than under-reading and missing context). It will never
// silently match something the user didn't ask to ignore.

// LoadIgnorer walks `root` collecting `.gitignore` files. Each file's
// patterns are appended in BFS order. The returned Ignorer is safe to
// query concurrently (read-only after load).
func LoadIgnorer(root string) (Ignorer, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	ig := &fsIgnorer{root: absRoot}

	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip unreadable entries rather than abort the whole load -
			// a perm-denied subtree should not stop us indexing the
			// rest of the repo.
			if errors.Is(err, fs.ErrPermission) {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			return err
		}
		// Skip walking into .git itself; never load any .gitignore that
		// might be inside (very unusual but possible if someone packs a
		// repo into a fixture).
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() != ".gitignore" {
			return nil
		}
		base, _ := filepath.Rel(absRoot, filepath.Dir(path))
		if base == "." {
			base = ""
		}
		base = filepath.ToSlash(base)
		rules, perr := parseGitignore(path, base)
		if perr != nil {
			return perr
		}
		ig.rules = append(ig.rules, rules...)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return ig, nil
}

// parseGitignore reads `path` and returns the parsed rules tagged with
// `base` (the directory the .gitignore lives in, relative to the Ignorer
// root, slash-form).
func parseGitignore(path, base string) ([]gitignoreRule, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []gitignoreRule
	s := bufio.NewScanner(f)
	// .gitignore files can have long lines (a single pattern is rarely
	// huge but a malformed file shouldn't crash us); 1 MiB is plenty.
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	for s.Scan() {
		line := s.Text()
		// Trim trailing whitespace; leading whitespace is significant
		// only in that gitignore treats spaces as part of the pattern
		// unless escaped - we don't support escapes so we leave leading
		// spaces alone.
		line = strings.TrimRight(line, " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := gitignoreRule{base: base, pattern: line}
		if strings.HasPrefix(r.pattern, "!") {
			r.negate = true
			r.pattern = r.pattern[1:]
		}
		if strings.HasSuffix(r.pattern, "/") {
			r.dirOnly = true
			r.pattern = strings.TrimSuffix(r.pattern, "/")
		}
		if strings.HasPrefix(r.pattern, "/") {
			r.rooted = true
			r.pattern = strings.TrimPrefix(r.pattern, "/")
		} else if strings.Contains(r.pattern, "/") {
			// A pattern containing a mid-string `/` is also anchored
			// to the .gitignore directory per gitignore(5).
			r.rooted = true
		}
		if r.pattern == "" {
			continue
		}
		out = append(out, r)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (ig *fsIgnorer) Root() string { return ig.root }

func (ig *fsIgnorer) IsIgnored(path string) bool {
	return ig.isIgnored(path, false)
}

func (ig *fsIgnorer) IsDirIgnored(path string) bool {
	return ig.isIgnored(path, true)
}

// isIgnored applies the standard gitignore precedence: walk all rules,
// keep the last match (negate or not); a path with no match is included.
// The always-ignored `.git/` shortcut runs first so even a malicious
// `!.git` pattern can't override it.
func (ig *fsIgnorer) isIgnored(path string, isDir bool) bool {
	rel := ig.relpath(path)
	if rel == "" {
		// The root itself is never ignored.
		return false
	}
	// .git is always ignored - no in-repo file can override this.
	if rel == ".git" || strings.HasPrefix(rel, ".git/") {
		return true
	}

	ignored := false
	for _, r := range ig.rules {
		if !ruleApplies(r, rel, isDir) {
			continue
		}
		ignored = !r.negate
	}
	return ignored
}

// relpath converts an absolute or relative path to one relative to the
// Ignorer root, in slash form. Returns "" if the path is the root itself
// or escapes upward.
func (ig *fsIgnorer) relpath(path string) string {
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(ig.root, p)
	}
	rel, err := filepath.Rel(ig.root, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

// ruleApplies tests whether rule r matches path rel (slash form, relative
// to the Ignorer root). Caller passes isDir true when rel is a directory.
func ruleApplies(r gitignoreRule, rel string, isDir bool) bool {
	if r.dirOnly && !isDir {
		// A dir-only rule like `node_modules/` should still ignore the
		// contents of node_modules - handled by callers that descend
		// (walker prunes dirs); for file-level checks we also want
		// "is the file UNDER an ignored dir?" treated as ignored.
		// We approximate that by matching the rule against any prefix
		// of rel that is itself a directory.
		if !pathHasIgnoredDirAncestor(r, rel) {
			return false
		}
		return true
	}

	// Scope rel to the rule's base directory.
	scoped := rel
	if r.base != "" {
		if rel == r.base {
			scoped = ""
		} else if strings.HasPrefix(rel, r.base+"/") {
			scoped = strings.TrimPrefix(rel, r.base+"/")
		} else {
			// Rule lives in a subtree this path isn't in.
			return false
		}
		if scoped == "" {
			return false
		}
	}

	if r.rooted {
		return matchGlob(r.pattern, scoped)
	}
	// Unrooted: pattern can match the basename of any path segment, or
	// any suffix of segments. Try the full scoped path first, then each
	// progressively-deeper basename.
	if matchGlob(r.pattern, scoped) {
		return true
	}
	parts := strings.Split(scoped, "/")
	for i := range parts {
		sub := strings.Join(parts[i:], "/")
		if matchGlob(r.pattern, sub) {
			return true
		}
	}
	return false
}

// pathHasIgnoredDirAncestor returns true if any directory ancestor of rel
// matches r as a directory rule.
func pathHasIgnoredDirAncestor(r gitignoreRule, rel string) bool {
	parts := strings.Split(rel, "/")
	// Iterate all proper prefixes (i.e. ancestors), inclusive of rel
	// itself if it's a directory - the caller handles the !isDir case
	// for the leaf already.
	for i := 1; i < len(parts); i++ {
		anc := strings.Join(parts[:i], "/")
		scoped := anc
		if r.base != "" {
			if anc == r.base {
				continue
			}
			if !strings.HasPrefix(anc, r.base+"/") {
				continue
			}
			scoped = strings.TrimPrefix(anc, r.base+"/")
		}
		if r.rooted {
			if matchGlob(r.pattern, scoped) {
				return true
			}
			continue
		}
		if matchGlob(r.pattern, scoped) {
			return true
		}
		ap := strings.Split(scoped, "/")
		for j := range ap {
			sub := strings.Join(ap[j:], "/")
			if matchGlob(r.pattern, sub) {
				return true
			}
		}
	}
	return false
}

// matchGlob is a small wildcard matcher supporting `*`, `?`, and `**`.
// `*` matches any run of non-`/` chars; `**` matches any number of path
// segments (including zero); `?` matches one non-`/` char. Everything
// else is a literal.
func matchGlob(pattern, name string) bool {
	// Fast path: no wildcards.
	if !strings.ContainsAny(pattern, "*?") {
		return pattern == name
	}
	return globMatch(pattern, name)
}

func globMatch(pattern, name string) bool {
	for {
		if pattern == "" {
			return name == ""
		}
		// Handle `**` first - it may consume any number of segments.
		if strings.HasPrefix(pattern, "**") {
			rest := strings.TrimPrefix(pattern, "**")
			rest = strings.TrimPrefix(rest, "/")
			if rest == "" {
				// trailing `**` matches anything (including empty).
				return true
			}
			// Try matching `rest` against name and every suffix of name
			// that begins after a `/`.
			if globMatch(rest, name) {
				return true
			}
			for i := 0; i < len(name); i++ {
				if name[i] == '/' && globMatch(rest, name[i+1:]) {
					return true
				}
			}
			return false
		}
		c := pattern[0]
		switch c {
		case '*':
			rest := pattern[1:]
			// `*` matches any non-`/` run. Try every length.
			if globMatch(rest, name) {
				return true
			}
			for i := 0; i < len(name); i++ {
				if name[i] == '/' {
					break
				}
				if globMatch(rest, name[i+1:]) {
					return true
				}
			}
			return false
		case '?':
			if name == "" || name[0] == '/' {
				return false
			}
			pattern = pattern[1:]
			name = name[1:]
		default:
			if name == "" || name[0] != c {
				return false
			}
			pattern = pattern[1:]
			name = name[1:]
		}
	}
}

// WalkRespectingGitignore is filepath.WalkDir but skips entries the
// Ignorer rejects. Directories that are ignored have their entire subtree
// pruned (returning fs.SkipDir). The walk also unconditionally skips
// `.git/` regardless of Ignorer state - defensive against a missing or
// empty Ignorer.
func WalkRespectingGitignore(root string, ig Ignorer, fn filepath.WalkFunc) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	return filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fn(path, nil, err)
		}
		// Always-skip .git dir wholesale.
		if d.IsDir() && d.Name() == ".git" && path != absRoot {
			return fs.SkipDir
		}
		if ig != nil && path != absRoot {
			if d.IsDir() {
				if ig.IsDirIgnored(path) {
					return fs.SkipDir
				}
			} else {
				if ig.IsIgnored(path) {
					return nil
				}
			}
		}
		// Adapt fs.DirEntry → os.FileInfo for the WalkFunc signature.
		info, infoErr := d.Info()
		if infoErr != nil {
			return fn(path, nil, infoErr)
		}
		return fn(path, info, nil)
	})
}
