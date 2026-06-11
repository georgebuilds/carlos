package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
)

// newObsidianFramedEnv wires a notesEnv whose CONFIGURED vault is the
// fixture vault and that knows two frames: "personal" (whole vault) and
// "work" (the sub/ subtree). The obsidian_* tools use this to exercise
// the frame: shorthand which resolves against the configured vault path.
func newObsidianFramedEnv(t *testing.T) *notesEnv {
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
		"personal",
	)
}

// errMsg pulls the "error" field out of a tool envelope, "" if absent.
func errMsg(t *testing.T, out []byte) string {
	t.Helper()
	m := asMap(t, out)
	s, _ := m["error"].(string)
	return s
}

// --- resolveObsidianVault ------------------------------------------------

// TestResolveObsidianVault_ExplicitWins — an explicit vault arg beats the
// frame shorthand; the frame is still resolved for labelling.
func TestResolveObsidianVault_ExplicitWins(t *testing.T) {
	env := newObsidianFramedEnv(t)
	vault, subtree, name, err := env.resolveObsidianVault("/some/vault", "work")
	if err != nil {
		t.Fatal(err)
	}
	if vault != "/some/vault" {
		t.Errorf("vault = %q, want explicit /some/vault", vault)
	}
	if subtree != "sub" {
		t.Errorf("subtree = %q, want sub (from work frame)", subtree)
	}
	if name != "work" {
		t.Errorf("name = %q, want work", name)
	}
}

// TestResolveObsidianVault_ExplicitUnknownFrame — explicit vault + an
// unknown frame surfaces the frame error rather than silently ignoring it.
func TestResolveObsidianVault_ExplicitUnknownFrame(t *testing.T) {
	env := newObsidianFramedEnv(t)
	_, _, _, err := env.resolveObsidianVault("/some/vault", "nope")
	if err == nil {
		t.Fatal("expected error for unknown frame with explicit vault")
	}
}

// TestResolveObsidianVault_FrameOnly — frame-only resolves to the
// configured vault path joined logically with that frame's subtree.
func TestResolveObsidianVault_FrameOnly(t *testing.T) {
	env := newObsidianFramedEnv(t)
	vault, subtree, name, err := env.resolveObsidianVault("", "work")
	if err != nil {
		t.Fatal(err)
	}
	if vault != env.cfg.Path {
		t.Errorf("vault = %q, want configured %q", vault, env.cfg.Path)
	}
	if subtree != "sub" || name != "work" {
		t.Errorf("got (%q,%q), want (sub,work)", subtree, name)
	}
}

// TestResolveObsidianVault_FrameOnlyUnknown — unknown frame-only errors.
func TestResolveObsidianVault_FrameOnlyUnknown(t *testing.T) {
	env := newObsidianFramedEnv(t)
	if _, _, _, err := env.resolveObsidianVault("", "ghost"); err == nil {
		t.Fatal("expected error for unknown frame-only")
	}
}

// TestResolveObsidianVault_Neither — neither arg returns zeros + nil so the
// caller's "missing required field" short-circuit fires.
func TestResolveObsidianVault_Neither(t *testing.T) {
	env := newObsidianFramedEnv(t)
	vault, _, _, err := env.resolveObsidianVault("", "")
	if err != nil {
		t.Fatal(err)
	}
	if vault != "" {
		t.Errorf("vault = %q, want empty", vault)
	}
}

// --- obsidian_get --------------------------------------------------------

func TestObsidianGet_MissingVault(t *testing.T) {
	tool := NewObsidianGetTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"note": "carlos"}`))
	if !strings.Contains(errMsg(t, out), "vault") {
		t.Errorf("want missing-vault error, got %q", errMsg(t, out))
	}
}

func TestObsidianGet_MissingNote(t *testing.T) {
	tool := NewObsidianGetTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"frame": "personal"}`))
	if !strings.Contains(errMsg(t, out), "note") {
		t.Errorf("want missing-note error, got %q", errMsg(t, out))
	}
}

func TestObsidianGet_UnknownFrame(t *testing.T) {
	tool := NewObsidianGetTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"note": "carlos", "frame": "ghost"}`))
	if !strings.Contains(errMsg(t, out), "unknown frame") {
		t.Errorf("want unknown-frame error, got %q", errMsg(t, out))
	}
}

func TestObsidianGet_HappyExplicitVault(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianGetTool(env)
	out, err := tool.Execute(context.Background(),
		[]byte(`{"vault": "`+env.cfg.Path+`", "note": "carlos"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["path"] != "carlos.md" {
		t.Errorf("path = %v, want carlos.md", m["path"])
	}
	if m["title"] != "carlos" {
		t.Errorf("title = %v", m["title"])
	}
}

func TestObsidianGet_WithSectionAndBody(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianGetTool(env)
	// section path
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"vault": "`+env.cfg.Path+`", "note": "mvp-roadmap", "section": "Phase 11"}`))
	m := asMap(t, out)
	if m["section"] != "Phase 11" {
		t.Errorf("section = %v", m["section"])
	}
	if body, _ := m["body"].(string); body == "" {
		t.Error("section body empty")
	}
	// body path
	out2, _ := tool.Execute(context.Background(),
		[]byte(`{"vault": "`+env.cfg.Path+`", "note": "carlos", "body": true}`))
	if body, _ := asMap(t, out2)["body"].(string); body == "" {
		t.Error("raw body empty")
	}
}

func TestObsidianGet_NoteNotFound(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianGetTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"vault": "`+env.cfg.Path+`", "note": "does-not-exist"}`))
	if !strings.Contains(errMsg(t, out), "not found") {
		t.Errorf("want not-found, got %q", errMsg(t, out))
	}
}

// TestObsidianGet_FrameSubtreeFiltersOut — a note that exists in the root
// but not the work subtree is reported not-found when frame=work.
func TestObsidianGet_FrameSubtreeFiltersOut(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianGetTool(env)
	// carlos.md is at the root, not under sub/, so frame=work hides it.
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"note": "carlos", "frame": "work"}`))
	if !strings.Contains(errMsg(t, out), "not found") {
		t.Errorf("want not-found for out-of-subtree note, got %q", errMsg(t, out))
	}
}

func TestObsidianGet_BadVaultEnvelope(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianGetTool(env)
	out, err := tool.Execute(context.Background(),
		[]byte(`{"vault": "/no/such/vault/path/here", "note": "carlos"}`))
	if err != nil {
		t.Fatal(err)
	}
	if errMsg(t, out) == "" {
		t.Error("expected an error envelope for a non-existent vault")
	}
}

// --- obsidian_search -----------------------------------------------------

func TestObsidianSearch_BadJSON(t *testing.T) {
	tool := NewObsidianSearchTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{`))
	if !strings.Contains(errMsg(t, out), "parse input") {
		t.Errorf("want parse error, got %q", errMsg(t, out))
	}
}

func TestObsidianSearch_MissingVault(t *testing.T) {
	tool := NewObsidianSearchTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"query": "x"}`))
	if !strings.Contains(errMsg(t, out), "vault") {
		t.Errorf("want missing-vault, got %q", errMsg(t, out))
	}
}

func TestObsidianSearch_MissingQuery(t *testing.T) {
	tool := NewObsidianSearchTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"frame": "personal"}`))
	if !strings.Contains(errMsg(t, out), "query") {
		t.Errorf("want missing-query, got %q", errMsg(t, out))
	}
}

func TestObsidianSearch_UnknownFrame(t *testing.T) {
	tool := NewObsidianSearchTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"query": "x", "frame": "ghost"}`))
	if !strings.Contains(errMsg(t, out), "unknown frame") {
		t.Errorf("want unknown-frame, got %q", errMsg(t, out))
	}
}

func TestObsidianSearch_FrameRestrictsToSubtree(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianSearchTool(env)
	out, err := tool.Execute(context.Background(),
		[]byte(`{"query": "duplicate-title", "frame": "work"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	matches, _ := m["matches"].([]any)
	if len(matches) == 0 {
		t.Fatalf("expected matches in work frame; got %+v", m)
	}
	for _, e := range matches {
		em, _ := e.(map[string]any)
		p, _ := em["path"].(string)
		if !strings.HasPrefix(p, "sub/") {
			t.Errorf("frame=work hit outside subtree: %q", p)
		}
		if em["frame"] != "work" {
			t.Errorf("frame label = %v, want work", em["frame"])
		}
	}
}

func TestObsidianSearch_HappyExplicitVault(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianSearchTool(env)
	out, err := tool.Execute(context.Background(),
		[]byte(`{"vault": "`+env.cfg.Path+`", "query": "carlos"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if matches, _ := m["matches"].([]any); len(matches) == 0 {
		t.Errorf("expected matches; got %+v", m)
	}
}

// --- obsidian_backlinks --------------------------------------------------

func TestObsidianBacklinks_MissingVault(t *testing.T) {
	tool := NewObsidianBacklinksTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"note": "carlos"}`))
	if !strings.Contains(errMsg(t, out), "vault") {
		t.Errorf("want missing-vault, got %q", errMsg(t, out))
	}
}

func TestObsidianBacklinks_MissingNote(t *testing.T) {
	tool := NewObsidianBacklinksTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"frame": "personal"}`))
	if !strings.Contains(errMsg(t, out), "note") {
		t.Errorf("want missing-note, got %q", errMsg(t, out))
	}
}

func TestObsidianBacklinks_Happy(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianBacklinksTool(env)
	// carlos is linked-to by mvp-roadmap.md, so it has backlinks.
	out, err := tool.Execute(context.Background(),
		[]byte(`{"vault": "`+env.cfg.Path+`", "note": "carlos"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if _, has := m["backlinks"]; !has {
		t.Errorf("expected backlinks field; got %+v", m)
	}
}

func TestObsidianBacklinks_FrameFiltersOut(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianBacklinksTool(env)
	// carlos resolves at root; frame=work should report not-found.
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"note": "carlos", "frame": "work"}`))
	if !strings.Contains(errMsg(t, out), "not found") {
		t.Errorf("want not-found, got %q", errMsg(t, out))
	}
}

// --- obsidian_tagged -----------------------------------------------------

func TestObsidianTagged_MissingVault(t *testing.T) {
	tool := NewObsidianTaggedTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"tag": "meta"}`))
	if !strings.Contains(errMsg(t, out), "vault") {
		t.Errorf("want missing-vault, got %q", errMsg(t, out))
	}
}

func TestObsidianTagged_MissingTag(t *testing.T) {
	tool := NewObsidianTaggedTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"frame": "personal"}`))
	if !strings.Contains(errMsg(t, out), "tag") {
		t.Errorf("want missing-tag, got %q", errMsg(t, out))
	}
}

func TestObsidianTagged_Happy(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianTaggedTool(env)
	out, err := tool.Execute(context.Background(),
		[]byte(`{"vault": "`+env.cfg.Path+`", "tag": "meta"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["tag"] != "meta" {
		t.Errorf("tag = %v", m["tag"])
	}
	notesArr, _ := m["notes"].([]any)
	if len(notesArr) == 0 {
		t.Errorf("expected notes for tag meta; got %+v", m)
	}
}

func TestObsidianTagged_FrameRestricts(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianTaggedTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"tag": "meta", "frame": "work"}`))
	m := asMap(t, out)
	notesArr, _ := m["notes"].([]any)
	for _, e := range notesArr {
		em, _ := e.(map[string]any)
		p, _ := em["path"].(string)
		if !strings.HasPrefix(p, "sub/") {
			t.Errorf("frame=work tagged hit outside subtree: %q", p)
		}
		if em["frame"] != "work" {
			t.Errorf("frame label = %v, want work", em["frame"])
		}
	}
}

func TestObsidianTagged_LimitTruncates(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianTaggedTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"vault": "`+env.cfg.Path+`", "tag": "meta", "limit": 1}`))
	m := asMap(t, out)
	notesArr, _ := m["notes"].([]any)
	if len(notesArr) > 1 {
		t.Errorf("limit=1 should cap notes at 1; got %d", len(notesArr))
	}
}

// --- obsidian_neighbors --------------------------------------------------

func TestObsidianNeighbors_BadJSON(t *testing.T) {
	tool := NewObsidianNeighborsTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`}`))
	if !strings.Contains(errMsg(t, out), "parse input") {
		t.Errorf("want parse error, got %q", errMsg(t, out))
	}
}

func TestObsidianNeighbors_MissingVault(t *testing.T) {
	tool := NewObsidianNeighborsTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"note": "carlos"}`))
	if !strings.Contains(errMsg(t, out), "vault") {
		t.Errorf("want missing-vault, got %q", errMsg(t, out))
	}
}

func TestObsidianNeighbors_MissingNote(t *testing.T) {
	tool := NewObsidianNeighborsTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"frame": "personal"}`))
	if !strings.Contains(errMsg(t, out), "note") {
		t.Errorf("want missing-note, got %q", errMsg(t, out))
	}
}

func TestObsidianNeighbors_Happy(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianNeighborsTool(env)
	out, err := tool.Execute(context.Background(),
		[]byte(`{"vault": "`+env.cfg.Path+`", "note": "carlos"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if _, has := m["outgoing"]; !has {
		t.Errorf("expected outgoing field; got %+v", m)
	}
	if _, has := m["incoming"]; !has {
		t.Errorf("expected incoming field; got %+v", m)
	}
}

func TestObsidianNeighbors_FrameFiltersOut(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianNeighborsTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"note": "carlos", "frame": "work"}`))
	if !strings.Contains(errMsg(t, out), "not found") {
		t.Errorf("want not-found, got %q", errMsg(t, out))
	}
}

// --- obsidian_recent -----------------------------------------------------

func TestObsidianRecent_BadJSON(t *testing.T) {
	tool := NewObsidianRecentTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{bad`))
	if !strings.Contains(errMsg(t, out), "parse input") {
		t.Errorf("want parse error, got %q", errMsg(t, out))
	}
}

func TestObsidianRecent_MissingVault(t *testing.T) {
	// Empty input + no frame -> resolveObsidianVault returns empty vault.
	tool := NewObsidianRecentTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(``))
	if !strings.Contains(errMsg(t, out), "vault") {
		t.Errorf("want missing-vault, got %q", errMsg(t, out))
	}
}

func TestObsidianRecent_UnknownFrame(t *testing.T) {
	tool := NewObsidianRecentTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"frame": "ghost"}`))
	if !strings.Contains(errMsg(t, out), "unknown frame") {
		t.Errorf("want unknown-frame, got %q", errMsg(t, out))
	}
}

func TestObsidianRecent_BadSince(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianRecentTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"vault": "`+env.cfg.Path+`", "since": "notaduration"}`))
	if !strings.Contains(errMsg(t, out), "invalid since") {
		t.Errorf("want invalid-since, got %q", errMsg(t, out))
	}
}

func TestObsidianRecent_Happy(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianRecentTool(env)
	out, err := tool.Execute(context.Background(),
		[]byte(`{"vault": "`+env.cfg.Path+`", "limit": 3}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	notesArr, _ := m["notes"].([]any)
	if len(notesArr) == 0 {
		t.Errorf("expected recent notes; got %+v", m)
	}
	if len(notesArr) > 3 {
		t.Errorf("limit=3 exceeded: %d", len(notesArr))
	}
}

// TestObsidianRecent_ValidSince — a parseable since duration is accepted
// and filters by modtime (exercises the since-assignment branch).
func TestObsidianRecent_ValidSince(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianRecentTool(env)
	out, err := tool.Execute(context.Background(),
		[]byte(`{"vault": "`+env.cfg.Path+`", "since": "8760h"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["since"] != "8760h" {
		t.Errorf("since echo = %v, want 8760h", m["since"])
	}
	if _, has := m["notes"]; !has {
		t.Errorf("expected notes field; got %+v", m)
	}
}

func TestObsidianRecent_FrameRestricts(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianRecentTool(env)
	out, _ := tool.Execute(context.Background(), []byte(`{"frame": "work"}`))
	m := asMap(t, out)
	notesArr, _ := m["notes"].([]any)
	for _, e := range notesArr {
		em, _ := e.(map[string]any)
		p, _ := em["path"].(string)
		if !strings.HasPrefix(p, "sub/") {
			t.Errorf("frame=work recent hit outside subtree: %q", p)
		}
	}
}

// --- obsidian_resolve ----------------------------------------------------

func TestObsidianResolve_BadJSON(t *testing.T) {
	tool := NewObsidianResolveTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`nope`))
	if !strings.Contains(errMsg(t, out), "parse input") {
		t.Errorf("want parse error, got %q", errMsg(t, out))
	}
}

func TestObsidianResolve_MissingVault(t *testing.T) {
	tool := NewObsidianResolveTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"link": "carlos"}`))
	if !strings.Contains(errMsg(t, out), "vault") {
		t.Errorf("want missing-vault, got %q", errMsg(t, out))
	}
}

func TestObsidianResolve_MissingLink(t *testing.T) {
	tool := NewObsidianResolveTool(newObsidianFramedEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"frame": "personal"}`))
	if !strings.Contains(errMsg(t, out), "link") {
		t.Errorf("want missing-link, got %q", errMsg(t, out))
	}
}

func TestObsidianResolve_Happy(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianResolveTool(env)
	out, err := tool.Execute(context.Background(),
		[]byte(`{"vault": "`+env.cfg.Path+`", "link": "carlos"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["resolved"] != "carlos.md" {
		t.Errorf("resolved = %v, want carlos.md", m["resolved"])
	}
}

// TestObsidianResolve_Ambiguous — "notes" resolves to both notes.md and
// sub/notes.md (duplicate titles), so the response should be ambiguous
// with candidates.
func TestObsidianResolve_Ambiguous(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianResolveTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"vault": "`+env.cfg.Path+`", "link": "notes"}`))
	m := asMap(t, out)
	if m["ambiguous"] != true {
		t.Errorf("ambiguous = %v, want true; full=%+v", m["ambiguous"], m)
	}
	if cands, _ := m["candidates"].([]any); len(cands) < 2 {
		t.Errorf("expected >=2 candidates; got %+v", m["candidates"])
	}
}

// TestObsidianResolve_FrameFiltersCandidates — frame=work narrows the
// ambiguous "notes" link to the sub/ candidate only, so it is no longer
// ambiguous and resolves to sub/notes.md.
func TestObsidianResolve_FrameFiltersCandidates(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianResolveTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"link": "notes", "frame": "work"}`))
	m := asMap(t, out)
	if m["resolved"] != "sub/notes.md" {
		t.Errorf("resolved = %v, want sub/notes.md", m["resolved"])
	}
}

// TestObsidianResolve_FrameNoCandidatesNotFound — a link that resolves
// only outside the work subtree returns not-found under frame=work.
func TestObsidianResolve_FrameNoCandidatesNotFound(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tool := NewObsidianResolveTool(env)
	out, _ := tool.Execute(context.Background(),
		[]byte(`{"link": "mvp-roadmap", "frame": "work"}`))
	if !strings.Contains(errMsg(t, out), "not found") {
		t.Errorf("want not-found for out-of-subtree link, got %q", errMsg(t, out))
	}
}

// TestObsidianTools_BadVaultEnvelope — every obsidian_* tool surfaces the
// "open vault" error envelope (not a transport error) when handed a vault
// path that cannot be opened.
func TestObsidianTools_BadVaultEnvelope(t *testing.T) {
	env := newObsidianFramedEnv(t)
	bad := "/no/such/vault/path/xyz"
	cases := []struct {
		name  string
		exec  func(context.Context, []byte) ([]byte, error)
		input string
	}{
		{"obsidian_get", NewObsidianGetTool(env).Execute, `{"vault":"` + bad + `","note":"x"}`},
		{"obsidian_search", NewObsidianSearchTool(env).Execute, `{"vault":"` + bad + `","query":"x"}`},
		{"obsidian_backlinks", NewObsidianBacklinksTool(env).Execute, `{"vault":"` + bad + `","note":"x"}`},
		{"obsidian_tagged", NewObsidianTaggedTool(env).Execute, `{"vault":"` + bad + `","tag":"x"}`},
		{"obsidian_neighbors", NewObsidianNeighborsTool(env).Execute, `{"vault":"` + bad + `","note":"x"}`},
		{"obsidian_recent", NewObsidianRecentTool(env).Execute, `{"vault":"` + bad + `"}`},
		{"obsidian_resolve", NewObsidianResolveTool(env).Execute, `{"vault":"` + bad + `","link":"x"}`},
	}
	for _, c := range cases {
		out, err := c.exec(context.Background(), []byte(c.input))
		if err != nil {
			t.Errorf("%s: want envelope, got transport error: %v", c.name, err)
			continue
		}
		if errMsg(t, out) == "" {
			t.Errorf("%s: expected an error envelope for a bad vault", c.name)
		}
	}
}

// --- schema sanity -------------------------------------------------------

// TestObsidianSchemasValid — every obsidian_* tool emits parseable JSON
// schema and a non-empty name/description.
func TestObsidianSchemasValid(t *testing.T) {
	env := newObsidianFramedEnv(t)
	tools := []Tool{
		NewObsidianGetTool(env),
		NewObsidianSearchTool(env),
		NewObsidianBacklinksTool(env),
		NewObsidianTaggedTool(env),
		NewObsidianNeighborsTool(env),
		NewObsidianRecentTool(env),
		NewObsidianResolveTool(env),
	}
	for _, tl := range tools {
		if tl.Name() == "" {
			t.Errorf("empty name")
		}
		if tl.Description() == "" {
			t.Errorf("%s: empty description", tl.Name())
		}
		var js map[string]any
		if err := json.Unmarshal(tl.Schema(), &js); err != nil {
			t.Errorf("%s: schema not valid JSON: %v", tl.Name(), err)
		}
	}
}
