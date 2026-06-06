package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
)

// testVaultPath returns the absolute path to the notes package fixture
// vault. The notes_* tools live in internal/tools but the fixture is
// owned by internal/notes; we resolve through the module root.
func testVaultPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../notes/testdata/vault")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

func testAltVaultPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../notes/testdata/vault_alt")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

// newTestEnv wires a notesEnv with the fixture vault path baked in.
// Tests that want the no-vault envelope construct their own with an
// empty VaultConfig.
func newTestEnv(t *testing.T) *notesEnv {
	t.Helper()
	return newNotesEnv(config.VaultConfig{
		Path:    testVaultPath(t),
		Exclude: []string{"templates/**"},
	})
}

// asMap is a tiny test-side helper that asserts a tool response parses
// as a JSON object and returns the parsed map. Keeps individual
// assertions as `m["field"]` rather than a typed struct so a future
// field addition doesn't break older tests.
func asMap(t *testing.T, out []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("response not a JSON object: %v\n--- raw ---\n%s", err, string(out))
	}
	return m
}

// TestNotesGetHappyPath — `notes_get carlos` returns the expected
// title, frontmatter, outline, and vault path.
func TestNotesGetHappyPath(t *testing.T) {
	tool := NewNotesGetTool(newTestEnv(t))
	out, err := tool.Execute(context.Background(), []byte(`{"note": "carlos"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	m := asMap(t, out)
	if m["path"] != "carlos.md" {
		t.Errorf("path: %v", m["path"])
	}
	if m["title"] != "carlos" {
		t.Errorf("title: %v", m["title"])
	}
	if m["vault"] == "" || m["vault"] == nil {
		t.Errorf("vault field missing: %+v", m)
	}
	if outline, ok := m["outline"].([]any); !ok || len(outline) == 0 {
		t.Errorf("outline missing or empty: %+v", m["outline"])
	}
	// Body should be absent by default.
	if _, has := m["body"]; has {
		t.Errorf("body should be omitted by default; got %+v", m["body"])
	}
}

// TestNotesGetWithBody — `body: true` returns the markdown body.
func TestNotesGetWithBody(t *testing.T) {
	tool := NewNotesGetTool(newTestEnv(t))
	out, err := tool.Execute(context.Background(), []byte(`{"note": "carlos", "body": true}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	body, ok := m["body"].(string)
	if !ok || body == "" {
		t.Fatalf("body missing or empty: %+v", m["body"])
	}
	if !strings.Contains(body, "TUI agent") {
		t.Errorf("body should include note body text; got %q", body)
	}
}

// TestNotesGetWithSection — extracts the named heading's content.
func TestNotesGetWithSection(t *testing.T) {
	tool := NewNotesGetTool(newTestEnv(t))
	out, err := tool.Execute(context.Background(), []byte(`{"note": "mvp-roadmap", "section": "Phase 11"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["section"] != "Phase 11" {
		t.Errorf("section field: %v", m["section"])
	}
	body, _ := m["body"].(string)
	if !strings.Contains(body, "orchestrator") {
		t.Errorf("body should contain orchestrator; got %q", body)
	}
}

// TestNotesGetNoteNotFound — `note not found:` envelope.
func TestNotesGetNoteNotFound(t *testing.T) {
	tool := NewNotesGetTool(newTestEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"note": "phase 99"}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "not found") {
		t.Errorf("expected 'not found' envelope; got %+v", m)
	}
}

// TestNotesGetMissingRequiredField — empty `note:` triggers a friendly
// missing-field envelope, not a panic.
func TestNotesGetMissingRequiredField(t *testing.T) {
	tool := NewNotesGetTool(newTestEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "note") {
		t.Errorf("expected required-field envelope; got %+v", m)
	}
}

// TestObsidianGetPerCallVault — Phase T-1 split: arbitrary vault
// queries now go through obsidian_get, which REQUIRES a vault field.
func TestObsidianGetPerCallVault(t *testing.T) {
	env := newNotesEnv(config.VaultConfig{})
	tool := NewObsidianGetTool(env)
	input := []byte(`{"note": "only-here", "vault": "` + testAltVaultPath(t) + `"}`)
	out, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["path"] != "only-here.md" {
		t.Errorf("expected only-here.md from alt vault; got %+v", m)
	}
	vault, _ := m["vault"].(string)
	if !strings.HasSuffix(vault, "vault_alt") {
		t.Errorf("vault field should point at alt vault; got %q", vault)
	}
}

// TestObsidianGetSameNoteDifferentVaults — obsidian_get against two
// vault fixtures yields different content. T-1 isolation assertion.
func TestObsidianGetSameNoteDifferentVaults(t *testing.T) {
	env := newTestEnv(t)
	primaryOut, _ := NewNotesGetTool(env).Execute(context.Background(), []byte(`{"note": "carlos"}`))
	pm := asMap(t, primaryOut)
	if pm["title"] != "carlos" {
		t.Errorf("primary title: %v", pm["title"])
	}

	altInput := []byte(`{"note": "carlos", "vault": "` + testAltVaultPath(t) + `"}`)
	altOut, _ := NewObsidianGetTool(env).Execute(context.Background(), altInput)
	am := asMap(t, altOut)
	altTitle, _ := am["title"].(string)
	if !strings.Contains(altTitle, "alt vault") {
		t.Errorf("alt title should reflect alt fixture; got %v", am["title"])
	}
}

// TestNotesGetIgnoresVaultField — notes_get is configured-vault-only.
// A vault: field in the input is silently dropped (the model can no
// longer redirect notes_get to an arbitrary path).
func TestNotesGetIgnoresVaultField(t *testing.T) {
	env := newTestEnv(t) // primary fixture wired
	tool := NewNotesGetTool(env)
	// Try to redirect to the alt vault via vault: — should be ignored.
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"note": "carlos", "vault": "`+testAltVaultPath(t)+`"}`))
	m := asMap(t, out)
	if title, _ := m["title"].(string); strings.Contains(title, "alt vault") {
		t.Errorf("notes_get should ignore vault: field; got title=%q (alt content leaked through)", title)
	}
}

// TestNotesGetNoVaultConfigured — both cfg + per-call empty → the
// documented "vault not configured" envelope.
func TestNotesGetNoVaultConfigured(t *testing.T) {
	env := newNotesEnv(config.VaultConfig{})
	tool := NewNotesGetTool(env)
	out, _ := tool.Execute(context.Background(), []byte(`{"note": "carlos"}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "vault not configured") {
		t.Errorf("expected 'vault not configured'; got %+v", m)
	}
}

// TestNotesGetSchemaValid — schema parses as JSON and does NOT
// expose a vault override field (T-1 split: notes_* is pinned to
// the configured vault; obsidian_get is the family that takes vault).
func TestNotesGetSchemaValid(t *testing.T) {
	tool := NewNotesGetTool(newTestEnv(t))
	var sch map[string]any
	if err := json.Unmarshal(tool.Schema(), &sch); err != nil {
		t.Fatalf("schema not JSON: %v", err)
	}
	props, _ := sch["properties"].(map[string]any)
	if _, has := props["vault"]; has {
		t.Error("notes_get schema MUST NOT include `vault` field after T-1 split")
	}
	if _, has := props["note"]; !has {
		t.Error("schema missing `note` field")
	}
}
