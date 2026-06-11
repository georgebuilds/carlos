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

// TestSkill_WriteSkillNil: WriteSkill rejects a nil *Skill before any IO.
func TestSkill_WriteSkillNil(t *testing.T) {
	if err := skills.WriteSkill(t.TempDir(), nil); err == nil {
		t.Error("want error for nil skill")
	}
}

// TestSkill_WriteSkillRejectsCrowdedDir: WriteSkill enforces the
// file-count cap on the DESTINATION directory so a previously-polluted
// dir can't smuggle a write past the guard. Regression for the
// dest-dir cap branch in WriteSkill.
func TestSkill_WriteSkillRejectsCrowdedDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "crowded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < skills.MaxFilesPerSkill+2; i++ {
		_ = os.WriteFile(filepath.Join(dir, "junk-"+pad(i)+".txt"), []byte("x"), 0o600)
	}
	err := skills.WriteSkill(dir, minimalSkill())
	if err == nil {
		t.Fatal("want error writing into an over-capacity directory")
	}
	if !strings.Contains(err.Error(), "exceeds cap") {
		t.Errorf("error should mention the cap, got %q", err.Error())
	}
}

// TestSkill_WriteSkillMkdirFails: when the target dir's parent is a
// regular file, MkdirAll fails and WriteSkill surfaces the error
// without leaving partial state. Exercises the mkdir error branch.
func TestSkill_WriteSkillMkdirFails(t *testing.T) {
	base := t.TempDir()
	// Create a regular file, then try to write a skill UNDER it.
	fileAsParent := filepath.Join(base, "iamafile")
	if err := os.WriteFile(fileAsParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(fileAsParent, "child-skill")
	err := skills.WriteSkill(dir, minimalSkill())
	if err == nil {
		t.Fatal("want mkdir error when parent is a file")
	}
	if !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("error should mention mkdir, got %q", err.Error())
	}
}

// TestSkill_WriteSkillOpenTmpFails: when SKILL.md.tmp already exists as
// a DIRECTORY, opening it for write fails and WriteSkill surfaces the
// error. Exercises the open-tmp error branch.
func TestSkill_WriteSkillOpenTmpFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tmp-collision")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-create SKILL.md.tmp as a directory so OpenFile(O_WRONLY) fails.
	if err := os.MkdirAll(filepath.Join(dir, "SKILL.md.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := skills.WriteSkill(dir, minimalSkill())
	if err == nil {
		t.Fatal("want open-tmp error when tmp path is a directory")
	}
	// The real SKILL.md must not have been created.
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); !os.IsNotExist(err) {
		t.Errorf("SKILL.md should not exist after open-tmp failure: %v", err)
	}
}

// TestSkill_WriteSkillBodyWithoutTrailingNewline: a body that lacks a
// trailing newline gets one appended on write (renderSkill newline
// branch); a body that already ends in newline is left as-is.
func TestSkill_WriteSkillBodyWithoutTrailingNewline(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nlfix")
	s := minimalSkill()
	s.Body = "no trailing newline" // note: no \n
	if err := skills.WriteSkill(dir, s); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "no trailing newline\n") {
		t.Errorf("expected a trailing newline appended, got %q", string(raw))
	}
}

// TestSkill_LoadBundleSkillHappyPath: a single .md file with frontmatter
// loads as a bundle skill; Path is the file path (not a dir) and the
// body is preserved.
func TestSkill_LoadBundleSkillHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "calendar-ics.md")
	content := "---\nname: calendar-ics\ndescription: Use when reading an ICS file\nbackend: ics\n---\n# body\n\nsteps here\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := skills.LoadBundleSkill(path)
	if err != nil {
		t.Fatalf("LoadBundleSkill: %v", err)
	}
	if s.Name != "calendar-ics" {
		t.Errorf("name: %q", s.Name)
	}
	if s.Backend != "ics" {
		t.Errorf("backend: %q", s.Backend)
	}
	if s.Path != path {
		t.Errorf("path: want %q got %q", path, s.Path)
	}
	if !strings.Contains(s.Body, "steps here") {
		t.Errorf("body lost: %q", s.Body)
	}
}

// TestSkill_LoadBundleSkillEmptyPath: an empty path is a hard error.
func TestSkill_LoadBundleSkillEmptyPath(t *testing.T) {
	if _, err := skills.LoadBundleSkill(""); err == nil {
		t.Error("want error for empty path")
	}
}

// TestSkill_LoadBundleSkillMissing: a non-existent file errors and the
// message names the path.
func TestSkill_LoadBundleSkillMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ghost.md")
	_, err := skills.LoadBundleSkill(p)
	if err == nil {
		t.Fatal("want error for missing file")
	}
	if !strings.Contains(err.Error(), "ghost.md") {
		t.Errorf("error should name the path, got %q", err.Error())
	}
}

// TestSkill_LoadBundleSkillNoFrontmatter: bundle skills have no
// defensible fallback name, so a frontmatter-less file is rejected
// rather than guessed.
func TestSkill_LoadBundleSkillNoFrontmatter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "naked.md")
	if err := os.WriteFile(path, []byte("just a body, no frontmatter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := skills.LoadBundleSkill(path)
	if err == nil {
		t.Fatal("want error for missing frontmatter")
	}
	if !strings.Contains(err.Error(), "frontmatter") {
		t.Errorf("error should mention frontmatter, got %q", err.Error())
	}
}

// TestSkill_LoadBundleSkillMalformedYAML: frontmatter present but the
// YAML doesn't unmarshal (or Validate fails) surfaces a path-tagged
// error. Here we omit the required description so Validate rejects it.
func TestSkill_LoadBundleSkillFailsValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-desc.md")
	// name present, description missing → Validate rejects.
	if err := os.WriteFile(path, []byte("---\nname: x\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := skills.LoadBundleSkill(path)
	if err == nil {
		t.Fatal("want validate error for missing description")
	}
}

// TestSkill_LoadSkillEmptyDir: LoadSkill with an empty dir string is a
// hard error before any filesystem touch.
func TestSkill_LoadSkillEmptyDir(t *testing.T) {
	if _, err := skills.LoadSkill(""); err == nil {
		t.Error("want error for empty dir")
	}
}

// TestSkill_ValidateNilReceiver: a nil *Skill fails Validate with a
// stable message rather than panicking.
func TestSkill_ValidateNilReceiver(t *testing.T) {
	var s *skills.Skill
	if err := s.Validate(); err == nil {
		t.Error("want error for nil skill")
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
