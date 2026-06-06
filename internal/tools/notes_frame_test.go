package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
)

// newFramedTestEnv wires a notesEnv with the fixture vault + two
// frames: "personal" (whole vault) and "work" (the sub/ subtree).
// `active` names the in-session active frame; pass "" to fall through
// to frames.Active. Both frames share the single Cache instance so
// vault opens are amortised across frame-switching tests.
func newFramedTestEnv(t *testing.T, active string) *notesEnv {
	t.Helper()
	frames := frame.Config{
		Default: "personal",
		Active:  "personal",
		List: []frame.Frame{
			{Name: "personal", VaultSubtree: ""},
			{Name: "work", VaultSubtree: "sub"},
		},
	}
	return newNotesEnvWithFrames(
		config.VaultConfig{
			Path:    testVaultPath(t),
			Exclude: []string{"templates/**"},
		},
		frames,
		active,
	)
}

// --- notes_search ------------------------------------------------------

// TestNotesSearchFrameRestrict — `frame: work` restricts results to the
// sub/ subtree. The notes_search query "ambiguity" only hits sub/notes.md
// (and not the root notes.md), so a single matched path proves the
// subtree restriction is wired.
func TestNotesSearchFrameRestrict(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewNotesSearchTool(env)
	out, err := tool.Execute(context.Background(),
		[]byte(`{"query": "duplicate-title", "frame": "work"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	matches, _ := m["matches"].([]any)
	if len(matches) == 0 {
		t.Fatalf("expected at least one match in work frame; got %+v", m)
	}
	for _, e := range matches {
		em, _ := e.(map[string]any)
		path, _ := em["path"].(string)
		if !strings.HasPrefix(path, "sub/") {
			t.Errorf("frame=work hit outside subtree: %q", path)
		}
		fr, _ := em["frame"].(string)
		if fr != "work" {
			t.Errorf("frame label = %q, want %q", fr, "work")
		}
	}
}

// TestNotesSearchFrameFanout — omitting `frame:` with two frames
// configured fans out across both subtrees. Each match carries its
// source frame name, and a hit can appear under any frame whose
// subtree covers its path (dedup by path; first-frame-wins).
func TestNotesSearchFrameFanout(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewNotesSearchTool(env)
	// "notes" hits both root notes.md and sub/notes.md (both carry
	// the term in their title), so we should see at least one entry
	// from each frame.
	out, _ := tool.Execute(context.Background(), []byte(`{"query": "duplicate-title"}`))
	m := asMap(t, out)
	matches, _ := m["matches"].([]any)
	if len(matches) == 0 {
		t.Fatalf("fan-out should return at least one match; got %+v", m)
	}
	// Every match must carry a frame label in fan-out mode.
	for _, e := range matches {
		em, _ := e.(map[string]any)
		fr, _ := em["frame"].(string)
		if fr == "" {
			t.Errorf("fan-out result missing frame label: %+v", em)
		}
	}
}

// TestNotesSearchLegacyNoPrefix — env with no frames behaves byte-for-
// byte like the pre-F-11 surface: matches carry no `frame` field.
func TestNotesSearchLegacyNoPrefix(t *testing.T) {
	tool := NewNotesSearchTool(newTestEnv(t)) // no frames wired
	out, _ := tool.Execute(context.Background(), []byte(`{"query": "carlos"}`))
	m := asMap(t, out)
	matches, _ := m["matches"].([]any)
	if len(matches) == 0 {
		t.Fatal("expected at least one match")
	}
	for _, e := range matches {
		em, _ := e.(map[string]any)
		if fr, has := em["frame"]; has && fr != "" {
			t.Errorf("legacy mode should not emit frame labels; got %v", fr)
		}
	}
}

// TestNotesSearchUnknownFrame — unknown `frame:` produces a clean error
// envelope without panicking.
func TestNotesSearchUnknownFrame(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewNotesSearchTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"query": "carlos", "frame": "nope"}`))
	m := asMap(t, out)
	errMsg, _ := m["error"].(string)
	if !strings.Contains(errMsg, "unknown frame") {
		t.Errorf("expected unknown-frame envelope; got %+v", m)
	}
}

// TestNotesSearchSchemaDeclaresFrame — the JSON schema advertises the
// new optional `frame` field with its semantics noted in the
// description.
func TestNotesSearchSchemaDeclaresFrame(t *testing.T) {
	tool := NewNotesSearchTool(newTestEnv(t))
	var sch map[string]any
	if err := json.Unmarshal(tool.Schema(), &sch); err != nil {
		t.Fatalf("schema not JSON: %v", err)
	}
	props, _ := sch["properties"].(map[string]any)
	fr, has := props["frame"].(map[string]any)
	if !has {
		t.Fatal("notes_search schema missing `frame` field")
	}
	desc, _ := fr["description"].(string)
	if !strings.Contains(desc, "active frame") {
		t.Errorf("frame description should mention active-frame default; got %q", desc)
	}
}

// --- notes_get ---------------------------------------------------------

// TestNotesGetFrameRestrictMisses — `notes_get` with frame=work won't
// resolve a note that lives outside the sub/ subtree.
func TestNotesGetFrameRestrictMisses(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewNotesGetTool(env)
	// carlos.md lives at vault root; frame=work restricts to sub/.
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"note": "carlos", "frame": "work"}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "not found") {
		t.Errorf("expected not-found envelope for cross-frame hit; got %+v", m)
	}
}

// TestNotesGetFrameRestrictHits — `notes_get` with frame=work resolves
// a note that lives inside sub/.
func TestNotesGetFrameRestrictHits(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewNotesGetTool(env)
	out, err := tool.Execute(context.Background(),
		[]byte(`{"note": "sub/notes.md", "frame": "work"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if path, _ := m["path"].(string); path != "sub/notes.md" {
		t.Errorf("expected sub/notes.md; got %v", m["path"])
	}
}

// TestNotesGetActiveFrameDefault — when `frame:` is omitted, the active
// frame's subtree gates resolution. With active=work, the root carlos
// note is unreachable.
func TestNotesGetActiveFrameDefault(t *testing.T) {
	env := newFramedTestEnv(t, "work")
	tool := NewNotesGetTool(env)
	out, _ := tool.Execute(context.Background(), []byte(`{"note": "carlos"}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "not found") {
		t.Errorf("active=work should hide carlos.md; got %+v", m)
	}
}

// TestNotesGetSchemaDeclaresFrame — schema advertises the optional
// `frame` field.
func TestNotesGetSchemaDeclaresFrame(t *testing.T) {
	tool := NewNotesGetTool(newTestEnv(t))
	var sch map[string]any
	if err := json.Unmarshal(tool.Schema(), &sch); err != nil {
		t.Fatalf("schema not JSON: %v", err)
	}
	props, _ := sch["properties"].(map[string]any)
	if _, has := props["frame"]; !has {
		t.Error("notes_get schema missing `frame` field")
	}
}

// --- notes_recent ------------------------------------------------------

// TestNotesRecentFrameRestrict — `frame: work` returns only sub/ notes.
func TestNotesRecentFrameRestrict(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewNotesRecentTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"frame": "work", "limit": 50}`))
	m := asMap(t, out)
	notes, _ := m["notes"].([]any)
	if len(notes) == 0 {
		t.Fatal("expected at least one recent in work frame")
	}
	for _, e := range notes {
		em, _ := e.(map[string]any)
		path, _ := em["path"].(string)
		if !strings.HasPrefix(path, "sub/") {
			t.Errorf("frame=work hit outside subtree: %q", path)
		}
		if fr, _ := em["frame"].(string); fr != "work" {
			t.Errorf("frame label = %q, want %q", fr, "work")
		}
	}
}

// TestNotesRecentFanoutLabels — no `frame:` with two frames configured:
// every entry carries a frame label.
func TestNotesRecentFanoutLabels(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewNotesRecentTool(env)
	out, _ := tool.Execute(context.Background(), []byte(`{"limit": 50}`))
	m := asMap(t, out)
	notes, _ := m["notes"].([]any)
	if len(notes) == 0 {
		t.Fatal("expected at least one recent in fan-out")
	}
	for _, e := range notes {
		em, _ := e.(map[string]any)
		if fr, _ := em["frame"].(string); fr == "" {
			t.Errorf("fan-out result missing frame label: %+v", em)
		}
	}
}

// TestNotesRecentLegacyNoPrefix — no frames wired -> no labels.
func TestNotesRecentLegacyNoPrefix(t *testing.T) {
	tool := NewNotesRecentTool(newTestEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{}`))
	m := asMap(t, out)
	notes, _ := m["notes"].([]any)
	for _, e := range notes {
		em, _ := e.(map[string]any)
		if fr, has := em["frame"]; has && fr != "" {
			t.Errorf("legacy mode should not emit frame labels; got %v", fr)
		}
	}
}

// --- notes_tagged ------------------------------------------------------

// TestNotesTaggedFrameRestrict — `frame: work` with tag "meta" returns
// only sub/notes.md (the root notes.md is excluded).
func TestNotesTaggedFrameRestrict(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewNotesTaggedTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"tag": "meta", "frame": "work"}`))
	m := asMap(t, out)
	notesEntries, _ := m["notes"].([]any)
	if len(notesEntries) != 1 {
		t.Fatalf("expected exactly 1 sub/ meta-tagged note; got %d", len(notesEntries))
	}
	first, _ := notesEntries[0].(map[string]any)
	path, _ := first["path"].(string)
	if path != "sub/notes.md" {
		t.Errorf("expected sub/notes.md; got %q", path)
	}
	if fr, _ := first["frame"].(string); fr != "work" {
		t.Errorf("frame label = %q, want %q", fr, "work")
	}
}

// TestNotesTaggedFanoutLabels — no `frame:` with two frames: every
// entry carries a label.
func TestNotesTaggedFanoutLabels(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewNotesTaggedTool(env)
	out, _ := tool.Execute(context.Background(), []byte(`{"tag": "meta"}`))
	m := asMap(t, out)
	entries, _ := m["notes"].([]any)
	if len(entries) == 0 {
		t.Fatal("expected at least one entry for #meta")
	}
	for _, e := range entries {
		em, _ := e.(map[string]any)
		if fr, _ := em["frame"].(string); fr == "" {
			t.Errorf("fan-out entry missing frame label: %+v", em)
		}
	}
}

// --- notes_neighbors / notes_backlinks / notes_resolve -----------------

// TestNotesResolveFrameRestrict — `notes` is ambiguous (root + sub).
// With frame=work, the work subtree wins.
func TestNotesResolveFrameRestrict(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewNotesResolveTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"link": "notes", "frame": "work"}`))
	m := asMap(t, out)
	resolved, _ := m["resolved"].(string)
	if resolved != "sub/notes.md" {
		t.Errorf("frame=work should resolve to sub/notes.md; got %q", resolved)
	}
}

// TestNotesBacklinksFrameRestrict — backlinks for a root note are
// blocked when frame restricts away from it.
func TestNotesBacklinksFrameRestrict(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewNotesBacklinksTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"note": "carlos", "frame": "work"}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "not found") {
		t.Errorf("expected not-found envelope; got %+v", m)
	}
}

// TestNotesNeighborsFrameRestrict — neighbors of a root note are
// blocked when frame restricts away from it.
func TestNotesNeighborsFrameRestrict(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewNotesNeighborsTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"note": "carlos", "frame": "work"}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "not found") {
		t.Errorf("expected not-found envelope; got %+v", m)
	}
}

// --- obsidian_search ---------------------------------------------------

// TestObsidianSearchFrameShorthand — `frame:` alone (no `vault:`)
// resolves to cfg.Vault.Path + frame.VaultSubtree.
func TestObsidianSearchFrameShorthand(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewObsidianSearchTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"query": "duplicate-title", "frame": "work"}`))
	m := asMap(t, out)
	matches, _ := m["matches"].([]any)
	if len(matches) == 0 {
		t.Fatalf("expected at least one match; got %+v", m)
	}
	for _, e := range matches {
		em, _ := e.(map[string]any)
		path, _ := em["path"].(string)
		if !strings.HasPrefix(path, "sub/") {
			t.Errorf("frame=work hit outside subtree: %q", path)
		}
	}
}

// TestObsidianSearchExplicitVaultBeatsFrame — when both `vault:` and
// `frame:` are passed, the explicit path wins. The frame name still
// labels results so cross-frame fan outs read well.
func TestObsidianSearchExplicitVaultBeatsFrame(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewObsidianSearchTool(env)
	input := []byte(`{"query": "carlos", "vault": "` + testAltVaultPath(t) +
		`", "frame": "work"}`)
	out, _ := tool.Execute(context.Background(), input)
	m := asMap(t, out)
	vault, _ := m["vault"].(string)
	if !strings.HasSuffix(vault, "vault_alt") {
		t.Errorf("explicit vault should win over frame shorthand; got %q", vault)
	}
}

// TestObsidianSearchUnknownFrame — clean envelope, no panic.
func TestObsidianSearchUnknownFrame(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewObsidianSearchTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"query": "carlos", "frame": "nope"}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "unknown frame") {
		t.Errorf("expected unknown-frame envelope; got %+v", m)
	}
}

// TestObsidianGetFrameShorthand — `obsidian_get` with `frame:` alone
// resolves against the configured vault + the frame's subtree.
func TestObsidianGetFrameShorthand(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewObsidianGetTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"note": "sub/notes.md", "frame": "work"}`))
	m := asMap(t, out)
	if path, _ := m["path"].(string); path != "sub/notes.md" {
		t.Errorf("expected sub/notes.md; got %v", m["path"])
	}
}

// TestObsidianGetFrameOnlyMisses — `obsidian_get` with frame=work
// reading a root-level note must fail not-found (cross-frame block).
func TestObsidianGetFrameOnlyMisses(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewObsidianGetTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"note": "carlos", "frame": "work"}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "not found") {
		t.Errorf("expected not-found envelope; got %+v", m)
	}
}

// TestObsidianGetSchemaDeclaresFrame — schema advertises the new
// optional `frame` field on the obsidian_* family.
func TestObsidianGetSchemaDeclaresFrame(t *testing.T) {
	tool := NewObsidianGetTool(newFramedTestEnv(t, "personal"))
	var sch map[string]any
	if err := json.Unmarshal(tool.Schema(), &sch); err != nil {
		t.Fatalf("schema not JSON: %v", err)
	}
	props, _ := sch["properties"].(map[string]any)
	if _, has := props["frame"]; !has {
		t.Error("obsidian_get schema missing `frame` field")
	}
}

// --- registry wiring ---------------------------------------------------

// TestNewDefaultRegistryWithFramesPreserved — the new constructor
// registers the same tool set as the back-compat one.
func TestNewDefaultRegistryWithFramesPreserved(t *testing.T) {
	want := []string{
		"notes_get", "notes_search", "notes_backlinks",
		"notes_tagged", "notes_neighbors", "notes_recent",
		"notes_resolve",
		"obsidian_get", "obsidian_search", "obsidian_backlinks",
		"obsidian_tagged", "obsidian_neighbors", "obsidian_recent",
		"obsidian_resolve",
	}
	frames := frame.Config{
		Default: "personal",
		Active:  "personal",
		List: []frame.Frame{
			{Name: "personal"},
			{Name: "work", VaultSubtree: "sub"},
		},
	}
	r := NewDefaultRegistryWithBaseDirAndFrames("", config.VaultConfig{}, frames, "")
	for _, name := range want {
		if _, ok := r.Get(name); !ok {
			t.Errorf("registry missing tool %q after F-11 constructor", name)
		}
	}
}

// --- cleanSubtree / inSubtree boundary tests ---------------------------

func TestCleanSubtreeNormalises(t *testing.T) {
	cases := map[string]string{
		"":           "",
		"work":       "work",
		"work/":      "work",
		"/work":      "work",
		"work/sub":   "work/sub",
		"./":         "",
		".":          "",
		"work//sub/": "work/sub",
	}
	for in, want := range cases {
		if got := cleanSubtree(in); got != want {
			t.Errorf("cleanSubtree(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInSubtreeBoundaries(t *testing.T) {
	cases := []struct {
		relpath, subtree string
		want             bool
	}{
		{"carlos.md", "", true},
		{"sub/notes.md", "sub", true},
		{"sub/notes.md", "su", false}, // prefix not aligned to slash
		{"subway/x.md", "sub", false}, // boundary check
		{"a/b/c.md", "a/b", true},
		{"a/bx/c.md", "a/b", false},
		{"a/b", "a/b", true}, // exact
	}
	for _, c := range cases {
		if got := inSubtree(c.relpath, c.subtree); got != c.want {
			t.Errorf("inSubtree(%q, %q) = %v, want %v",
				c.relpath, c.subtree, got, c.want)
		}
	}
}
