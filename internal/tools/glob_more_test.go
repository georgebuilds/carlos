package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestGlob_RespectGitignoreFalse — with respect_gitignore=false the
// gitignored path IS listed (the non-gitignore walk branch), but .git is
// still pruned.
func TestGlob_RespectGitignoreFalse(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "node_modules/\n")
	writeFile(t, filepath.Join(root, "node_modules", "x.js"), "")
	writeFile(t, filepath.Join(root, "src", "y.js"), "")
	writeFile(t, filepath.Join(root, ".git", "config.js"), "")

	in, _ := json.Marshal(map[string]any{
		"pattern":           "**/*.js",
		"root":              root,
		"respect_gitignore": false,
	})
	out, err := NewGlobTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("node_modules/x.js")) {
		t.Errorf("respect_gitignore=false should list ignored path: %q", out)
	}
	if !bytes.Contains(out, []byte("src/y.js")) {
		t.Errorf("expected src/y.js: %q", out)
	}
	if bytes.Contains(out, []byte(".git/")) {
		t.Errorf(".git must still be pruned: %q", out)
	}
}

// TestGlob_AnchoredPattern — a leading-slash pattern anchors to root, so
// only top-level matches qualify.
func TestGlob_AnchoredLeadingSlash(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "")
	writeFile(t, filepath.Join(root, "sub", "main.go"), "")

	in, _ := json.Marshal(map[string]any{"pattern": "/main.go", "root": root})
	out, err := NewGlobTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !bytes.Contains(out, []byte("main.go")) {
		t.Errorf("anchored /main.go should match top-level: %q", s)
	}
	if bytes.Contains(out, []byte("sub/main.go")) {
		t.Errorf("anchored /main.go must NOT match sub/main.go: %q", s)
	}
}

// TestGlob_DirectoryTrailingSlash — matched directories are printed with a
// trailing slash.
func TestGlob_DirectoryTrailingSlash(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "builddir", "out.bin"), "")

	in, _ := json.Marshal(map[string]any{"pattern": "builddir", "root": root})
	out, err := NewGlobTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("builddir/")) {
		t.Errorf("directory match should carry trailing slash: %q", out)
	}
}

// TestGlob_BaseDirResolvesRelativeRoot — a GlobTool with BaseDir resolves
// a relative root argument against it.
func TestGlob_BaseDirResolvesRelativeRoot(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "proj", "a.go"), "")

	tool := &GlobTool{BaseDir: base}
	in, _ := json.Marshal(map[string]any{"pattern": "*.go", "root": "proj"})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("a.go")) {
		t.Errorf("BaseDir-relative root should find proj/a.go: %q", out)
	}
}

// TestGlob_UnanchoredMatchesNested — a plain pattern (no leading slash,
// no **) still matches files in subdirectories via the implicit **/
// fallback.
func TestGlob_UnanchoredMatchesNested(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "deep", "nested", "target.go"), "")

	in, _ := json.Marshal(map[string]any{"pattern": "target.go", "root": root})
	out, err := NewGlobTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("deep/nested/target.go")) {
		t.Errorf("unanchored pattern should match nested file via **/ fallback: %q", out)
	}
}

// TestGlob_DefaultsToCwd — with no root and no BaseDir the glob runs from
// the process cwd.
func TestGlob_DefaultsToCwd(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cwdmatch.go"), "")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{"pattern": "*.go"})
	out, err := NewGlobTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("cwdmatch.go")) {
		t.Errorf("cwd-default glob should find cwdmatch.go: %q", out)
	}
}

// TestGlob_BaseDirDefaultRoot — when root is omitted and BaseDir is set,
// the glob runs inside BaseDir.
func TestGlob_BaseDirDefaultRoot(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "z.txt"), "")

	tool := &GlobTool{BaseDir: base}
	in, _ := json.Marshal(map[string]any{"pattern": "*.txt"})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("z.txt")) {
		t.Errorf("omitted root should default to BaseDir: %q", out)
	}
}
