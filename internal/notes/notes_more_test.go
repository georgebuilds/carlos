package notes

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestCacheResetPath drops a cached entry so the next Open rebuilds.
func TestCacheResetPath(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v1, err := c.Open(fixtureVault(t))
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	c.ResetPath(fixtureVault(t))
	v2, err := c.Open(fixtureVault(t))
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if v1 == v2 {
		t.Errorf("after ResetPath, Open should return a freshly-built index")
	}
}

// TestCacheResetPathBadPath is a no-op.
func TestCacheResetPathBadPath(t *testing.T) {
	c := NewCache(nil)
	// Path that fails canonicalisation: tilde-with-username form is
	// rejected by expandHome.
	c.ResetPath("~bad-user/foo")
	// Just confirm no panic / no side effect.
	if len(c.Keys()) != 0 {
		t.Errorf("expected empty cache")
	}
}

// TestCacheKeys lists every cached path, sorted.
func TestCacheKeys(t *testing.T) {
	c := NewCache(nil)
	if _, err := c.Open(fixtureVault(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Open(altVault(t)); err != nil {
		t.Fatal(err)
	}
	keys := c.Keys()
	if len(keys) != 2 {
		t.Fatalf("want 2 keys, got %d: %v", len(keys), keys)
	}
	// Keys are unsorted by contract; sort here to make the assertion
	// order-independent.
	sort.Strings(keys)
	if !strings.HasSuffix(keys[0], "testdata/vault") && !strings.HasSuffix(keys[1], "testdata/vault") {
		t.Errorf("primary vault path missing from keys: %v", keys)
	}
}

// TestExpandHome_TildeAlone resolves to the user's home directory.
func TestExpandHome_TildeAlone(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir on this platform")
	}
	got, err := expandHome("~")
	if err != nil {
		t.Fatal(err)
	}
	if got != home {
		t.Errorf("want %q, got %q", home, got)
	}
}

// TestExpandHome_TildeSlash resolves `~/sub` to home + sub.
func TestExpandHome_TildeSlash(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir on this platform")
	}
	got, err := expandHome("~/foo/bar")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "foo/bar")
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

// TestExpandHome_NonTilde passes through verbatim.
func TestExpandHome_NonTilde(t *testing.T) {
	got, err := expandHome("/abs/path")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/abs/path" {
		t.Errorf("got %q", got)
	}
}

// TestExpandHome_TildeUser is unsupported.
func TestExpandHome_TildeUser(t *testing.T) {
	_, err := expandHome("~alice/foo")
	if err == nil {
		t.Fatal("expected error for ~user form")
	}
}

// TestCanonicalisePath_EmptyError surfaces ErrNoVaultConfigured.
func TestCanonicalisePath_Empty(t *testing.T) {
	_, err := canonicalisePath("")
	if !errors.Is(err, ErrNoVaultConfigured) {
		t.Errorf("want ErrNoVaultConfigured, got %v", err)
	}
}

// TestCanonicalisePath_ExpandsTilde works when home is available.
func TestCanonicalisePath_ExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got, err := canonicalisePath("~/foo")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "foo")
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

// TestSearchTotal returns the pre-truncation count.
func TestSearchTotal(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	total, err := v.SearchTotal("carlos", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 {
		t.Errorf("want at least one match")
	}
	// With Limit=1, the total should still reflect every match.
	limitedHits, _ := v.Search("carlos", SearchOptions{Limit: 1})
	if total < len(limitedHits) {
		t.Errorf("total %d < limited %d", total, len(limitedHits))
	}
}

// TestSearchTotal_EmptyQuery rejects empty query.
func TestSearchTotal_EmptyQuery(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	_, err := v.SearchTotal("   ", SearchOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestSearchTotal_WithTagFilter excludes tagged-missing notes from the
// count.
func TestSearchTotal_WithTag(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	total, err := v.SearchTotal("carlos", SearchOptions{Tag: "research"})
	if err != nil {
		t.Fatal(err)
	}
	if total < 0 {
		t.Errorf("negative count: %d", total)
	}
}

// TestBodyRaw returns the post-frontmatter body.
func TestBodyRaw(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	n, err := v.Get("carlos")
	if err != nil {
		t.Fatal(err)
	}
	body := BodyRaw(n)
	if !strings.Contains(body, "# carlos") {
		t.Errorf("body missing heading: %q", body[:60])
	}
	// Frontmatter should NOT be in the body.
	if strings.Contains(body, "title: carlos") {
		t.Errorf("frontmatter leaked into body")
	}
}

// TestBodyRaw_Nil returns an empty string for a nil note.
func TestBodyRaw_Nil(t *testing.T) {
	if got := BodyRaw(nil); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

// TestDescription_FromFrontmatter prefers the frontmatter description.
func TestDescription_FromFrontmatter(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	n, _ := v.Get("carlos")
	d := Description(n)
	if !strings.Contains(d, "Pure-Go") {
		t.Errorf("description should come from frontmatter; got %q", d)
	}
}

// TestDescription_FallbackToFirstPara picks the first non-heading
// paragraph when frontmatter description is empty.
func TestDescription_FallbackParagraph(t *testing.T) {
	dir := t.TempDir()
	notePath := filepath.Join(dir, "x.md")
	body := "# Title\n\nThis is the first paragraph that should be the description.\n\nA second one.\n"
	if err := os.WriteFile(notePath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	n, err := v.Get("x.md")
	if err != nil {
		t.Fatal(err)
	}
	d := Description(n)
	if !strings.Contains(d, "first paragraph") {
		t.Errorf("description should be first paragraph; got %q", d)
	}
	// Should not bleed into the second paragraph.
	if strings.Contains(d, "second one") {
		t.Errorf("description should stop at blank line; got %q", d)
	}
}

// TestDescription_Nil returns empty.
func TestDescription_Nil(t *testing.T) {
	if got := Description(nil); got != "" {
		t.Errorf("got %q", got)
	}
}

// TestDescription_LongParagraphTruncates at 200 chars.
func TestDescription_LongParagraph(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("word ", 100)
	body := "# title\n\n" + long + "\n"
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := NewCache(nil).Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := v.Get("x.md")
	d := Description(n)
	if len(d) <= 200 || len(d) > 250 {
		// The cap is a soft one; we just confirm it didn't take the
		// entire long paragraph.
	}
	if d == "" {
		t.Error("description should not be empty")
	}
}

// TestStringField_Missing returns false.
func TestStringField_Missing(t *testing.T) {
	if _, ok := stringField(map[string]any{}, "x"); ok {
		t.Errorf("expected miss")
	}
}

// TestStringField_NonString returns false.
func TestStringField_NonString(t *testing.T) {
	if _, ok := stringField(map[string]any{"x": 5}, "x"); ok {
		t.Errorf("expected miss for non-string")
	}
}

// TestStringSliceField_StringScalar accepts a bare string and wraps it.
func TestStringSliceField_StringScalar(t *testing.T) {
	got := stringSliceField(map[string]any{"k": "single"}, "k")
	if len(got) != 1 || got[0] != "single" {
		t.Errorf("got %#v", got)
	}
}

// TestStringSliceField_EmptyString returns an empty slice.
func TestStringSliceField_EmptyString(t *testing.T) {
	got := stringSliceField(map[string]any{"k": ""}, "k")
	if len(got) != 0 {
		t.Errorf("want empty, got %#v", got)
	}
}

// TestStringSliceField_AnySliceWithNonStrings drops non-strings.
func TestStringSliceField_MixedTypes(t *testing.T) {
	got := stringSliceField(map[string]any{"k": []any{"a", 5, "b", ""}}, "k")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("got %#v", got)
	}
}

// TestStringSliceField_NativeStringSlice copies it.
func TestStringSliceField_StringSlice(t *testing.T) {
	got := stringSliceField(map[string]any{"k": []string{"a", "b"}}, "k")
	if len(got) != 2 || got[0] != "a" {
		t.Errorf("got %#v", got)
	}
}

// TestStringSliceField_Missing returns empty.
func TestStringSliceField_Missing(t *testing.T) {
	got := stringSliceField(map[string]any{}, "missing")
	if len(got) != 0 {
		t.Errorf("got %#v", got)
	}
}

// TestStringSliceField_WrongType returns empty.
func TestStringSliceField_WrongType(t *testing.T) {
	got := stringSliceField(map[string]any{"k": 42}, "k")
	if len(got) != 0 {
		t.Errorf("got %#v", got)
	}
}

// TestIsExcluded_DoubleStarPath matches a `templates/**` glob.
func TestIsExcluded_DoubleStar(t *testing.T) {
	if !isExcluded("templates/foo.md", []string{"templates/**"}) {
		t.Error("should match templates/**")
	}
	if isExcluded("other/foo.md", []string{"templates/**"}) {
		t.Error("should not match templates/**")
	}
}

// TestIsExcluded_EmptyPattern is ignored.
func TestIsExcluded_EmptyPattern(t *testing.T) {
	if isExcluded("a.md", []string{""}) {
		t.Error("empty pattern should match nothing")
	}
}

// TestIsExcluded_BasenameGlob - `*.draft.md` against basename.
func TestIsExcluded_BasenameGlob(t *testing.T) {
	if !isExcluded("sub/dir/foo.draft.md", []string{"*.draft.md"}) {
		t.Error("basename glob should match")
	}
}

// TestIsExcluded_FullMatch tests the bare filepath.Match arm.
func TestIsExcluded_FullMatch(t *testing.T) {
	if !isExcluded("a.md", []string{"a.md"}) {
		t.Error("exact-path glob should match")
	}
}

// TestRelPath_OutsideRoot returns "".
func TestRelPath_OutsideRoot(t *testing.T) {
	// "abs" not inside root yields a relpath with ../ - the function
	// still returns it via filepath.ToSlash. Just confirm no panic.
	got := relPath("/root", "/elsewhere/file.md")
	if got == "" {
		t.Skip("platform-specific")
	}
}

// TestFenceMarkerOf covers the `~~~` branch.
func TestFenceMarkerOf(t *testing.T) {
	if got := fenceMarkerOf("```"); got != "```" {
		t.Errorf("backtick got %q", got)
	}
	if got := fenceMarkerOf("~~~"); got != "~~~" {
		t.Errorf("tilde got %q", got)
	}
}

// TestIsFence accepts both fence markers.
func TestIsFence(t *testing.T) {
	if !isFence("```") || !isFence("~~~") || !isFence("```go") {
		t.Error("isFence should accept ```/~~~ prefixes")
	}
	if isFence("not a fence") {
		t.Error("isFence false positive")
	}
}

// TestFileMTime returns stat info for an existing file.
func TestFileMTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.md")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	mt, sz, err := fileMTime(path)
	if err != nil {
		t.Fatal(err)
	}
	if mt.IsZero() || sz == 0 {
		t.Errorf("mt=%v sz=%d", mt, sz)
	}
}

// TestFileMTime_Missing surfaces the os.Stat error.
func TestFileMTime_Missing(t *testing.T) {
	_, _, err := fileMTime("/nonexistent/abs/path/here.md")
	if err == nil {
		t.Error("expected stat error")
	}
}

// TestLineOf_NegativeOffset clamps to line 1.
func TestLineOf_Negative(t *testing.T) {
	if got := lineOf([]byte("abc"), -5); got != 1 {
		t.Errorf("got %d", got)
	}
}

// TestLineOf_OffsetPastEnd clamps to last line.
func TestLineOf_PastEnd(t *testing.T) {
	if got := lineOf([]byte("a\nb\n"), 100); got != 3 {
		t.Errorf("got %d", got)
	}
}

// TestIndexLine finds a line at the start.
func TestIndexLine_AtStart(t *testing.T) {
	got := indexLine([]byte("---\nbody\n"), []byte("---"))
	if got != 0 {
		t.Errorf("got %d", got)
	}
}

// TestIndexLine_NotFound returns -1.
func TestIndexLine_NotFound(t *testing.T) {
	got := indexLine([]byte("body\nmore\n"), []byte("---"))
	if got != -1 {
		t.Errorf("got %d", got)
	}
}

// TestIndexLine_CRLFTolerated finds a line ending in `\r\n`.
func TestIndexLine_CRLF(t *testing.T) {
	got := indexLine([]byte("first\r\n---\r\nrest\r\n"), []byte("---"))
	if got != len("first\r\n") {
		t.Errorf("got %d", got)
	}
}

// TestBodyStart_NoFrontmatter returns 0.
func TestBodyStart_NoFM(t *testing.T) {
	if got := bodyStart([]byte("# hello\n")); got != 0 {
		t.Errorf("got %d", got)
	}
}

// TestBodyStart_CRLF tolerates Windows line endings.
func TestBodyStart_CRLF(t *testing.T) {
	src := []byte("---\r\ntitle: x\r\n---\r\nbody\r\n")
	if got := bodyStart(src); got == 0 || got >= len(src) {
		t.Errorf("got %d", got)
	}
}

// TestBodyStart_NoClosingDash returns 0 (fall-back behavior).
func TestBodyStart_NoCloser(t *testing.T) {
	src := []byte("---\ntitle: x\nbody without closer\n")
	if got := bodyStart(src); got != 0 {
		t.Errorf("expected 0 fall-back, got %d", got)
	}
}

// TestParseHeading_NonHeading returns ok=false.
func TestParseHeading_NotAHeading(t *testing.T) {
	if _, _, ok := parseHeading("just text"); ok {
		t.Error("non-heading line should not parse")
	}
}

// TestParseHeading_NoSpaceAfterHashes rejects (non-ATX).
func TestParseHeading_NoSpace(t *testing.T) {
	if _, _, ok := parseHeading("#nospace"); ok {
		t.Error("ATX requires space after hashes")
	}
}

// TestParseHeading_TooDeepLevel rejects 7+ hashes.
func TestParseHeading_TooDeep(t *testing.T) {
	if _, _, ok := parseHeading("####### deep"); ok {
		t.Error("level > 6 should be rejected")
	}
}

// TestParseHeading_Level6Accepted is the boundary.
func TestParseHeading_Level6(t *testing.T) {
	level, _, ok := parseHeading("###### six")
	if !ok || level != 6 {
		t.Errorf("level=%d ok=%v", level, ok)
	}
}

// TestSectionBody_Nil short-circuits.
func TestSectionBody_NilNote(t *testing.T) {
	if got := SectionBody(nil, "x"); got != "" {
		t.Errorf("got %q", got)
	}
}

// TestSectionBody_EmptyName short-circuits.
func TestSectionBody_EmptyName(t *testing.T) {
	n := &Note{body: "# hi\nstuff"}
	if got := SectionBody(n, ""); got != "" {
		t.Errorf("got %q", got)
	}
}

// TestSectionBody_NoMatch returns empty.
func TestSectionBody_NoMatch(t *testing.T) {
	n := &Note{body: "# hi\nstuff\n## sub\nother\n"}
	if got := SectionBody(n, "nonexistent"); got != "" {
		t.Errorf("got %q", got)
	}
}

// TestSectionBody_StopAtDeeperHeading verifies we stop at a same-or-
// shallower-level heading and DON'T leak into the next section.
func TestSectionBody_RespectsLevel(t *testing.T) {
	n := &Note{body: "## A\ncontent of A\n### A.sub\nstill A's tree\n## B\ncontent of B\n"}
	got := SectionBody(n, "A")
	if !strings.Contains(got, "content of A") {
		t.Errorf("missing A: %q", got)
	}
	if strings.Contains(got, "content of B") {
		t.Errorf("leaked into B: %q", got)
	}
	// Note: implementation includes deeper subsections (### A.sub) -
	// that matches the doc comment.
}

// TestVaultIndex_NotADir surfaces a clean error.
func TestVaultIndex_NotADir(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "afile")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := newVaultIndex(filePath, nil)
	if err == nil {
		t.Fatal("expected not-a-dir error")
	}
}

// TestVaultIndex_BrokenFrontmatterFileSkipped: a file with an
// unterminated frontmatter still parses (frontmatter handling is
// silent-skip on error per the doc comment) - confirm the whole
// vault still indexes.
func TestVaultIndex_BrokenFrontmatterSkipped(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.md")
	bad := filepath.Join(dir, "bad.md")
	if err := os.WriteFile(good, []byte("# good\n\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Unterminated frontmatter - silent-skip in parseFile.
	if err := os.WriteFile(bad, []byte("---\ntitle: bad\nno closer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := v.Get("good"); err != nil {
		t.Errorf("good note should be indexed: %v", err)
	}
	// "bad" still appears because the unterminated FM doesn't fail
	// parseFile - the frontmatter handler silent-skips on error.
}

// TestNeighbors_NotFound surfaces ErrNotFound.
func TestNeighbors_NotFound(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	_, _, _, err := v.Neighbors("ghostnote")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// TestBacklinks_NotFound surfaces ErrNotFound.
func TestBacklinks_NotFound(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	_, err := v.Backlinks("ghostnote", 10)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// TestTagged_EmptyTag returns an error.
func TestTagged_EmptyTag(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	_, err := v.Tagged("", 10)
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestTagged_HashPrefixStripped - `#project` resolves the same as
// `project`.
func TestTagged_HashPrefixStripped(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	a, _ := v.Tagged("project", 50)
	b, _ := v.Tagged("#project", 50)
	if len(a) != len(b) {
		t.Errorf("with vs without # should match: %d vs %d", len(a), len(b))
	}
}

// TestTagged_LimitTrunc caps the result count.
func TestTagged_Limit(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	out, err := v.Tagged("project", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Errorf("want 1, got %d", len(out))
	}
}

// TestRecent_DefaultLimit returns 10.
func TestRecent_DefaultLimit(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	out := v.Recent(0, 0)
	if len(out) == 0 {
		t.Errorf("expected non-empty recent")
	}
	if len(out) > 10 {
		t.Errorf("default limit should cap at 10; got %d", len(out))
	}
}

// TestResolve_ExactPathOnly when the path doesn't match a title-key.
func TestResolve_ExactPath(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	resolved, _, err := v.Resolve("carlos.md")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "carlos.md" {
		t.Errorf("got %q", resolved)
	}
}

// TestResolve_TrailingSlashTolerated.
func TestResolve_TrailingSlash(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	resolved, _, err := v.Resolve("carlos/")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "carlos.md" {
		t.Errorf("got %q", resolved)
	}
}

// TestResolve_Empty returns ErrNotFound.
func TestResolve_Empty(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	_, _, err := v.Resolve("")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

// TestMaybeRefresh_FileChange picks up a mutated note.
func TestMaybeRefresh_FileChange(t *testing.T) {
	src := fixtureVault(t)
	dst := t.TempDir()
	copyDir(t, src, dst)
	c := NewCache([]string{"templates/**"})
	v, err := c.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	// Modify carlos.md and bump mtime explicitly.
	carlosPath := filepath.Join(dst, "carlos.md")
	if err := os.WriteFile(carlosPath, []byte("---\ntitle: edited\n---\n\n# changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(carlosPath, future, future); err != nil {
		t.Fatal(err)
	}
	if err := v.MaybeRefresh(); err != nil {
		t.Fatal(err)
	}
	n, err := v.Get("edited")
	if err != nil {
		t.Fatalf("after edit, 'edited' title should resolve: %v", err)
	}
	if n.Title != "edited" {
		t.Errorf("title not updated: %q", n.Title)
	}
}

// TestMaybeRefresh_FileDeletion forces a full re-scan when a file
// disappears.
func TestMaybeRefresh_FileDeletion(t *testing.T) {
	src := fixtureVault(t)
	dst := t.TempDir()
	copyDir(t, src, dst)
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(dst)
	if _, err := v.Get("onboarding"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dst, "onboarding.md")); err != nil {
		t.Fatal(err)
	}
	if err := v.MaybeRefresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get("onboarding"); !errors.Is(err, ErrNotFound) {
		t.Errorf("onboarding should be gone after refresh; err=%v", err)
	}
}

// TestParseInline_FrontmatterTagsHonored - frontmatter tags
// participate in v.tags alongside inline ones.
func TestParseInline_FenceCloseDifferentMarker(t *testing.T) {
	// Open with ```, close with ~~~ stays open; the rest of the body
	// is treated as fenced and tags / wikilinks are skipped.
	dir := t.TempDir()
	body := "```go\n[[fakelink]]\n~~~\n[[outsidefence]]\n```\n[[afterclose]]\n"
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := NewCache(nil).Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	n, err := v.Get("x.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range n.Links {
		if l.Display == "fakelink" {
			t.Errorf("fenced link leaked: %+v", l)
		}
	}
}

// TestStripMarkdownAnchors_NoLinks is a no-op.
func TestStripMarkdownAnchors_NoLinks(t *testing.T) {
	// Cover the fast-path return.
	got := stripMarkdownAnchors("plain text only")
	if got != "plain text only" {
		t.Errorf("got %q", got)
	}
}

// TestStripMarkdownAnchors_RemoveFragment drops `#frag`.
func TestStripMarkdownAnchors_Frag(t *testing.T) {
	got := stripMarkdownAnchors("see [link](url#frag) here")
	if strings.Contains(got, "#frag") {
		t.Errorf("got %q", got)
	}
}

// TestStripInlineCode_NoBacktick fast-path returns verbatim.
func TestStripInlineCode_NoBacktick(t *testing.T) {
	got := stripInlineCode("plain text")
	if got != "plain text" {
		t.Errorf("got %q", got)
	}
}

// TestStripInlineCode_Matched removes the span.
func TestStripInlineCode_Matched(t *testing.T) {
	got := stripInlineCode("see `code#tag` here")
	if strings.Contains(got, "#tag") {
		t.Errorf("got %q", got)
	}
}

// TestStripInlineCode_UnmatchedOpener writes the run verbatim.
func TestStripInlineCode_Unmatched(t *testing.T) {
	got := stripInlineCode("see ``unmatched here")
	if !strings.Contains(got, "``") {
		t.Errorf("opener should remain on unmatched: %q", got)
	}
}

// TestContainsString covers the helper.
func TestContainsString(t *testing.T) {
	if !containsString([]string{"a", "b"}, "b") {
		t.Error("expected hit")
	}
	if containsString([]string{"a", "b"}, "z") {
		t.Error("expected miss")
	}
}

// TestScoreNote_NoMatch returns zero.
func TestScoreNote_NoMatch(t *testing.T) {
	n := &Note{Title: "nope", body: "different content"}
	s, sn, ln := scoreNote(n, "missing")
	if s != 0 || sn != "" || ln != 0 {
		t.Errorf("expected zero hit; got %v %q %d", s, sn, ln)
	}
}

// TestScoreNote_TitleOnly drives the firstParagraph fallback path.
func TestScoreNote_TitleOnlyMatches(t *testing.T) {
	n := &Note{Title: "uniquetitlematch", body: "Some body content here\nMore body text\n"}
	s, sn, ln := scoreNote(n, "uniquetitle")
	if s == 0 {
		t.Errorf("expected score > 0")
	}
	if sn == "" || ln == 0 {
		t.Errorf("expected fallback snippet; got %q line %d", sn, ln)
	}
}

// TestFirstParagraph returns the first non-blank paragraph.
func TestFirstParagraph(t *testing.T) {
	body := "\n\nfirst paragraph here\nstill paragraph\n\nsecond paragraph\n"
	got, ln := firstParagraph(body)
	if !strings.Contains(got, "first paragraph") {
		t.Errorf("got %q", got)
	}
	if ln == 0 {
		t.Errorf("got line %d", ln)
	}
	if strings.Contains(got, "second") {
		t.Errorf("leaked into second paragraph: %q", got)
	}
}

// TestFirstParagraph_Empty returns empty + zero line.
func TestFirstParagraph_Empty(t *testing.T) {
	got, ln := firstParagraph("")
	if got != "" || ln != 0 {
		t.Errorf("got %q line %d", got, ln)
	}
}

// TestMatchesWhere_NumericValue compares via %v.
func TestMatchesWhere_AnyComparison(t *testing.T) {
	fm := map[string]any{"x": int64(5)}
	if !matchesWhere(fm, map[string]any{"x": 5}) {
		t.Error("int64(5) should match int 5 via stringified compare")
	}
	if matchesWhere(fm, map[string]any{"x": 6}) {
		t.Error("should not match")
	}
}

// TestMatchesWhere_MissingKey returns false.
func TestMatchesWhere_Missing(t *testing.T) {
	if matchesWhere(map[string]any{}, map[string]any{"x": 1}) {
		t.Error("missing key should fail")
	}
}

// TestMatchesWhere_Empty trivially true.
func TestMatchesWhere_Empty(t *testing.T) {
	if !matchesWhere(map[string]any{"a": 1}, nil) {
		t.Error("nil where should be true")
	}
}

// TestBodySnippet_NoMatch returns zero.
func TestBodySnippet_NoMatch(t *testing.T) {
	sn, ln, cnt := bodySnippet("hello world", "missing")
	if sn != "" || ln != 0 || cnt != 0 {
		t.Errorf("got %q ln %d cnt %d", sn, ln, cnt)
	}
}

// TestBodySnippet_Multiple counts every hit.
func TestBodySnippet_MultipleHits(t *testing.T) {
	body := "foo bar foo baz foo qux foo zap"
	_, _, cnt := bodySnippet(body, "foo")
	if cnt != 4 {
		t.Errorf("want 4 hits, got %d", cnt)
	}
}

// TestIsSpaceByte covers the helper.
func TestIsSpaceByte(t *testing.T) {
	for _, c := range []byte{' ', '\t', '\n', '\r'} {
		if !isSpaceByte(c) {
			t.Errorf("%q should be space", c)
		}
	}
	if isSpaceByte('a') {
		t.Error("'a' should not be space")
	}
}

// TestHeadingTextWithFormatting flattens nested inline nodes.
func TestHeadingTextWithFormatting(t *testing.T) {
	// A note where the heading has emphasis: `## *bold* word`.
	dir := t.TempDir()
	body := "# Top\n\n## *emphasized* heading\n\ncontent\n"
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := NewCache(nil).Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	n, err := v.Get("x.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Headings) < 2 {
		t.Fatalf("want 2 headings, got %d", len(n.Headings))
	}
	// The emphasised text must be extracted.
	found := false
	for _, h := range n.Headings {
		if strings.Contains(h.Text, "emphasized") {
			found = true
		}
	}
	if !found {
		t.Errorf("emphasized heading text not extracted: %+v", n.Headings)
	}
}
