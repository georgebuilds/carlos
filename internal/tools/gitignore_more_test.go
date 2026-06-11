package tools

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestMatchGlob_Table exercises the glob matcher's branches directly:
// literal, `*`, `?`, `**` (leading/middle/trailing), and the no-match
// fall-through returns.
func TestMatchGlob_Table(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"abc", "abc", true},
		{"abc", "abd", false}, // literal mismatch
		{"a*c", "abc", true},
		{"a*c", "ac", true},
		{"a*c", "abxc", true},
		{"a*c", "ab/c", false}, // * does not cross /
		{"a*", "abc", true},
		{"a*", "a", true},
		{"a*", "b", false}, // first char mismatch
		{"a?c", "abc", true},
		{"a?c", "ac", false},  // ? needs exactly one
		{"a?c", "a/c", false}, // ? does not match /
		{"**", "a/b/c", true}, // trailing ** matches anything
		{"**/x", "x", true},   // ** matches zero segments
		{"**/x", "a/b/x", true},
		{"**/x", "a/b/y", false}, // ** with no matching suffix
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x/y/c", false},
		{"*.go", "main.go", true},
		{"*.go", "main.rs", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pat, c.name); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pat, c.name, got, c.want)
		}
	}
}

// TestGitignore_BarePatternsSkipped — a line that reduces to empty after
// stripping `!` / trailing `/` is skipped without crashing.
func TestGitignore_BarePatternsSkipped(t *testing.T) {
	root := t.TempDir()
	// "!/" -> negate + rooted strip -> empty pattern -> skipped.
	// "/"  -> rooted strip -> empty pattern -> skipped.
	writeFile(t, filepath.Join(root, ".gitignore"), "!/\n/\n*.keep\n")
	writeFile(t, filepath.Join(root, "x.keep"), "")
	writeFile(t, filepath.Join(root, "y.txt"), "")

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.IsIgnored(filepath.Join(root, "x.keep")) {
		t.Error("the valid *.keep rule should still apply after skipped bare lines")
	}
	if ig.IsIgnored(filepath.Join(root, "y.txt")) {
		t.Error("y.txt should not be ignored")
	}
}

// TestGitignore_QuestionMarkGlob — `?` matches exactly one non-slash char.
func TestGitignore_QuestionMarkGlob(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "file?.txt\n")
	writeFile(t, filepath.Join(root, "file1.txt"), "")
	writeFile(t, filepath.Join(root, "file12.txt"), "")
	writeFile(t, filepath.Join(root, "file.txt"), "")

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.IsIgnored(filepath.Join(root, "file1.txt")) {
		t.Error("file1.txt should match file?.txt")
	}
	if ig.IsIgnored(filepath.Join(root, "file12.txt")) {
		t.Error("file12.txt has two chars, must NOT match file?.txt")
	}
	if ig.IsIgnored(filepath.Join(root, "file.txt")) {
		t.Error("file.txt has zero chars, must NOT match file?.txt")
	}
}

// TestGitignore_MidStringSlashIsRooted — a pattern containing a mid-string
// slash (no leading slash) is anchored to the .gitignore directory per
// gitignore(5): "doc/frotz" matches doc/frotz but not a/doc/frotz.
func TestGitignore_MidStringSlashIsRooted(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "doc/frotz\n")
	writeFile(t, filepath.Join(root, "doc", "frotz"), "")
	writeFile(t, filepath.Join(root, "a", "doc", "frotz"), "")

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.IsIgnored(filepath.Join(root, "doc", "frotz")) {
		t.Error("doc/frotz should be ignored")
	}
	if ig.IsIgnored(filepath.Join(root, "a", "doc", "frotz")) {
		t.Error("a/doc/frotz must NOT match the rooted pattern doc/frotz")
	}
}

// TestGitignore_DirOnlyMatchesNestedFileUnderBaseGitignore — a dir-only
// rule declared in a nested .gitignore should ignore files under the
// matched directory, exercising the pathHasIgnoredDirAncestor base-scoping
// branch.
func TestGitignore_DirOnlyFromNestedGitignore(t *testing.T) {
	root := t.TempDir()
	// Nested .gitignore in sub/ with a dir-only rule.
	writeFile(t, filepath.Join(root, "sub", ".gitignore"), "cache/\n")
	writeFile(t, filepath.Join(root, "sub", "cache", "data.bin"), "")
	writeFile(t, filepath.Join(root, "sub", "keep.txt"), "")
	// A same-named dir OUTSIDE the nested gitignore's base must NOT be hit.
	writeFile(t, filepath.Join(root, "cache", "top.bin"), "")

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.IsDirIgnored(filepath.Join(root, "sub", "cache")) {
		t.Error("sub/cache should be dir-ignored by nested rule")
	}
	if !ig.IsIgnored(filepath.Join(root, "sub", "cache", "data.bin")) {
		t.Error("file under sub/cache should be ignored")
	}
	if ig.IsIgnored(filepath.Join(root, "sub", "keep.txt")) {
		t.Error("sub/keep.txt should not be ignored")
	}
	if ig.IsIgnored(filepath.Join(root, "cache", "top.bin")) {
		t.Error("top-level cache/ must NOT be ignored by a rule scoped to sub/")
	}
}

// TestGitignore_RootedPatternUnderBase — a rooted pattern in a nested
// .gitignore anchors to that nested directory, exercising the rooted
// branch inside the base-scoping logic.
func TestGitignore_RootedPatternUnderBase(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pkg", ".gitignore"), "/build\n")
	writeFile(t, filepath.Join(root, "pkg", "build"), "")
	writeFile(t, filepath.Join(root, "pkg", "deep", "build"), "")

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.IsIgnored(filepath.Join(root, "pkg", "build")) {
		t.Error("pkg/build should match rooted /build in pkg/.gitignore")
	}
	if ig.IsIgnored(filepath.Join(root, "pkg", "deep", "build")) {
		t.Error("pkg/deep/build must NOT match the rooted /build")
	}
}

// TestGitignore_RelpathEscapeUpwardNotIgnored — querying a path that
// escapes above the root resolves to "" (root) and is never ignored.
func TestGitignore_RelpathEscapeUpward(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	writeFile(t, filepath.Join(root, ".gitignore"), "*\n")
	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	// A sibling path outside root: "../other.txt" relative to root.
	outside := filepath.Join(filepath.Dir(root), "other.txt")
	if ig.IsIgnored(outside) {
		t.Error("a path outside the ignorer root must not be reported ignored")
	}
	// The root itself is never ignored.
	if ig.IsIgnored(root) {
		t.Error("the root itself must never be ignored")
	}
}

// TestGitignore_NegatedDirOnlyAncestor — a dir-only ignore plus a negation
// re-includes a specific file beneath it. Confirms last-match-wins through
// the ancestor path.
func TestGitignore_NegateInsideIgnoredDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "logs/\n!logs/keep.log\n")
	writeFile(t, filepath.Join(root, "logs", "drop.log"), "")
	writeFile(t, filepath.Join(root, "logs", "keep.log"), "")

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.IsIgnored(filepath.Join(root, "logs", "drop.log")) {
		t.Error("logs/drop.log should be ignored")
	}
	if ig.IsIgnored(filepath.Join(root, "logs", "keep.log")) {
		t.Error("logs/keep.log should be re-included by the negation")
	}
}

// TestGitignore_DoubleStarMiddle — `a/**/b` matches across any number of
// intermediate segments including zero.
func TestGitignore_DoubleStarMiddle(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "a/**/b\n")
	writeFile(t, filepath.Join(root, "a", "b"), "")
	writeFile(t, filepath.Join(root, "a", "x", "y", "b"), "")
	writeFile(t, filepath.Join(root, "a", "c"), "")

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.IsIgnored(filepath.Join(root, "a", "b")) {
		t.Error("a/b should match a/**/b (zero intermediate segments)")
	}
	if !ig.IsIgnored(filepath.Join(root, "a", "x", "y", "b")) {
		t.Error("a/x/y/b should match a/**/b")
	}
	if ig.IsIgnored(filepath.Join(root, "a", "c")) {
		t.Error("a/c should not match a/**/b")
	}
}

// TestLoadIgnorer_RelativeRootArgument — LoadIgnorer accepts a relative
// root and resolves it to absolute; queries by relative path then work.
func TestLoadIgnorer_RelativeRootArgument(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(root, "x.log"), "")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	ig, err := LoadIgnorer(".")
	if err != nil {
		t.Fatal(err)
	}
	// Query by a path relative to the (now absolute) root.
	if !ig.IsIgnored("x.log") {
		t.Error("relative-path query against relative-loaded root should work")
	}
	if ig.Root() == "" || !filepath.IsAbs(ig.Root()) {
		t.Errorf("Root() should be absolute; got %q", ig.Root())
	}
}

// TestLoadIgnorer_SkipsPermDeniedSubtree — an unreadable directory in the
// tree is skipped rather than aborting the whole load. The rest of the
// repo's .gitignore rules still apply.
func TestLoadIgnorer_SkipsPermDeniedSubtree(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission bits")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(root, "x.log"), "")
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(locked, "inner.txt"), "")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatalf("LoadIgnorer should skip perm-denied subtree, not fail: %v", err)
	}
	if !ig.IsIgnored(filepath.Join(root, "x.log")) {
		t.Error("root-level rule should still apply after skipping locked dir")
	}
}

// TestLoadIgnorer_BadRoot — a root that cannot be walked surfaces an
// error instead of panicking.
func TestLoadIgnorer_BadRoot(t *testing.T) {
	// A path under a regular file is not a directory; WalkDir errors.
	root := t.TempDir()
	notADir := filepath.Join(root, "afile")
	writeFile(t, notADir, "x")
	if _, err := LoadIgnorer(filepath.Join(notADir, "subpath")); err == nil {
		t.Error("expected error loading from a non-directory path")
	}
}

// TestWalkRespectingGitignore_PropagatesWalkError — when the walk hits an
// entry error the callback receives it (here by passing a nonexistent
// root, which makes WalkDir invoke fn with the error).
func TestWalkRespectingGitignore_PropagatesWalkError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	var sawErr bool
	err := WalkRespectingGitignore(missing, nil, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			sawErr = true
		}
		return e
	})
	if err == nil {
		t.Error("expected an error walking a missing root")
	}
	if !sawErr {
		t.Error("callback should have observed the walk error")
	}
}

// TestWalkRespectingGitignore_VisitsExpectedWithNestedRules — end-to-end
// walk with a nested .gitignore re-including a file, asserting the exact
// visited set.
func TestWalkRespectingGitignore_NestedReinclude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "*.tmp\n")
	writeFile(t, filepath.Join(root, "sub", ".gitignore"), "!keep.tmp\n")
	writeFile(t, filepath.Join(root, "a.tmp"), "")
	writeFile(t, filepath.Join(root, "sub", "keep.tmp"), "")
	writeFile(t, filepath.Join(root, "sub", "drop.tmp"), "")
	writeFile(t, filepath.Join(root, "main.go"), "")

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	var visited []string
	err = WalkRespectingGitignore(root, ig, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		visited = append(visited, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(visited)
	want := []string{".gitignore", "main.go", "sub/.gitignore", "sub/keep.tmp"}
	if !equalStringSlices(visited, want) {
		t.Errorf("visited %v, want %v", visited, want)
	}
}
