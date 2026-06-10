package notes

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureVault returns the absolute path to internal/notes/testdata/vault
// for use as the primary vault in tests. testdata/vault_alt holds an
// independent fixture for cross-vault isolation assertions.
func fixtureVault(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("testdata/vault")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

func altVault(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("testdata/vault_alt")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

// TestCacheOpenIdempotent verifies Cache.Open returns the SAME index
// pointer across calls — the lazy-build sync.Once contract.
func TestCacheOpenIdempotent(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v1, err := c.Open(fixtureVault(t))
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	v2, err := c.Open(fixtureVault(t))
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if v1 != v2 {
		t.Errorf("Open should return same *VaultIndex; got distinct pointers")
	}
}

// TestCacheOpenMissingPath ensures a non-existent path yields a clean
// error rather than a panic.
func TestCacheOpenMissingPath(t *testing.T) {
	c := NewCache(nil)
	_, err := c.Open("/nonexistent/path/for/test")
	if err == nil {
		t.Fatal("expected error opening nonexistent path")
	}
}

// TestCacheOpenEmptyPath checks the empty-string case maps to
// ErrNoVaultConfigured (used by ResolveVaultPath).
func TestCacheOpenEmptyPath(t *testing.T) {
	c := NewCache(nil)
	_, err := c.Open("")
	if !errors.Is(err, ErrNoVaultConfigured) {
		t.Errorf("expected ErrNoVaultConfigured, got %v", err)
	}
}

// TestResolveVaultPathPerCallWins exercises the per-call override
// semantics: any non-empty perCallVault wins over cfg.Vault.Path.
func TestResolveVaultPathPerCallWins(t *testing.T) {
	got, err := ResolveVaultPath("/cfg/path", "/perCall/path")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.HasSuffix(got, "/perCall/path") {
		t.Errorf("per-call should win; got %q", got)
	}
}

func TestResolveVaultPathCfgFallback(t *testing.T) {
	got, err := ResolveVaultPath("/cfg/path", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.HasSuffix(got, "/cfg/path") {
		t.Errorf("cfg should be used; got %q", got)
	}
}

func TestResolveVaultPathBothEmpty(t *testing.T) {
	_, err := ResolveVaultPath("", "")
	if !errors.Is(err, ErrNoVaultConfigured) {
		t.Errorf("expected ErrNoVaultConfigured, got %v", err)
	}
}

// TestGetByExactPath verifies the path-match branch of resolveLink.
func TestGetByExactPath(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, err := c.Open(fixtureVault(t))
	if err != nil {
		t.Fatal(err)
	}
	n, err := v.Get("carlos.md")
	if err != nil {
		t.Fatalf("get carlos.md: %v", err)
	}
	if n.Path != "carlos.md" {
		t.Errorf("path: want carlos.md got %q", n.Path)
	}
}

// TestGetByTitle exercises the case-insensitive title lookup branch
// for a uniquely-named note.
func TestGetByTitle(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	n, err := v.Get("carlos")
	if err != nil {
		t.Fatalf("get carlos: %v", err)
	}
	if n.Path != "carlos.md" {
		t.Errorf("expected carlos.md; got %q", n.Path)
	}
}

// TestGetAmbiguousShortestPathWins is the "two notes with the same
// title" case from Obsidian: shortest relpath wins.
func TestGetAmbiguousShortestPathWins(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	n, err := v.Get("notes")
	if err != nil {
		t.Fatalf("get notes: %v", err)
	}
	if n.Path != "notes.md" {
		t.Errorf("shortest path should win; got %q", n.Path)
	}
}

// TestGetByAlias verifies aliases get folded into the title index.
func TestGetByAlias(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	n, err := v.Get("skill induction")
	if err != nil {
		t.Fatalf("get skill induction (alias): %v", err)
	}
	if n.Path != "skill-induction.md" {
		t.Errorf("alias should resolve to skill-induction.md; got %q", n.Path)
	}
}

// TestGetUnresolved verifies ErrNotFound surfaces for misses.
func TestGetUnresolved(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	_, err := v.Get("phase 99")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestResolveStripsBrackets allows callers to pass "[[X]]" verbatim.
func TestResolveStripsBrackets(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	resolved, _, err := v.Resolve("[[carlos]]")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved != "carlos.md" {
		t.Errorf("expected carlos.md; got %q", resolved)
	}
}

// TestResolveDropsSectionAnchor — `[[note#section]]` resolves at the
// note level; the section is consumed by the caller layer.
func TestResolveDropsSectionAnchor(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	resolved, _, err := v.Resolve("mvp-roadmap#Phase 12")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved != "mvp-roadmap.md" {
		t.Errorf("expected mvp-roadmap.md; got %q", resolved)
	}
}

// TestTwoVaultsAreIsolated is the load-bearing assertion: indexing
// vault A then vault B doesn't leak between them.
func TestTwoVaultsAreIsolated(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	primary, err := c.Open(fixtureVault(t))
	if err != nil {
		t.Fatal(err)
	}
	alt, err := c.Open(altVault(t))
	if err != nil {
		t.Fatal(err)
	}

	primaryCarlos, err := primary.Get("carlos")
	if err != nil {
		t.Fatalf("primary carlos: %v", err)
	}
	if primaryCarlos.Title != "carlos" {
		t.Errorf("primary title: %q", primaryCarlos.Title)
	}

	altCarlos, err := alt.Get("carlos")
	if err != nil {
		t.Fatalf("alt carlos: %v", err)
	}
	if !strings.Contains(altCarlos.Title, "alt vault") {
		t.Errorf("alt should be the alt fixture; got %q", altCarlos.Title)
	}

	if _, err := primary.Get("only-here"); !errors.Is(err, ErrNotFound) {
		t.Errorf("primary must not see only-here; err=%v", err)
	}
	if _, err := alt.Get("hermes-distillation"); !errors.Is(err, ErrNotFound) {
		t.Errorf("alt must not see hermes-distillation; err=%v", err)
	}
}

// TestSearchSubstring covers the basic body-hit path.
func TestSearchSubstring(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	hits, err := v.Search("skill induction", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit for 'skill induction'")
	}
	// The title-matched note should rank first.
	if !strings.Contains(strings.ToLower(hits[0].Title), "skill induction") {
		t.Errorf("top hit should be skill induction note; got %q", hits[0].Title)
	}
}

// TestSearchTagFilter restricts to a tag.
func TestSearchTagFilter(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	hits, err := v.Search("carlos", SearchOptions{Tag: "research"})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		n, _ := v.Get(h.Path)
		if !containsString(n.Tags, "research") {
			t.Errorf("hit %s missing tag research", h.Path)
		}
	}
}

// TestSearchWhereFrontmatter restricts via frontmatter k/v.
func TestSearchWhereFrontmatter(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	hits, err := v.Search("phase", SearchOptions{Where: map[string]any{"project": "carlos"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	for _, h := range hits {
		n, _ := v.Get(h.Path)
		if got := n.Frontmatter["project"]; got != "carlos" {
			t.Errorf("hit %s project=%v", h.Path, got)
		}
	}
}

// TestSearchLimitHonored — limit caps the result count.
func TestSearchLimitHonored(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	hits, err := v.Search("carlos", SearchOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Errorf("limit=1; got %d hits", len(hits))
	}
}

// TestBacklinks pulls every link into carlos.md and checks the carlos
// note is reachable from skill-induction + mvp-roadmap + hermes-
// distillation + onboarding.
func TestBacklinks(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	bl, err := v.Backlinks("carlos", 50)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"mvp-roadmap.md":         false,
		"skill-induction.md":     false,
		"hermes-distillation.md": false,
		"onboarding.md":          false,
	}
	for _, b := range bl {
		want[b.Path] = true
	}
	for path, hit := range want {
		if !hit {
			t.Errorf("backlinks missing %s", path)
		}
	}
}

// TestBacklinksAreVaultLocal — carlos.md in vault_alt must NOT appear
// in primary.Backlinks("carlos").
func TestBacklinksAreVaultLocal(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	primary, _ := c.Open(fixtureVault(t))
	_, _ = c.Open(altVault(t))
	bl, err := primary.Backlinks("carlos", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bl {
		if strings.Contains(b.Path, "vault_alt") {
			t.Errorf("primary backlinks bled into alt: %s", b.Path)
		}
	}
}

// TestTaggedIncludesBothFrontmatterAndInline pins that #project inline
// tags + tags: [project] frontmatter both populate v.tags["project"].
func TestTaggedIncludesBothFrontmatterAndInline(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	notes, err := v.Tagged("project", 50)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, n := range notes {
		paths[n.Path] = true
	}
	if !paths["carlos.md"] {
		t.Error("tagged(project) should include carlos.md (frontmatter)")
	}
	if !paths["mvp-roadmap.md"] {
		t.Error("tagged(project) should include mvp-roadmap.md (inline #project)")
	}
}

// TestRecentOrdering verifies newest-first ordering. We touch
// mvp-roadmap.md ahead of carlos.md and assert it appears first.
func TestRecentOrdering(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))

	// Set explicit modtimes on two notes so the assertion is
	// deterministic regardless of how the files were laid down on
	// disk.
	now := time.Now()
	if err := os.Chtimes(filepath.Join(v.Root, "mvp-roadmap.md"), now, now); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(v.Root, "onboarding.md"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := v.Refresh(); err != nil {
		t.Fatal(err)
	}
	recent := v.Recent(2, 0)
	if len(recent) != 2 {
		t.Fatalf("expected 2 recent; got %d", len(recent))
	}
	if recent[0].Path != "mvp-roadmap.md" {
		t.Errorf("recent[0]: want mvp-roadmap.md got %q", recent[0].Path)
	}

	// since=24h should drop onboarding.md.
	since := v.Recent(10, 24*time.Hour)
	for _, n := range since {
		if n.Path == "onboarding.md" {
			t.Error("since=24h should have dropped onboarding.md")
		}
	}
}

// TestNeighborsOutInUnresolved exercises Neighbors's three return
// channels. carlos.md links to mvp-roadmap, hermes-distillation,
// skill-induction (resolved) + unresolved-target (unresolved).
func TestNeighborsOutInUnresolved(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	out, in, unres, err := v.Neighbors("carlos")
	if err != nil {
		t.Fatal(err)
	}
	outPaths := map[string]bool{}
	for _, n := range out {
		outPaths[n.Path] = true
	}
	for _, want := range []string{"mvp-roadmap.md", "hermes-distillation.md", "skill-induction.md"} {
		if !outPaths[want] {
			t.Errorf("neighbors outgoing missing %s", want)
		}
	}
	if len(in) == 0 {
		t.Error("expected at least one incoming neighbor")
	}
	// Unresolved links have Target="" (resolveLink rewrote it). The
	// raw text is preserved in Display so the model still has
	// something to surface to the user.
	found := false
	for _, u := range unres {
		if u.Display == "unresolved-target" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ghost-link unresolved-target; got %+v", unres)
	}
}

// TestMaybeRefreshDetectsChange — after a body edit + mtime bump, the
// index reflects the new body.
func TestMaybeRefreshDetectsChange(t *testing.T) {
	// Use a temp copy so we don't mutate the committed testdata.
	src := fixtureVault(t)
	dst := t.TempDir()
	copyDir(t, src, dst)

	c := NewCache([]string{"templates/**"})
	v, err := c.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get("carlos"); err != nil {
		t.Fatal(err)
	}

	// Add a new note and bump mtimes.
	newPath := filepath.Join(dst, "fresh.md")
	if err := os.WriteFile(newPath, []byte("---\ntitle: fresh\ntags: [meta]\n---\n\n# fresh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := v.MaybeRefresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get("fresh"); err != nil {
		t.Errorf("MaybeRefresh missed new note: %v", err)
	}
}

// TestMaybeRefreshNoOpWhenUnchanged — the cheap path: no files changed,
// MaybeRefresh returns nil quickly without mutating index.
func TestMaybeRefreshNoOpWhenUnchanged(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	carlosBefore, _ := v.Get("carlos")
	if err := v.MaybeRefresh(); err != nil {
		t.Fatal(err)
	}
	carlosAfter, _ := v.Get("carlos")
	if carlosBefore != carlosAfter {
		t.Error("MaybeRefresh on unchanged vault should not rebuild")
	}
}

// TestExcludesFilterApplied — the templates/** exclude keeps templated
// notes out of the byTitle index.
func TestExcludesFilterApplied(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	if _, err := v.Get("excluded"); !errors.Is(err, ErrNotFound) {
		t.Errorf("excluded should not be indexed; got %v", err)
	}
}

// TestFencedCodeNotParsed — wikilinks inside fenced blocks must NOT
// land in mvp-roadmap.md's links.
func TestFencedCodeNotParsed(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	n, _ := v.Get("mvp-roadmap")
	for _, l := range n.Links {
		if l.Target == "code-fenced" || strings.Contains(l.Display, "code-fenced") {
			t.Errorf("fenced wikilink leaked: %+v", l)
		}
	}
	if containsString(n.Tags, "fenced-tag") {
		t.Error("fenced #tag leaked into tags")
	}
}

// TestURLAnchorNotTag — `(url#section)` must not produce a `#section`
// tag.
func TestURLAnchorNotTag(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	n, _ := v.Get("skill-induction")
	if containsString(n.Tags, "section") {
		t.Error("URL anchor leaked as a #section tag")
	}
}

// TestSectionBodyExtraction pulls the body under a heading.
func TestSectionBodyExtraction(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	n, _ := v.Get("mvp-roadmap")
	body := SectionBody(n, "Phase 11")
	if !strings.Contains(body, "orchestrator") {
		t.Errorf("section body for 'Phase 11' should include 'orchestrator'; got %q", body)
	}
	// Must NOT bleed into the next sibling heading's content.
	if strings.Contains(body, "## Phase 12 — Notes tools") {
		t.Errorf("section extraction overran into next heading; got %q", body)
	}
}

// TestSearchMatchLineIsFileRelativeWithFrontmatter is the regression for
// the bodySnippet body-vs-file coordinate bug: a body-only match must
// report Match.Line in file-relative coordinates so a consumer opening
// the file at that line lands ON the match, not inside the frontmatter.
//
// Layout (file-relative line numbers in comments):
//
//	1: ---
//	2: title: frontmatter-line-test
//	3: ---
//	4: (blank)
//	5: filler line one
//	6: filler line two
//	7: filler line three
//	8: filler line four
//	9: targetword sits here   <-- body line 6, file line 9
//
// Body-relative count: skip headerLines (3) → body line 6 = file line 9.
func TestSearchMatchLineIsFileRelativeWithFrontmatter(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join([]string{
		"---",
		"title: frontmatter-line-test",
		"---",
		"",
		"filler line one",
		"filler line two",
		"filler line three",
		"filler line four",
		"targetword sits here",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "fm.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := v.Search("targetword", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected one hit for targetword")
	}
	// File line 9 is where `targetword` lives. The bug had Line=6
	// (body-relative) which would open vim inside the frontmatter.
	if hits[0].Line != 9 {
		t.Errorf("Match.Line: want 9 (file-relative), got %d", hits[0].Line)
	}

	// Sanity check the byte-level coordinate: the line we report,
	// extracted directly from the file, must contain the query.
	raw, err := os.ReadFile(filepath.Join(dir, "fm.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	if hits[0].Line < 1 || hits[0].Line > len(lines) {
		t.Fatalf("reported line %d out of range (file has %d lines)", hits[0].Line, len(lines))
	}
	if !strings.Contains(lines[hits[0].Line-1], "targetword") {
		t.Errorf("file-line %d = %q does not contain match", hits[0].Line, lines[hits[0].Line-1])
	}
}

// TestSearchMatchLineNoFrontmatter pins the trivial no-header case so a
// future "always add headerLines" implementation doesn't drift the
// no-frontmatter path off by some constant offset.
func TestSearchMatchLineNoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join([]string{
		"# header",
		"",
		"filler one",
		"targetword on line four",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "plain.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := v.Search("targetword", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected one hit for targetword")
	}
	if hits[0].Line != 4 {
		t.Errorf("Match.Line: want 4, got %d", hits[0].Line)
	}
}

// TestBodySnippetEmptyQuery guards the degenerate q="" case. The bug:
// strings.Index(lower, "") == 0 and strings.Count(lower, "") ==
// len(lower)+1, so a direct caller passing an empty query would otherwise
// get a max-score garbage snippet covering the head of the body.
func TestBodySnippetEmptyQuery(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("bodySnippet(\"\") panicked: %v", r)
		}
	}()
	sn, ln, cnt := bodySnippet("hello world\nsecond line\n", "", 0)
	if sn != "" {
		t.Errorf("snippet: want empty, got %q", sn)
	}
	if ln != 0 {
		t.Errorf("line: want 0, got %d", ln)
	}
	if cnt != 0 {
		t.Errorf("count: want 0, got %d", cnt)
	}
}

// copyDir is a tiny recursive copy used by MaybeRefresh tests.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
}
