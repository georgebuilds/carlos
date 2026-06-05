package tools

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeFile is a tiny helper: ensures the parent dir exists, writes the
// file, t.Fatals on any I/O error. Keeps the test bodies focused on
// behaviour instead of error plumbing.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitignore_SimplePatterns(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\nbuild/\n")
	writeFile(t, filepath.Join(root, "keep.go"), "")
	writeFile(t, filepath.Join(root, "drop.log"), "")
	writeFile(t, filepath.Join(root, "build", "out.bin"), "")

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	if ig.IsIgnored(filepath.Join(root, "keep.go")) {
		t.Error("keep.go should not be ignored")
	}
	if !ig.IsIgnored(filepath.Join(root, "drop.log")) {
		t.Error("drop.log should be ignored")
	}
	if !ig.IsDirIgnored(filepath.Join(root, "build")) {
		t.Error("build/ should be ignored as a directory")
	}
	if !ig.IsIgnored(filepath.Join(root, "build", "out.bin")) {
		t.Error("file under ignored dir should be ignored")
	}
}

func TestGitignore_Negation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "*.tmp\n!keep.tmp\n")
	writeFile(t, filepath.Join(root, "scratch.tmp"), "")
	writeFile(t, filepath.Join(root, "keep.tmp"), "")
	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.IsIgnored(filepath.Join(root, "scratch.tmp")) {
		t.Error("scratch.tmp should be ignored")
	}
	if ig.IsIgnored(filepath.Join(root, "keep.tmp")) {
		t.Error("keep.tmp should be re-included by negation rule")
	}
}

func TestGitignore_NestedOverrides(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "*.txt\n")
	writeFile(t, filepath.Join(root, "sub", ".gitignore"), "!important.txt\n")
	writeFile(t, filepath.Join(root, "sub", "important.txt"), "")
	writeFile(t, filepath.Join(root, "sub", "other.txt"), "")
	writeFile(t, filepath.Join(root, "top.txt"), "")

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.IsIgnored(filepath.Join(root, "top.txt")) {
		t.Error("top.txt should be ignored by root gitignore")
	}
	if !ig.IsIgnored(filepath.Join(root, "sub", "other.txt")) {
		t.Error("sub/other.txt should still be ignored")
	}
	if ig.IsIgnored(filepath.Join(root, "sub", "important.txt")) {
		t.Error("sub/important.txt should be re-included by nested .gitignore")
	}
}

func TestGitignore_RootedPattern(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "/topfile\n")
	writeFile(t, filepath.Join(root, "topfile"), "")
	writeFile(t, filepath.Join(root, "sub", "topfile"), "")

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.IsIgnored(filepath.Join(root, "topfile")) {
		t.Error("rooted /topfile should match top-level")
	}
	if ig.IsIgnored(filepath.Join(root, "sub", "topfile")) {
		t.Error("rooted /topfile must NOT match sub/topfile")
	}
}

func TestGitignore_DoubleStar(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "**/node_modules/\n")
	writeFile(t, filepath.Join(root, "node_modules", "x.js"), "")
	writeFile(t, filepath.Join(root, "pkg", "deep", "node_modules", "y.js"), "")
	writeFile(t, filepath.Join(root, "pkg", "src.js"), "")

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.IsIgnored(filepath.Join(root, "node_modules", "x.js")) {
		t.Error("top node_modules should be ignored")
	}
	if !ig.IsIgnored(filepath.Join(root, "pkg", "deep", "node_modules", "y.js")) {
		t.Error("nested node_modules should be ignored")
	}
	if ig.IsIgnored(filepath.Join(root, "pkg", "src.js")) {
		t.Error("pkg/src.js should NOT be ignored")
	}
}

func TestGitignore_DotGitAlwaysIgnored(t *testing.T) {
	root := t.TempDir()
	// Even with a deliberately permissive .gitignore, .git/ must stay ignored.
	writeFile(t, filepath.Join(root, ".gitignore"), "!.git\n!.git/**\n")
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, "src.go"), "")

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.IsDirIgnored(filepath.Join(root, ".git")) {
		t.Error(".git/ must always be ignored")
	}
	if !ig.IsIgnored(filepath.Join(root, ".git", "HEAD")) {
		t.Error(".git/HEAD must always be ignored")
	}
	if ig.IsIgnored(filepath.Join(root, "src.go")) {
		t.Error("src.go should not be ignored")
	}
}

func TestGitignore_CommentsAndBlanks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "# a comment\n\n   \n*.bak\n")
	writeFile(t, filepath.Join(root, "x.bak"), "")
	writeFile(t, filepath.Join(root, "y.go"), "")

	ig, err := LoadIgnorer(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.IsIgnored(filepath.Join(root, "x.bak")) {
		t.Error("x.bak should be ignored despite comments/blanks above the rule")
	}
	if ig.IsIgnored(filepath.Join(root, "y.go")) {
		t.Error("y.go should not be ignored")
	}
}

func TestWalkRespectingGitignore_PrunesIgnored(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "ignored/\n*.log\n")
	writeFile(t, filepath.Join(root, "keep.go"), "")
	writeFile(t, filepath.Join(root, "skip.log"), "")
	writeFile(t, filepath.Join(root, "ignored", "deep", "file.go"), "")
	writeFile(t, filepath.Join(root, "src", "main.go"), "")
	// Simulate a .git dir to confirm wholesale skip.
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "x")

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
	want := []string{".gitignore", "keep.go", "src/main.go"}
	if !equalStringSlices(visited, want) {
		t.Errorf("walk visited %v, want %v", visited, want)
	}
}

func TestWalkRespectingGitignore_NilIgnorerStillSkipsGit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "")
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "x")
	var seen []string
	err := WalkRespectingGitignore(root, nil, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		seen = append(seen, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range seen {
		if s == ".git/HEAD" {
			t.Error(".git/ must be skipped even with nil Ignorer")
		}
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
