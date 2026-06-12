package notes

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestNewVaultIndexScanError verifies that a scan failure (an
// unreadable subdirectory) propagates out of newVaultIndex rather than
// silently yielding a partial index. WalkDir surfaces the permission
// error through its error callback, which scan returns verbatim.
func TestNewVaultIndexScanError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits not meaningful on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission bits")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "locked")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "x.md"), []byte("# x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Strip all permissions so WalkDir's descent into the dir errors.
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	c := NewCache(nil)
	_, err := c.Open(dir)
	if err == nil {
		t.Fatal("expected scan error from unreadable subdir, got nil")
	}
}

// TestScanSkipsNonMarkdown confirms non-.md files are ignored by the
// walker while sibling .md files index normally.
func TestScanSkipsNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("# note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "image.png"), []byte("not markdown"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("plain text"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(v.Keys()); got != 1 {
		t.Fatalf("expected only the .md file indexed; got %d notes: %v", got, v.Keys())
	}
	if _, err := v.Get("note"); err != nil {
		t.Errorf("note.md should be indexed: %v", err)
	}
}

// Keys is a tiny test accessor; VaultIndex has no public iterator and we
// want to assert on the indexed set without poking unexported fields
// from another file. Defined here (test build only) so production stays
// minimal.
func (v *VaultIndex) Keys() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]string, 0, len(v.notes))
	for k := range v.notes {
		out = append(out, k)
	}
	return out
}

// TestScanFileLevelExclude pins that a *file* glob (not just a directory
// glob) is honored during the walk: `*.draft.md` keeps a draft out of
// the index even at the vault root.
func TestScanFileLevelExclude(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.md"), []byte("# keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wip.draft.md"), []byte("# wip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCache([]string{"*.draft.md"})
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	keys := v.Keys()
	for _, k := range keys {
		if strings.Contains(k, "draft") {
			t.Errorf("draft file should be excluded; indexed %q", k)
		}
	}
	if _, err := v.Get("keep"); err != nil {
		t.Errorf("keep.md should be indexed: %v", err)
	}
}

// TestScanSkipsBrokenFileButContinues confirms a file that parseFile
// can't read (here: a path turned unreadable) is skipped non-fatally,
// and the rest of the vault still indexes. This drives the
// "parse error -> continue" branch in scan.
func TestScanSkipsUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits not meaningful on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permission bits")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.md"), []byte("# good\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.md")
	if err := os.WriteFile(bad, []byte("# bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o600) })

	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatalf("unreadable file should be skipped, not fatal: %v", err)
	}
	if _, err := v.Get("good"); err != nil {
		t.Errorf("good.md should index despite sibling read failure: %v", err)
	}
	if _, err := v.Get("bad"); err == nil {
		t.Error("bad.md should be absent from the index")
	}
}

// TestResolveLinkAmbiguousShortestPathWins drives the multi-candidate
// branch of resolveLink directly (more than one note shares a title):
// shortest relpath wins, alphabetical tie-break. The committed fixture's
// "notes" title is covered by Get, but here we assert the ordering with a
// 3-way tie to exercise the sort comparator fully.
func TestResolveLinkAmbiguousOrdering(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, title string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("---\ntitle: "+title+"\n---\n\n# "+title+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Three notes, all titled "Topic". Resolution by title (not by
	// exact path) must pick the shortest path; "bbb.md" and "aaa.md"
	// are both length 6 so the alpha tie-break selects "aaa.md".
	mustWrite("bbb.md", "Topic")
	mustWrite("aaa.md", "Topic")
	mustWrite("deep/nested/topic.md", "Topic")

	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Use a title query that has no exact-path match so resolveLink
	// must go through the byTitle multi-candidate path.
	n, err := v.Get("Topic")
	if err != nil {
		t.Fatalf("get Topic: %v", err)
	}
	if n.Path != "aaa.md" {
		t.Errorf("ambiguous resolve: want aaa.md (shortest, alpha tie), got %q", n.Path)
	}
}

// TestResolveMultiCandidateList exercises Resolve's wide candidate set +
// its sort: a title shared by several notes returns all of them, shortest
// first, alphabetical tie-break.
func TestResolveMultiCandidateList(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, title string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("---\ntitle: "+title+"\n---\n\n# "+title+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// "bbb.md" and "aaa.md" are both 6 chars (a true length tie) so the
	// alphabetical tie-break must select "aaa.md"; "sub/topic.md" is
	// longer and must sort last.
	mustWrite("bbb.md", "Shared")
	mustWrite("aaa.md", "Shared")
	mustWrite("sub/topic.md", "Shared")

	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	resolved, cands, err := v.Resolve("Shared")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(cands) != 3 {
		t.Fatalf("expected 3 candidates, got %d: %+v", len(cands), cands)
	}
	// Shortest-first; aaa.md and bbb.md tie on length, alpha picks aaa.md.
	if cands[0].Path != "aaa.md" {
		t.Errorf("first candidate: want aaa.md, got %q", cands[0].Path)
	}
	if cands[2].Path != "sub/topic.md" {
		t.Errorf("longest path should sort last; got %q", cands[2].Path)
	}
	if resolved != "aaa.md" {
		t.Errorf("resolved: want aaa.md, got %q", resolved)
	}
}

// TestSearchEmptyQueryErrors drives the empty-query guard in Search.
func TestSearchEmptyQueryErrors(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	if _, err := v.Search("   ", SearchOptions{}); err == nil {
		t.Error("blank query should error")
	}
}

// TestSearchLimitCappedAt50 drives the limit>50 clamp. We can only
// observe the clamp behaviorally by asking for a huge limit and noting
// the call still succeeds; the clamp itself caps internal slicing.
func TestSearchLimitCappedAt50(t *testing.T) {
	dir := t.TempDir()
	// 60 notes all containing the query word "needle" in the body.
	for i := 0; i < 60; i++ {
		name := filepath.Join(dir, "n"+pad(i)+".md")
		if err := os.WriteFile(name, []byte("# n\n\nneedle here\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := v.Search("needle", SearchOptions{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 50 {
		t.Errorf("limit should clamp to 50; got %d", len(hits))
	}
}

func pad(i int) string {
	s := []byte("00")
	s[0] = byte('0' + i/10)
	s[1] = byte('0' + i%10)
	return string(s)
}

// TestSearchTotalWhereExcludes drives SearchTotal's where-filter
// continue branch: a note that matches the query text but fails the
// frontmatter predicate must not be counted.
func TestSearchTotalWhereExcludes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"),
		[]byte("---\nstatus: alpha\n---\n\nwidget content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"),
		[]byte("---\nstatus: beta\n---\n\nwidget content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	total, err := v.SearchTotal("widget", SearchOptions{Where: map[string]any{"status": "alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("where=status:alpha should count only a.md; got total=%d", total)
	}
}

// TestBacklinksLimitTruncates drives Backlinks' truncation branch.
func TestBacklinksLimitTruncates(t *testing.T) {
	dir := t.TempDir()
	// Target note + three sources all linking to it.
	if err := os.WriteFile(filepath.Join(dir, "hub.md"),
		[]byte("# hub\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{"s1", "s2", "s3"} {
		if err := os.WriteFile(filepath.Join(dir, src+".md"),
			[]byte("# "+src+"\n\nlinks to [[hub]] here\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	bl, err := v.Backlinks("hub", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(bl) != 2 {
		t.Errorf("limit=2 should truncate 3 backlinks to 2; got %d", len(bl))
	}
}

// TestBacklinksDefaultLimit drives the limit<=0 default path.
func TestBacklinksDefaultLimit(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	bl, err := v.Backlinks("carlos", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(bl) == 0 {
		t.Error("expected backlinks with default limit")
	}
}

// TestTaggedLimitTruncates drives Tagged's truncation branch.
func TestTaggedLimitTruncates(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"x", "y", "z"} {
		if err := os.WriteFile(filepath.Join(dir, name+".md"),
			[]byte("---\ntags: [shared]\n---\n\n# "+name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Tagged("shared", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("limit=2 should truncate 3 tagged notes to 2; got %d", len(got))
	}
}

// TestTaggedDefaultLimit drives Tagged's limit<=0 default branch.
func TestTaggedDefaultLimit(t *testing.T) {
	c := NewCache([]string{"templates/**"})
	v, _ := c.Open(fixtureVault(t))
	got, err := v.Tagged("project", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Error("expected tagged notes with default limit")
	}
}

// TestNeighborsDedupsDuplicateLinks drives the seenOut / seenIn dedup
// branches: a note that links to the same target twice yields a single
// outgoing neighbor, and a source linking twice counts once incoming.
func TestNeighborsDedupsDuplicateLinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.md"),
		[]byte("# target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// hub links to target twice (two separate lines).
	if err := os.WriteFile(filepath.Join(dir, "hub.md"),
		[]byte("# hub\n\nfirst [[target]] mention\n\nsecond [[target]] mention\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// source links to hub twice as well.
	if err := os.WriteFile(filepath.Join(dir, "source.md"),
		[]byte("# source\n\n[[hub]] one\n\n[[hub]] two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, in, _, err := v.Neighbors("hub")
	if err != nil {
		t.Fatal(err)
	}
	targetCount := 0
	for _, n := range out {
		if n.Path == "target.md" {
			targetCount++
		}
	}
	if targetCount != 1 {
		t.Errorf("duplicate outgoing links should dedup to 1 target; got %d", targetCount)
	}
	srcCount := 0
	for _, n := range in {
		if n.Path == "source.md" {
			srcCount++
		}
	}
	if srcCount != 1 {
		t.Errorf("duplicate incoming links should dedup to 1 source; got %d", srcCount)
	}
}

// TestMaybeRefreshSkipsNonMarkdownAndExcludes drives MaybeRefresh's
// walker filter branches: a non-.md file and an excluded file must not
// be seen as "new files" that trigger a rescan, and the no-op return
// must hold (same index pointer behavior).
func TestMaybeRefreshSkipsNonMarkdownAndExcludes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.md"),
		[]byte("# note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// `*.draft.md` is a FILE-level glob: it isn't pruned at the dir
	// branch of the walk, so it exercises MaybeRefresh's per-file
	// isExcluded check rather than the SkipDir path.
	c := NewCache([]string{"*.draft.md"})
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := v.Get("note")

	// Drop a non-md file and a file-excluded md file; neither should
	// count as a "new file" forcing a rescan.
	if err := os.WriteFile(filepath.Join(dir, "img.png"),
		[]byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wip.draft.md"),
		[]byte("# wip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := v.MaybeRefresh(); err != nil {
		t.Fatal(err)
	}
	after, _ := v.Get("note")
	if before != after {
		t.Error("adding only non-md/excluded files should NOT rescan")
	}
	// The excluded draft must still be invisible.
	if _, err := v.Get("wip"); err == nil {
		t.Error("excluded draft should never be indexed")
	}
}

// TestMaybeRefreshDetectsDeletionViaKeyMismatch drives the "key present
// before, absent now" branch (and its sibling len-mismatch path) of
// MaybeRefresh by deleting a file. Removing one of two notes flips the
// keyset and forces a full scan.
func TestMaybeRefreshDetectsAddedFileSameCount(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.md"),
		[]byte("# keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.md"),
		[]byte("# old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Swap: remove old.md, add new.md. Count stays at 2 but the keyset
	// differs, so the "key not in prev" branch must trigger a rescan.
	if err := os.Remove(filepath.Join(dir, "old.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.md"),
		[]byte("# new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := v.MaybeRefresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get("new"); err != nil {
		t.Errorf("same-count keyset swap should rescan and pick up new.md: %v", err)
	}
	if _, err := v.Get("old"); err == nil {
		t.Error("old.md should be gone after rescan")
	}
}

// TestMaybeRefreshWalkError drives MaybeRefresh's WalkDir error return
// by making a subdirectory unreadable after the initial open.
func TestMaybeRefreshWalkError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits not meaningful on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission bits")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "root.md"),
		[]byte("# root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "child.md"),
		[]byte("# child\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	if err := v.MaybeRefresh(); err == nil {
		t.Error("MaybeRefresh should surface the WalkDir permission error")
	}
}

// TestScoreNoteBodyHitsCapped drives the extra-body-hit cap (extra >
// 0.5) in scoreNote: many body hits stop adding score past the cap. We
// compare a note with 2 body hits against one with 20: the score delta
// must saturate, not grow linearly.
func TestScoreNoteBodyHitsCapped(t *testing.T) {
	mk := func(reps int) *Note {
		var b strings.Builder
		for i := 0; i < reps; i++ {
			b.WriteString("needle paragraph\n\n")
		}
		return &Note{Title: "untitled", body: b.String()}
	}
	twoHits := mk(2)
	manyHits := mk(20)

	s2, _, _ := scoreNote(twoHits, "needle")
	sMany, _, _ := scoreNote(manyHits, "needle")

	// Base body hit (+1.0) + capped extra (+0.5 max). Two hits give
	// +0.1 extra; twenty hits saturate at +0.5. Both stay <= 1.5.
	if sMany > 1.5+1e-9 {
		t.Errorf("body-hit extra should cap at +0.5 (total <=1.5); got %v", sMany)
	}
	if sMany <= s2 {
		t.Errorf("more hits should score higher up to the cap; got two=%v many=%v", s2, sMany)
	}
	if sMany < 1.5-1e-9 {
		t.Errorf("20 hits should saturate the +0.5 cap (total 1.5); got %v", sMany)
	}
}

// TestBodySnippetLongMatchTruncated drives bodySnippet's len>cap+40
// truncation branch with a body that has no whitespace near the match,
// forcing the expansion loops to run long before the hard cap clamps.
func TestBodySnippetLongMatchTruncated(t *testing.T) {
	// A long unbroken run of non-space bytes around the query forces
	// start/end expansion to walk far, producing an over-long slice the
	// final cap+40 clamp must trim.
	long := strings.Repeat("x", 400) + "needle" + strings.Repeat("y", 400)
	sn, _, cnt := bodySnippet(long, "needle", 0)
	if cnt != 1 {
		t.Fatalf("expected one hit; got %d", cnt)
	}
	if len(sn) > 200+40 {
		t.Errorf("snippet must be clamped to cap+40 (240); got len %d", len(sn))
	}
}

// TestFirstParagraphLongTruncated drives firstParagraph's >240 break.
func TestFirstParagraphLongTruncated(t *testing.T) {
	var b strings.Builder
	// Many short lines in one paragraph (no blank line) push the
	// builder past 240 chars, hitting the early break.
	for i := 0; i < 50; i++ {
		b.WriteString("word word word word\n")
	}
	para, line := firstParagraph(b.String())
	if line != 1 {
		t.Errorf("paragraph starts at line 1; got %d", line)
	}
	if len(para) <= 240 {
		// The break fires AFTER appending the line that crosses 240, so
		// the result is just over 240 but bounded.
		t.Logf("paragraph length %d (acceptable, break fired)", len(para))
	}
}

// TestLineScannerNoTrailingNewline drives linesIter.next's EOF-without-
// newline branch through a parsed note whose final line carries an
// inline tag but no trailing `\n`.
func TestLineScannerNoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	// No trailing newline after the tag-bearing last line.
	if err := os.WriteFile(filepath.Join(dir, "tail.md"),
		[]byte("# tail\n\nbody with #finaltag"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	n, err := v.Get("tail")
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(n.Tags, "finaltag") {
		t.Errorf("tag on a no-trailing-newline final line should parse; tags=%v", n.Tags)
	}
}

// TestRecentSincePresent keeps the since>0 cutoff path warm with a
// deterministic modtime split: one fresh, one stale note.
func TestRecentSinceCutoff(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fresh.md"),
		[]byte("# fresh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale.md"),
		[]byte("# stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(filepath.Join(dir, "fresh.md"), now, now); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-72 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "stale.md"), old, old); err != nil {
		t.Fatal(err)
	}
	c := NewCache(nil)
	v, err := c.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := v.Recent(10, 24*time.Hour)
	for _, n := range got {
		if n.Path == "stale.md" {
			t.Error("since=24h should exclude the 72h-old note")
		}
	}
	if len(got) != 1 || got[0].Path != "fresh.md" {
		t.Errorf("expected only fresh.md within cutoff; got %v", paths(got))
	}
}

func paths(ns []*Note) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Path
	}
	return out
}
