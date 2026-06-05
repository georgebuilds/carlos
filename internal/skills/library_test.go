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
