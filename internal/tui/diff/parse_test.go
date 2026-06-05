package diff

import (
	"os"
	"path/filepath"
	"testing"
)

func mustReadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join("testdata", name)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return data
}

func TestParseUnified_empty(t *testing.T) {
	pd := parseUnified(nil)
	if len(pd.files) != 0 {
		t.Fatalf("expected 0 files for nil input, got %d", len(pd.files))
	}
}

func TestParseUnified_simple(t *testing.T) {
	pd := parseUnified(mustReadTestdata(t, "simple.diff"))
	if len(pd.files) != 1 {
		t.Fatalf("want 1 file, got %d", len(pd.files))
	}
	f := pd.files[0]
	if f.oldPath != "a/hello.go" {
		t.Errorf("oldPath = %q, want a/hello.go", f.oldPath)
	}
	if f.newPath != "b/hello.go" {
		t.Errorf("newPath = %q, want b/hello.go", f.newPath)
	}
	if len(f.hunks) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(f.hunks))
	}
	h := f.hunks[0]
	if h.oldStart != 1 || h.newStart != 1 {
		t.Errorf("hunk starts (%d,%d), want (1,1)", h.oldStart, h.newStart)
	}
	if len(h.lines) == 0 {
		t.Fatal("hunk has no body lines")
	}
}

func TestParseUnified_multiHunk(t *testing.T) {
	pd := parseUnified(mustReadTestdata(t, "multi_hunk.diff"))
	if len(pd.files) != 2 {
		t.Fatalf("want 2 files, got %d", len(pd.files))
	}
	if len(pd.files[0].hunks) != 2 {
		t.Errorf("foo.go: want 2 hunks, got %d", len(pd.files[0].hunks))
	}
	if len(pd.files[1].hunks) != 1 {
		t.Errorf("bar.go: want 1 hunk, got %d", len(pd.files[1].hunks))
	}
}

func TestParseUnified_newFile_devNull(t *testing.T) {
	pd := parseUnified(mustReadTestdata(t, "new_file.diff"))
	if len(pd.files) != 1 {
		t.Fatalf("want 1 file, got %d", len(pd.files))
	}
	if pd.files[0].oldPath != "/dev/null" {
		t.Errorf("oldPath = %q, want /dev/null", pd.files[0].oldPath)
	}
	if pd.files[0].newPath != "b/created.txt" {
		t.Errorf("newPath = %q, want b/created.txt", pd.files[0].newPath)
	}
}

func TestParseUnified_deletedFile_devNull(t *testing.T) {
	pd := parseUnified(mustReadTestdata(t, "deleted_file.diff"))
	if len(pd.files) != 1 {
		t.Fatalf("want 1 file, got %d", len(pd.files))
	}
	if pd.files[0].newPath != "/dev/null" {
		t.Errorf("newPath = %q, want /dev/null", pd.files[0].newPath)
	}
	if preferredPath(pd.files[0]) != "a/gone.txt" {
		t.Errorf("preferredPath = %q, want a/gone.txt", preferredPath(pd.files[0]))
	}
}

func TestParseHunkRange(t *testing.T) {
	cases := []struct {
		in           string
		wantO, wantN int
	}{
		{"@@ -1,3 +1,4 @@", 1, 1},
		{"@@ -10,2 +12,5 @@ context", 10, 12},
		{"@@ -1 +1 @@", 1, 1},
		{"@@ -0,0 +1,5 @@", 0, 1},
		{"malformed", 0, 0},
	}
	for _, tc := range cases {
		o, n := parseHunkRange(tc.in)
		if o != tc.wantO || n != tc.wantN {
			t.Errorf("parseHunkRange(%q) = (%d,%d), want (%d,%d)", tc.in, o, n, tc.wantO, tc.wantN)
		}
	}
}

func TestParseUnified_bareDiff_noGitHeader(t *testing.T) {
	// A `diff -u` output that lacks the `diff --git` line should still
	// parse into one file with one hunk.
	raw := []byte("--- a/x.txt\n+++ b/x.txt\n@@ -1,1 +1,1 @@\n-old\n+new\n")
	pd := parseUnified(raw)
	if len(pd.files) != 1 {
		t.Fatalf("want 1 file, got %d", len(pd.files))
	}
	if len(pd.files[0].hunks) != 1 {
		t.Errorf("want 1 hunk, got %d", len(pd.files[0].hunks))
	}
}

func TestParseUnified_noNewlineMarker(t *testing.T) {
	pd := parseUnified(mustReadTestdata(t, "no_newline.diff"))
	if len(pd.files) != 1 || len(pd.files[0].hunks) != 1 {
		t.Fatalf("expected 1 file 1 hunk")
	}
	// Body should contain both '-' and '+' lines plus two `\ No newline...` markers.
	var sawMarker int
	for _, l := range pd.files[0].hunks[0].lines {
		if len(l) > 0 && l[0] == '\\' {
			sawMarker++
		}
	}
	if sawMarker != 2 {
		t.Errorf("expected 2 no-newline markers, got %d", sawMarker)
	}
}
