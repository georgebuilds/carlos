package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBundleSkill_requiresFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "naked.md")
	if err := os.WriteFile(path, []byte("# no frontmatter here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundleSkill(path); err == nil {
		t.Error("LoadBundleSkill should reject a file with no frontmatter")
	}
}

func TestLoadBundleSkill_parsesFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "caldav.md")
	body := `---
name: calendar-caldav
description: speak CalDAV
backend: caldav
frame_default: work
frames: [work, research]
---

# body
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadBundleSkill(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "calendar-caldav" {
		t.Errorf("Name = %q", s.Name)
	}
	if s.Backend != "caldav" {
		t.Errorf("Backend = %q", s.Backend)
	}
	if s.FrameDefault != "work" {
		t.Errorf("FrameDefault = %q", s.FrameDefault)
	}
	if len(s.Frames) != 2 || s.Frames[0] != "work" {
		t.Errorf("Frames = %v", s.Frames)
	}
}

func TestLoadLibrary_bundleDirectory(t *testing.T) {
	root := t.TempDir()
	calDir := filepath.Join(root, "calendar")
	if err := os.MkdirAll(calDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"INDEX.md":    "name: calendar\ndescription: entry point\n",
		"caldav.md":   "name: calendar-caldav\ndescription: speak caldav\nbackend: caldav\n",
		"ics-file.md": "name: calendar-ics-file\ndescription: read/write ics\nbackend: ics\n",
	}
	for n, fm := range files {
		body := "---\n" + fm + "---\n\n# body\n"
		if err := os.WriteFile(filepath.Join(calDir, n), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lib, err := LoadLibrary([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Active) != 3 {
		t.Fatalf("Active = %d, want 3", len(lib.Active))
	}
	if lib.ByName("calendar") == nil {
		t.Error("INDEX.md (name=calendar) did not load")
	}
	if lib.ByName("calendar-caldav") == nil {
		t.Error("caldav.md did not load")
	}
}

func TestLoadLibrary_singleAndBundleMix(t *testing.T) {
	root := t.TempDir()
	// Single skill (SKILL.md layout)
	singleDir := filepath.Join(root, "loneranger")
	if err := os.MkdirAll(singleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(singleDir, "SKILL.md"),
		[]byte("---\nname: lone\ndescription: lonely skill\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Bundle skill (no SKILL.md, two .md files)
	bundleDir := filepath.Join(root, "twins")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"alpha.md", "beta.md"} {
		body := "---\nname: twins-" + n[:len(n)-3] + "\ndescription: x\n---\n"
		if err := os.WriteFile(filepath.Join(bundleDir, n), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lib, err := LoadLibrary([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"lone", "twins-alpha", "twins-beta"} {
		if lib.ByName(want) == nil {
			t.Errorf("missing skill %q", want)
		}
	}
}

func TestLibrary_ForFrame(t *testing.T) {
	lib := &Library{Active: []*Skill{
		{Name: "everywhere", Description: "x"},
		{Name: "work-only", Description: "x", Frames: []string{"work"}},
		{Name: "p-and-w", Description: "x", Frames: []string{"personal", "work"}},
	}}
	got := lib.ForFrame("personal")
	if len(got) != 2 {
		t.Errorf("personal got %d skills, want 2 (everywhere + p-and-w)", len(got))
	}
	got = lib.ForFrame("work")
	if len(got) != 3 {
		t.Errorf("work got %d skills, want 3", len(got))
	}
}

func TestLibrary_PickBackend(t *testing.T) {
	lib := &Library{Active: []*Skill{
		{Name: "calendar", Description: "index"},
		{Name: "calendar-ics-file", Description: "ics", Backend: "ics"},
		{Name: "calendar-caldav", Description: "caldav", Backend: "caldav"},
	}}
	if s := lib.PickBackend("calendar", "caldav"); s == nil || s.Name != "calendar-caldav" {
		t.Errorf("PickBackend(calendar, caldav) wrong: %v", s)
	}
	if s := lib.PickBackend("calendar", "missing"); s != nil {
		t.Errorf("PickBackend(calendar, missing) should be nil; got %v", s)
	}
}

func TestLibrary_PickBackend_NameMatchFallback(t *testing.T) {
	lib := &Library{Active: []*Skill{
		// No Backend frontmatter field; name prefix is the only signal.
		{Name: "calendar-ics", Description: "ics"},
	}}
	if s := lib.PickBackend("calendar", "ics"); s == nil || s.Name != "calendar-ics" {
		t.Errorf("name-prefix fallback failed: %v", s)
	}
}

func TestLoadLibrary_LoadsShippedCalendarBundle(t *testing.T) {
	// Pointer into the repo's shipped bundle. Skips when running from a
	// sandboxed environment that doesn't have access (e.g. CI tarball).
	root := findRepoRoot(t)
	if root == "" {
		t.Skip("repo root not found; running outside the checkout?")
	}
	calRoot := filepath.Join(root, "skills")
	if _, err := os.Stat(filepath.Join(calRoot, "calendar", "INDEX.md")); err != nil {
		t.Skipf("shipped calendar bundle missing: %v", err)
	}
	lib, err := LoadLibrary([]string{calRoot})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{
		"calendar", "calendar-ics-file", "calendar-caldav",
		"calendar-apple", "calendar-mcp", "calendar-cross-frame-view",
	} {
		if lib.ByName(n) == nil {
			t.Errorf("shipped bundle missing skill %q", n)
		}
	}
	if s := lib.PickBackend("calendar", "caldav"); s == nil {
		t.Error("PickBackend(calendar, caldav) returned nil on shipped bundle")
	}
}

// findRepoRoot walks up from cwd looking for a go.mod. Returns "" when
// none found.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for dir := wd; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
	}
	return ""
}
