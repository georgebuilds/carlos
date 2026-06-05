package skills_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/skills"
)

// TestSkill_RoundTrip: write a Skill via WriteSkill, parse it back with
// LoadSkill, every persisted field round-trips. Verifies (a) the YAML
// frontmatter shape, (b) the file mode 0600, (c) the body separator.
func TestSkill_RoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test-skill")
	s := &skills.Skill{
		Name:         "test-skill",
		Description:  "Use when you need to verify round-trip serialization.",
		Provenance:   skills.ProvInduced,
		InducedFrom:  []string{"agent-1", "agent-2"},
		InducerModel: "anthropic:claude-3-5-sonnet",
		JudgeModel:   "openai:gpt-4o",
		Created:      time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC),
		Updated:      time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC),
		ReuseCount:   3,
		Body:         "# heading\n\nbody text here\n",
	}
	if err := skills.WriteSkill(dir, s); err != nil {
		t.Fatalf("WriteSkill: %v", err)
	}

	// Verify perms 0600 on SKILL.md.
	info, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perms: want 0600 got %o", info.Mode().Perm())
	}

	loaded, err := skills.LoadSkill(dir)
	if err != nil {
		t.Fatalf("LoadSkill: %v", err)
	}
	if loaded.Name != s.Name {
		t.Errorf("name: want %q got %q", s.Name, loaded.Name)
	}
	if loaded.Description != s.Description {
		t.Errorf("desc: want %q got %q", s.Description, loaded.Description)
	}
	if loaded.Provenance != s.Provenance {
		t.Errorf("provenance: want %q got %q", s.Provenance, loaded.Provenance)
	}
	if loaded.InducerModel != s.InducerModel {
		t.Errorf("inducer_model: want %q got %q", s.InducerModel, loaded.InducerModel)
	}
	if loaded.JudgeModel != s.JudgeModel {
		t.Errorf("judge_model: want %q got %q", s.JudgeModel, loaded.JudgeModel)
	}
	if loaded.ReuseCount != s.ReuseCount {
		t.Errorf("reuse_count: want %d got %d", s.ReuseCount, loaded.ReuseCount)
	}
	if len(loaded.InducedFrom) != 2 || loaded.InducedFrom[0] != "agent-1" {
		t.Errorf("induced_from: %v", loaded.InducedFrom)
	}
	if !strings.Contains(loaded.Body, "body text here") {
		t.Errorf("body lost: %q", loaded.Body)
	}
	if loaded.Path != dir {
		t.Errorf("path: want %q got %q", dir, loaded.Path)
	}
}

// TestSkill_AtomicWrite: confirm no .tmp file is left behind after a
// successful WriteSkill, mirroring config.go's atomic-write invariant.
func TestSkill_AtomicWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "atomic")
	s := minimalSkill()
	if err := skills.WriteSkill(dir, s); err != nil {
		t.Fatalf("WriteSkill: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("tmp leftover: %s", e.Name())
		}
	}
}

// TestSkill_OverwriteIsIdempotent: writing the same skill twice
// produces a stable file; the second write fully replaces the first
// (rename-over-existing).
func TestSkill_OverwriteIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ovr")
	s := minimalSkill()
	if err := skills.WriteSkill(dir, s); err != nil {
		t.Fatalf("first: %v", err)
	}
	s.Description = "Use when description has changed."
	if err := skills.WriteSkill(dir, s); err != nil {
		t.Fatalf("second: %v", err)
	}
	loaded, err := skills.LoadSkill(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Description != "Use when description has changed." {
		t.Errorf("overwrite lost: %q", loaded.Description)
	}
}

// TestSkill_ValidateNameRequired: name is mandatory in the SPEC.
func TestSkill_ValidateNameRequired(t *testing.T) {
	s := &skills.Skill{Description: "Use when ..."}
	if err := s.Validate(); err == nil {
		t.Error("want error for missing name")
	}
}

// TestSkill_ValidateDescriptionRequired: description is the load-bearing
// always-resident field; SPEC says it's required.
func TestSkill_ValidateDescriptionRequired(t *testing.T) {
	s := &skills.Skill{Name: "x"}
	if err := s.Validate(); err == nil {
		t.Error("want error for missing description")
	}
}

// TestSkill_ValidateBodyCap: bodies > 5000 tokens (20_000 chars)
// rejected.
func TestSkill_ValidateBodyCap(t *testing.T) {
	s := minimalSkill()
	s.Body = strings.Repeat("a", skills.MaxBodyChars+1)
	if err := s.Validate(); err == nil {
		t.Error("want error for oversized body")
	}
}

// TestSkill_WriteSkillRejectsOversized: WriteSkill must call Validate
// before writing any bytes.
func TestSkill_WriteSkillRejectsOversized(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "big")
	s := minimalSkill()
	s.Body = strings.Repeat("a", skills.MaxBodyChars+1)
	err := skills.WriteSkill(dir, s)
	if err == nil {
		t.Fatal("want error for oversized body")
	}
	// Nothing should land on disk.
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); !os.IsNotExist(err) {
		t.Errorf("file should not exist after validation failure: %v", err)
	}
}

// TestSkill_LoadSkillMissing: error message must mention the path.
func TestSkill_LoadSkillMissing(t *testing.T) {
	_, err := skills.LoadSkill(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error %q should include path", err.Error())
	}
}

// TestSkill_LoadSkillMalformedFrontmatter: an unterminated frontmatter
// (no closing `---`) is detected.
func TestSkill_LoadSkillMalformedFrontmatter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := "---\nname: foo\n(this never terminates)"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := skills.LoadSkill(dir)
	if err == nil {
		t.Fatal("want error for unterminated frontmatter")
	}
}

// TestSkill_LoadSkillNoFrontmatter: a body-only file should still
// parse; the basename of the dir becomes the name.
func TestSkill_LoadSkillNoFrontmatter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "body-only-skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Without frontmatter the loader uses defaults; Validate then fails
	// because description is missing. Provide a minimal description-only
	// frontmatter via the looser shape: just markdown body should fail
	// validate. The contract is: frontmatter-less files fail Validate
	// because no description.
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("just body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := skills.LoadSkill(dir)
	if err == nil {
		t.Error("want validate error for body-only (no description)")
	}
}

// TestSkill_LoadSkillTooManyFiles enforces the file-count cap so a
// malformed bundle can't explode the loader.
func TestSkill_LoadSkillTooManyFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "many")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a valid SKILL.md + a bunch of decoys.
	s := minimalSkill()
	if err := skills.WriteSkill(dir, s); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < skills.MaxFilesPerSkill+5; i++ {
		path := filepath.Join(dir, "decoy-"+pad(i)+".txt")
		_ = os.WriteFile(path, []byte("x"), 0o600)
	}
	_, err := skills.LoadSkill(dir)
	if err == nil {
		t.Error("want error for too many files")
	}
}

func minimalSkill() *skills.Skill {
	return &skills.Skill{
		Name:        "minimal",
		Description: "Use when running the round-trip test fixtures.",
		Provenance:  skills.ProvHandWritten,
		Created:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Updated:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Body:        "body\n",
	}
}

func pad(i int) string {
	s := ""
	for v := i; v > 0; v /= 10 {
		s = string(rune('0'+v%10)) + s
	}
	if s == "" {
		s = "0"
	}
	return s
}
