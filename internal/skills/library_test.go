package skills_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/skills"
)

// TestLibrary_LoadLibraryEmpty: non-existent roots are silently skipped.
func TestLibrary_LoadLibraryEmpty(t *testing.T) {
	lib, err := skills.LoadLibrary([]string{
		filepath.Join(t.TempDir(), "does-not-exist"),
		"",
	})
	if err != nil {
		t.Fatalf("LoadLibrary: %v", err)
	}
	if len(lib.Active) != 0 {
		t.Errorf("want 0 skills, got %d", len(lib.Active))
	}
}

// TestLibrary_LoadLibraryHappyPath: two skills in one root load cleanly.
func TestLibrary_LoadLibraryHappyPath(t *testing.T) {
	root := t.TempDir()
	writeSkillFixture(t, root, "alpha", "Use when alpha")
	writeSkillFixture(t, root, "beta", "Use when beta")

	lib, err := skills.LoadLibrary([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Active) != 2 {
		t.Fatalf("want 2 skills, got %d", len(lib.Active))
	}
	if lib.ByName("alpha") == nil || lib.ByName("beta") == nil {
		t.Errorf("missing expected skill in: %v", lib.Active)
	}
}

// TestLibrary_ProjectShadowsUser: the project root wins on name
// collision (later in the rootDirs slice).
func TestLibrary_ProjectShadowsUser(t *testing.T) {
	userRoot := t.TempDir()
	projRoot := t.TempDir()
	writeSkillFixture(t, userRoot, "shared", "Use when user-level desc")
	writeSkillFixture(t, projRoot, "shared", "Use when project-level desc")

	lib, err := skills.LoadLibrary([]string{userRoot, projRoot})
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Active) != 1 {
		t.Fatalf("want 1 skill (dedup), got %d", len(lib.Active))
	}
	got := lib.ByName("shared")
	if got == nil {
		t.Fatal("shared not found")
	}
	if got.Description != "Use when project-level desc" {
		t.Errorf("project should shadow user, got desc=%q", got.Description)
	}
}

// TestLibrary_SkipsArchiveAndProposals: _archive and _proposals dirs
// must not be loaded as skills.
func TestLibrary_SkipsArchiveAndProposals(t *testing.T) {
	root := t.TempDir()
	writeSkillFixture(t, root, "real", "Use when real")
	// Place a SKILL.md under _archive — should be skipped.
	writeSkillFixture(t, filepath.Join(root, "_archive"), "old", "Use when old")
	writeSkillFixture(t, filepath.Join(root, "_proposals"), "pending", "Use when pending")

	lib, err := skills.LoadLibrary([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Active) != 1 || lib.Active[0].Name != "real" {
		t.Errorf("want only 'real', got %+v", lib.Active)
	}
}

// TestLibrary_BadSkillSkipped: a malformed SKILL.md must not nuke the
// rest of the library — it's silently skipped.
func TestLibrary_BadSkillSkipped(t *testing.T) {
	root := t.TempDir()
	writeSkillFixture(t, root, "good", "Use when good")
	badDir := filepath.Join(root, "bad")
	_ = os.MkdirAll(badDir, 0o755)
	_ = os.WriteFile(filepath.Join(badDir, "SKILL.md"), []byte("---\nname: bad\n(unterminated)"), 0o600)

	lib, err := skills.LoadLibrary([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Active) != 1 {
		t.Fatalf("want 1 skill, got %d", len(lib.Active))
	}
}

// TestLibrary_Descriptions returns descriptions in load order.
func TestLibrary_Descriptions(t *testing.T) {
	root := t.TempDir()
	writeSkillFixture(t, root, "a-first", "Use when first")
	writeSkillFixture(t, root, "b-second", "Use when second")
	lib, err := skills.LoadLibrary([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	descs := lib.Descriptions()
	if len(descs) != 2 {
		t.Fatalf("want 2 descriptions, got %d", len(descs))
	}
}

// TestLibrary_DefaultSearchPaths: confirms the 5 SPEC paths in order.
func TestLibrary_DefaultSearchPaths(t *testing.T) {
	paths := skills.DefaultSearchPaths("/home/u", "/proj")
	want := []string{
		"/home/u/.claude/skills",
		"/home/u/.agents/skills",
		"/proj/.claude/skills",
		"/proj/.agents/skills",
		"/home/u/.carlos/skills",
	}
	if len(paths) != len(want) {
		t.Fatalf("want %d paths, got %d: %v", len(want), len(paths), paths)
	}
	for i := range paths {
		if paths[i] != want[i] {
			t.Errorf("[%d] want %q got %q", i, want[i], paths[i])
		}
	}
}

// TestLibrary_DefaultSearchPathsNoProject: project paths omitted when
// projectRoot is empty.
func TestLibrary_DefaultSearchPathsNoProject(t *testing.T) {
	paths := skills.DefaultSearchPaths("/home/u", "")
	if len(paths) != 3 {
		t.Errorf("want 3 paths (no project), got %d: %v", len(paths), paths)
	}
}

// TestLibrary_WriteRootClaudeConvention: cfg=claude → .claude/skills/.
func TestLibrary_WriteRootClaudeConvention(t *testing.T) {
	cfg := &config.Config{Skills: config.SkillsConfig{Convention: config.SkillsConventionClaude}}
	r := skills.WriteRoot(cfg, "/h", "/proj")
	if r != "/proj/.claude/skills" {
		t.Errorf("want /proj/.claude/skills, got %q", r)
	}
}

// TestLibrary_WriteRootAgentsConvention: cfg=agents (default) →
// .agents/skills/.
func TestLibrary_WriteRootAgentsConvention(t *testing.T) {
	cfg := &config.Config{Skills: config.SkillsConfig{Convention: config.SkillsConventionAgents}}
	r := skills.WriteRoot(cfg, "/h", "")
	if r != "/h/.agents/skills" {
		t.Errorf("want /h/.agents/skills, got %q", r)
	}
}

// TestLibrary_WriteRootNilCfg: nil cfg uses default (agents).
func TestLibrary_WriteRootNilCfg(t *testing.T) {
	r := skills.WriteRoot(nil, "/h", "")
	if r != "/h/.agents/skills" {
		t.Errorf("want /h/.agents/skills, got %q", r)
	}
}

// TestLibrary_NewLibraryEmpty: NewLibrary returns a usable empty
// library with no roots and no active skills.
func TestLibrary_NewLibraryEmpty(t *testing.T) {
	lib := skills.NewLibrary()
	if lib == nil {
		t.Fatal("NewLibrary returned nil")
	}
	if len(lib.Active) != 0 || len(lib.Roots) != 0 || len(lib.Drafts) != 0 {
		t.Errorf("want empty library, got %+v", lib)
	}
	// ByName on an empty library must not panic and returns nil.
	if lib.ByName("anything") != nil {
		t.Error("ByName on empty library should be nil")
	}
}

// TestLibrary_ByNameNoMatch: ByName returns nil when no skill matches,
// and skips nil entries without panicking.
func TestLibrary_ByNameNoMatch(t *testing.T) {
	lib := &skills.Library{Active: []*skills.Skill{
		nil,
		{Name: "present", Description: "Use when present"},
	}}
	if lib.ByName("absent") != nil {
		t.Error("want nil for absent name")
	}
	if got := lib.ByName("present"); got == nil || got.Name != "present" {
		t.Errorf("want 'present', got %+v", got)
	}
}

// TestLibrary_ForFrame filters by the frames frontmatter list: an empty
// Frames means "every frame"; a non-empty list restricts to membership.
func TestLibrary_ForFrame(t *testing.T) {
	lib := &skills.Library{Active: []*skills.Skill{
		nil, // exercises the nil-skip branch
		{Name: "everywhere", Description: "Use when everywhere"},
		{Name: "research-only", Description: "Use when researching", Frames: []string{"research"}},
		{Name: "build-only", Description: "Use when building", Frames: []string{"build"}},
	}}

	research := lib.ForFrame("research")
	gotNames := map[string]bool{}
	for _, s := range research {
		gotNames[s.Name] = true
	}
	if !gotNames["everywhere"] {
		t.Error("frame-less skill should appear in every frame")
	}
	if !gotNames["research-only"] {
		t.Error("research-only skill should appear in research frame")
	}
	if gotNames["build-only"] {
		t.Error("build-only skill must NOT appear in research frame")
	}
	if len(research) != 2 {
		t.Errorf("want 2 skills for research frame, got %d", len(research))
	}
}

// TestLibrary_ForFrameNilReceiver: ForFrame on a nil *Library returns
// nil rather than panicking (defensive guard).
func TestLibrary_ForFrameNilReceiver(t *testing.T) {
	var lib *skills.Library
	if got := lib.ForFrame("x"); got != nil {
		t.Errorf("want nil for nil receiver, got %v", got)
	}
}

// TestLibrary_PickBackend resolves a capability+backend pair via the
// frontmatter `backend` field and the name-prefix fallback.
func TestLibrary_PickBackend(t *testing.T) {
	lib := &skills.Library{Active: []*skills.Skill{
		nil,
		{Name: "calendar-ics", Description: "Use when ics", Backend: "ics"},
		{Name: "calendar-caldav", Description: "Use when caldav", Backend: "caldav"},
		// A skill whose Backend field is empty but whose name still
		// matches the capability- prefix → exercises the fallback loop.
		{Name: "weather-noaa", Description: "Use when noaa"},
	}}

	if got := lib.PickBackend("calendar", "ics"); got == nil || got.Name != "calendar-ics" {
		t.Errorf("want calendar-ics, got %+v", got)
	}
	if got := lib.PickBackend("calendar", "caldav"); got == nil || got.Name != "calendar-caldav" {
		t.Errorf("want calendar-caldav, got %+v", got)
	}
	// Fallback: name prefix matches even with empty Backend field.
	if got := lib.PickBackend("weather", "noaa"); got == nil || got.Name != "weather-noaa" {
		t.Errorf("want weather-noaa via name fallback, got %+v", got)
	}
	// No match returns nil.
	if got := lib.PickBackend("calendar", "google"); got != nil {
		t.Errorf("want nil for unknown backend, got %+v", got)
	}
}

// TestLibrary_PickBackendGuards: nil receiver and empty args return nil.
func TestLibrary_PickBackendGuards(t *testing.T) {
	var nilLib *skills.Library
	if nilLib.PickBackend("calendar", "ics") != nil {
		t.Error("nil receiver should yield nil")
	}
	lib := &skills.Library{Active: []*skills.Skill{{Name: "calendar-ics", Backend: "ics"}}}
	if lib.PickBackend("", "ics") != nil {
		t.Error("empty capability should yield nil")
	}
	if lib.PickBackend("calendar", "") != nil {
		t.Error("empty backend should yield nil")
	}
}

// TestLibrary_LoadLibraryBundleLayout: a subdirectory with no SKILL.md
// but multiple *.md files (each with its own frontmatter) loads every
// file as a distinct bundle skill (the Phase C-3 capability-bundle
// layout exercised through loadSkillsAt).
func TestLibrary_LoadLibraryBundleLayout(t *testing.T) {
	root := t.TempDir()
	bundleDir := filepath.Join(root, "calendar")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	icsMD := "---\nname: calendar-ics\ndescription: Use when reading an ICS file\nbackend: ics\n---\nbody\n"
	caldavMD := "---\nname: calendar-caldav\ndescription: Use when talking to CalDAV\nbackend: caldav\n---\nbody\n"
	// A stray non-.md file must be ignored by the bundle loader.
	if err := os.WriteFile(filepath.Join(bundleDir, "ics-file.md"), []byte(icsMD), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "caldav.md"), []byte(caldavMD), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "README.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}

	lib, err := skills.LoadLibrary([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Active) != 2 {
		t.Fatalf("want 2 bundle skills, got %d: %+v", len(lib.Active), lib.Active)
	}
	if lib.PickBackend("calendar", "ics") == nil || lib.PickBackend("calendar", "caldav") == nil {
		t.Error("both bundle backends should be resolvable")
	}
}

// TestLibrary_LoadLibraryRootIsFile: a root path that exists but is a
// regular file (not a directory) is silently skipped rather than
// erroring.
func TestLibrary_LoadLibraryRootIsFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	lib, err := skills.LoadLibrary([]string{f})
	if err != nil {
		t.Fatalf("file root should be skipped, got err: %v", err)
	}
	if len(lib.Active) != 0 {
		t.Errorf("want 0 skills, got %d", len(lib.Active))
	}
}

// TestLibrary_LoadLibraryEntryFileSkipped: a top-level regular file
// (not a directory) inside a root is skipped; only subdirectories are
// treated as skills.
func TestLibrary_LoadLibraryEntryFileSkipped(t *testing.T) {
	root := t.TempDir()
	writeSkillFixture(t, root, "real", "Use when real")
	if err := os.WriteFile(filepath.Join(root, "loose.md"), []byte("not a skill dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	lib, err := skills.LoadLibrary([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Active) != 1 || lib.Active[0].Name != "real" {
		t.Errorf("want only 'real', got %+v", lib.Active)
	}
}

// TestLibrary_LoadFromConfig overlays the embedded bundled pack onto a
// disk library rooted at a fake HOME, and verifies a user-installed
// skill of the same name as a bundled one still wins.
func TestLibrary_LoadFromConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A user-installed skill named "calendar" shadows the bundled one.
	userSkillsRoot := filepath.Join(home, ".carlos", "skills")
	writeSkillFixture(t, userSkillsRoot, "calendar", "Use when user-overridden calendar")
	// A unique user skill that should survive the overlay.
	writeSkillFixture(t, userSkillsRoot, "my-unique-skill", "Use when unique")

	cfg := &config.Config{Skills: config.SkillsConfig{Convention: config.SkillsConventionAgents}}
	lib, err := skills.LoadFromConfig(cfg, "")
	if err != nil {
		t.Fatalf("LoadFromConfig: %v", err)
	}
	got := lib.ByName("calendar")
	if got == nil {
		t.Fatal("calendar should be present (either user or bundled)")
	}
	if got.Description != "Use when user-overridden calendar" {
		t.Errorf("user calendar should shadow bundled; got desc=%q", got.Description)
	}
	if lib.ByName("my-unique-skill") == nil {
		t.Error("unique user skill lost after overlay")
	}
	// The overlay should also have pulled in at least one purely-bundled
	// skill (more than just the user's two).
	if len(lib.Active) < 3 {
		t.Errorf("expected bundled overlay to add skills; got %d total", len(lib.Active))
	}
}

// writeSkillFixture creates root/<name>/SKILL.md with the supplied
// description; helper for tests above.
func writeSkillFixture(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	s := &skills.Skill{
		Name:        name,
		Description: desc,
		Provenance:  skills.ProvHandWritten,
		Created:     time.Now().UTC(),
		Updated:     time.Now().UTC(),
		Body:        "body for " + name + "\n",
	}
	if err := skills.WriteSkill(dir, s); err != nil {
		t.Fatalf("writeSkillFixture(%s): %v", name, err)
	}
}
