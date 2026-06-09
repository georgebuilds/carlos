package projectctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a t.Helper for synthetic tree construction.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// markGitRoot writes an empty .git directory so isGitRoot stops the
// walk. We use a directory (real git) rather than a file (worktree git
// link) so the test doesn't depend on filepath semantics for symlinks.
func markGitRoot(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
}

// --- Discover ---

func TestDiscoverAgentsAtCwd(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "cwd rules\n")

	files, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	if files[0].Source != "AGENTS.md" {
		t.Errorf("Source: want AGENTS.md got %q", files[0].Source)
	}
	if files[0].Level != 0 {
		t.Errorf("Level: want 0 got %d", files[0].Level)
	}
	if files[0].SizeBytes == 0 {
		t.Errorf("SizeBytes: want > 0")
	}
}

func TestDiscoverAgentsAtParent(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root rules\n")
	child := filepath.Join(root, "sub")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	files, err := Discover(child)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	if files[0].Level != 1 {
		t.Errorf("Level: want 1 got %d", files[0].Level)
	}
}

func TestDiscoverStopsAtGitRoot(t *testing.T) {
	// outer/ has AGENTS.md (would-be parent context).
	// outer/inner/ is its own git root, also has AGENTS.md.
	// Walking from outer/inner/sub/ should only find the inner AGENTS.md
	// — we must NOT escape the inner git root into outer/.
	outer := t.TempDir()
	writeFile(t, filepath.Join(outer, "AGENTS.md"), "outer rules\n")
	inner := filepath.Join(outer, "inner")
	markGitRoot(t, inner)
	writeFile(t, filepath.Join(inner, "AGENTS.md"), "inner rules\n")
	sub := filepath.Join(inner, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	files, err := Discover(sub)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file (inner only), got %d: %+v", len(files), files)
	}
	if !strings.HasPrefix(files[0].Path, inner) {
		t.Errorf("expected inner AGENTS.md, got %q", files[0].Path)
	}
}

func TestDiscoverBothNamesSameLevel(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "open\n")
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "claude\n")
	writeFile(t, filepath.Join(root, ".claude", "CLAUDE.md"), "claude ns\n")
	writeFile(t, filepath.Join(root, ".agents", "AGENTS.md"), "agents ns\n")

	files, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(files) != 4 {
		t.Fatalf("want 4 files, got %d", len(files))
	}
	// Priority order from candidateNames.
	want := []string{
		"AGENTS.md",
		"CLAUDE.md",
		filepath.Join(".claude", "CLAUDE.md"),
		filepath.Join(".agents", "AGENTS.md"),
	}
	for i, f := range files {
		if f.Source != want[i] {
			t.Errorf("file %d: want %q got %q", i, want[i], f.Source)
		}
	}
}

func TestDiscoverMultiLevelOrdering(t *testing.T) {
	// root: AGENTS.md
	// root/mid: AGENTS.md
	// root/mid/leaf: AGENTS.md
	// Discover from leaf should return Level=0 first.
	root := t.TempDir()
	markGitRoot(t, root)
	mid := filepath.Join(root, "mid")
	leaf := filepath.Join(mid, "leaf")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "L2\n")
	writeFile(t, filepath.Join(mid, "AGENTS.md"), "L1\n")
	writeFile(t, filepath.Join(leaf, "AGENTS.md"), "L0\n")

	files, err := Discover(leaf)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("want 3 files, got %d", len(files))
	}
	for i, want := range []int{0, 1, 2} {
		if files[i].Level != want {
			t.Errorf("file %d: want Level=%d got %d", i, want, files[i].Level)
		}
	}
}

func TestDiscoverNoFiles(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	files, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("want 0 files in empty project, got %d", len(files))
	}
}

func TestDiscoverSkipsHiddenDirs(t *testing.T) {
	// An AGENTS.md inside .git/ must not be picked up — the walker should
	// never descend into hidden trees.
	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, ".git", "AGENTS.md"), "should not load\n")

	files, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, f := range files {
		if strings.Contains(f.Path, string(filepath.Separator)+".git"+string(filepath.Separator)) {
			t.Errorf("walked into .git: %q", f.Path)
		}
	}
}

func TestDiscoverRejectsBadCwd(t *testing.T) {
	if _, err := Discover(""); err == nil {
		t.Errorf("empty cwd should error")
	}
	if _, err := Discover(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Errorf("missing cwd should error")
	}
}

// --- Load ---

func TestLoadConcatenatesWithHeaders(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "rule A\n")

	ctx, err := LoadFromCwd(root)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	if !strings.Contains(ctx.Combined, "# [project context: AGENTS.md]") {
		t.Errorf("missing provenance header in:\n%s", ctx.Combined)
	}
	if !strings.Contains(ctx.Combined, "rule A") {
		t.Errorf("missing file content in:\n%s", ctx.Combined)
	}
	if ctx.TotalBytes == 0 {
		t.Errorf("TotalBytes: want > 0")
	}
	if len(ctx.Files) != 1 {
		t.Errorf("Files: want 1 got %d", len(ctx.Files))
	}
}

func TestLoadParentBeforeChild(t *testing.T) {
	// Parent context must appear BEFORE child context in Combined, so
	// the child can override the parent in reading order.
	root := t.TempDir()
	markGitRoot(t, root)
	child := filepath.Join(root, "kid")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "PARENT\n")
	writeFile(t, filepath.Join(child, "AGENTS.md"), "CHILD\n")

	ctx, err := LoadFromCwd(child)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	pIdx := strings.Index(ctx.Combined, "PARENT")
	cIdx := strings.Index(ctx.Combined, "CHILD")
	if pIdx < 0 || cIdx < 0 {
		t.Fatalf("missing content: parent=%d child=%d in:\n%s", pIdx, cIdx, ctx.Combined)
	}
	if pIdx >= cIdx {
		t.Errorf("expected parent before child; parentIdx=%d childIdx=%d", pIdx, cIdx)
	}
}

func TestLoadFromCwdNoFiles(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	ctx, err := LoadFromCwd(root)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	if ctx == nil {
		t.Fatal("ctx is nil")
	}
	if ctx.Combined != "" {
		t.Errorf("Combined: want empty got %q", ctx.Combined)
	}
}

// --- @-include resolution ---

func TestExpandIncludesSimple(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "main\n@./inc.md\ntail\n")
	writeFile(t, filepath.Join(root, "inc.md"), "INCLUDED\n")

	ctx, err := LoadFromCwd(root)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	if !strings.Contains(ctx.Combined, "INCLUDED") {
		t.Errorf("include not expanded:\n%s", ctx.Combined)
	}
	if !strings.Contains(ctx.Combined, "main") || !strings.Contains(ctx.Combined, "tail") {
		t.Errorf("surrounding content lost:\n%s", ctx.Combined)
	}
}

func TestExpandIncludesNested(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "@./a.md\n")
	writeFile(t, filepath.Join(root, "a.md"), "A-start\n@./b.md\nA-end\n")
	writeFile(t, filepath.Join(root, "b.md"), "B-leaf\n")

	ctx, err := LoadFromCwd(root)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	for _, s := range []string{"A-start", "B-leaf", "A-end"} {
		if !strings.Contains(ctx.Combined, s) {
			t.Errorf("missing %q in:\n%s", s, ctx.Combined)
		}
	}
}

func TestExpandIncludesCycle(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "@./a.md\n")
	writeFile(t, filepath.Join(root, "a.md"), "@./b.md\n")
	writeFile(t, filepath.Join(root, "b.md"), "@./a.md\n")

	ctx, err := LoadFromCwd(root)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	if !strings.Contains(ctx.Combined, "cycle detected") {
		t.Errorf("cycle not detected:\n%s", ctx.Combined)
	}
}

func TestExpandIncludesMissingFile(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "@./nope.md\n")

	ctx, err := LoadFromCwd(root)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	if !strings.Contains(ctx.Combined, "include failed") {
		t.Errorf("missing include should emit failure stub:\n%s", ctx.Combined)
	}
}

func TestLoad_IncludeExpansionErrorEmitsStub(t *testing.T) {
	// When an @-include fails (here: target file doesn't exist), the
	// loader must surface the failure rather than silently writing
	// partial content:
	//   - the inline `[project context: include failed - ...]` stub from
	//     expandIncludes is preserved
	//   - a trailing `[project context: include expand failed - ...]`
	//     summary stub is appended after the expanded content
	//   - the prefix content BEFORE the failed include is still present
	//   - the LoadedFile is flagged Partial=true so callers can surface
	//     the degraded state
	//   - the overall Load call does not return an error or panic
	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"),
		"PREFIX-CONTENT\n@./missing.md\nSUFFIX-CONTENT\n")

	ctx, err := LoadFromCwd(root)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	if ctx == nil {
		t.Fatal("ctx is nil")
	}

	// Prefix content before the broken include must still be present
	// (verifies we didn't drop the partial expansion).
	if !strings.Contains(ctx.Combined, "PREFIX-CONTENT") {
		t.Errorf("prefix content lost on include failure:\n%s", ctx.Combined)
	}
	// Suffix after the broken include should also be present — the
	// individual include failure is non-fatal; expansion continues.
	if !strings.Contains(ctx.Combined, "SUFFIX-CONTENT") {
		t.Errorf("suffix content lost on include failure:\n%s", ctx.Combined)
	}

	// Inline stub from expandIncludes (the per-line marker).
	if !strings.Contains(ctx.Combined, "[project context: include failed - ./missing.md") {
		t.Errorf("missing inline include-failed stub:\n%s", ctx.Combined)
	}
	// Trailing summary stub appended by Load.
	if !strings.Contains(ctx.Combined, "[project context: include expand failed - AGENTS.md") {
		t.Errorf("missing trailing 'include expand failed' summary stub:\n%s", ctx.Combined)
	}

	// LoadedFile.Partial must reflect the degraded state.
	if len(ctx.Files) != 1 {
		t.Fatalf("Files: want 1 got %d", len(ctx.Files))
	}
	if !ctx.Files[0].Partial {
		t.Errorf("LoadedFile.Partial: want true on include failure, got false")
	}

	// The summary stub must appear AFTER the prefix content in reading
	// order, so the model reads the partial output and then the marker.
	prefixIdx := strings.Index(ctx.Combined, "PREFIX-CONTENT")
	stubIdx := strings.Index(ctx.Combined, "include expand failed")
	if prefixIdx < 0 || stubIdx < 0 || stubIdx <= prefixIdx {
		t.Errorf("expected summary stub after prefix; prefixIdx=%d stubIdx=%d in:\n%s",
			prefixIdx, stubIdx, ctx.Combined)
	}
}

func TestLoad_IncludeExpansionHappyPathNotPartial(t *testing.T) {
	// Sanity check: a successful expansion must NOT set Partial and must
	// NOT emit a trailing 'include expand failed' stub. Guards the new
	// flag against regressing on happy-path loads.
	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "main\n@./inc.md\ntail\n")
	writeFile(t, filepath.Join(root, "inc.md"), "INCLUDED\n")

	ctx, err := LoadFromCwd(root)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	if len(ctx.Files) != 1 {
		t.Fatalf("Files: want 1 got %d", len(ctx.Files))
	}
	if ctx.Files[0].Partial {
		t.Errorf("LoadedFile.Partial: want false on clean expansion, got true")
	}
	if strings.Contains(ctx.Combined, "include expand failed") {
		t.Errorf("unexpected failure stub on clean expansion:\n%s", ctx.Combined)
	}
}

func TestExpandIncludesIgnoresEmailLike(t *testing.T) {
	// "@georgebuilds" in prose must NOT be treated as an include — would
	// generate spurious "include failed" stubs in every README that
	// mentions a handle.
	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "ping @georgebuilds for review\n")

	ctx, err := LoadFromCwd(root)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	if strings.Contains(ctx.Combined, "include failed") {
		t.Errorf("@handle should not be treated as include:\n%s", ctx.Combined)
	}
	if !strings.Contains(ctx.Combined, "@georgebuilds") {
		t.Errorf("original @handle should be preserved:\n%s", ctx.Combined)
	}
}

func TestExpandIncludesSkipsCodeFence(t *testing.T) {
	// An `@./path` inside a ``` fence is shell-prompt example text, not
	// an include directive.
	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "before\n```\n@./fake.md\n```\nafter\n")

	ctx, err := LoadFromCwd(root)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	if strings.Contains(ctx.Combined, "include failed") {
		t.Errorf("@-line inside code fence should not be expanded:\n%s", ctx.Combined)
	}
	if !strings.Contains(ctx.Combined, "@./fake.md") {
		t.Errorf("original @-line inside fence should be preserved:\n%s", ctx.Combined)
	}
}

func TestExpandIncludesTilde(t *testing.T) {
	// ~/<file> should resolve to $HOME/<file>. We point HOME at a temp
	// dir so the test is hermetic.
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, "global.md"), "GLOBAL\n")

	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "@~/global.md\n")

	ctx, err := LoadFromCwd(root)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	if !strings.Contains(ctx.Combined, "GLOBAL") {
		t.Errorf("~ include not resolved:\n%s", ctx.Combined)
	}
}

func TestExpandIncludesDepthCap(t *testing.T) {
	// Build a 6-deep chain; cap is 4. The deepest two should NOT be
	// expanded (they hit the cap stub).
	root := t.TempDir()
	markGitRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "@./d1.md\n")
	for i := 1; i <= 5; i++ {
		next := i + 1
		writeFile(t, filepath.Join(root, "d"+itoa(i)+".md"),
			"D"+itoa(i)+"\n@./d"+itoa(next)+".md\n")
	}
	writeFile(t, filepath.Join(root, "d6.md"), "D6-LEAF\n")

	ctx, err := LoadFromCwd(root)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	if strings.Contains(ctx.Combined, "D6-LEAF") {
		t.Errorf("depth cap not enforced; leaf appeared:\n%s", ctx.Combined)
	}
	if !strings.Contains(ctx.Combined, "depth") {
		t.Errorf("depth cap message missing:\n%s", ctx.Combined)
	}
}

// --- Truncation ---

func TestLoadTruncatesAtCap(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	// Build a file > MaxTotalBytes (256 KiB).
	big := strings.Repeat("x", int(MaxTotalBytes)+1024)
	writeFile(t, filepath.Join(root, "AGENTS.md"), big)

	ctx, err := LoadFromCwd(root)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	if !ctx.Truncated {
		t.Errorf("expected Truncated=true on >256 KiB content")
	}
	if ctx.TotalBytes > MaxTotalBytes {
		t.Errorf("TotalBytes %d exceeded cap %d", ctx.TotalBytes, MaxTotalBytes)
	}
}

func TestLoadTruncatesAcrossMultipleFiles(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	// Two files of ~200 KiB each — first fits, second is partially
	// dropped.
	chunk := strings.Repeat("y", 200*1024)
	writeFile(t, filepath.Join(root, "AGENTS.md"), chunk)
	writeFile(t, filepath.Join(root, "CLAUDE.md"), chunk)

	ctx, err := LoadFromCwd(root)
	if err != nil {
		t.Fatalf("LoadFromCwd: %v", err)
	}
	if !ctx.Truncated {
		t.Errorf("expected Truncated=true across multiple files")
	}
}

// itoa is a tiny strconv.Itoa to avoid importing strconv just for the
// loop in TestExpandIncludesDepthCap.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
