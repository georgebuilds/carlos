package chat

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/tui/slash"
	"github.com/georgebuilds/carlos/internal/workspace"
)

func TestPermsSlash_RegisteredInBuiltins(t *testing.T) {
	if _, ok := slash.Lookup("permissions"); !ok {
		t.Error("slash.Lookup(\"permissions\"): not in Builtins")
	}
}

func TestPermsSlash_TogglesOverlay(t *testing.T) {
	m := newTestModel(t)
	if m.showPerms {
		t.Fatal("precondition: showPerms should start false")
	}
	c, _ := slash.Parse("/permissions")
	_ = m.dispatchSlash(c)
	if !m.showPerms {
		t.Error("first /permissions: showPerms should be true")
	}
	if m.permsTab != permsTabBuiltin {
		t.Errorf("first open: permsTab = %v, want permsTabBuiltin", m.permsTab)
	}
	_ = m.dispatchSlash(c)
	if m.showPerms {
		t.Error("second /permissions: showPerms should toggle back off")
	}
}

func TestBuiltinRowsCoverPolicyAllowList(t *testing.T) {
	// If this fails, builtinRows is out of sync with
	// agent.DefaultBuiltinAllow: either add the missing row or
	// remove the policy entry. The overlay renders a runtime
	// warning when this drifts, but the test catches it at PR time.
	missing := builtinDrift()
	if len(missing) > 0 {
		t.Errorf("builtinRows missing annotations for: %v", missing)
	}
}

func TestBuiltinRowsHaveNoExtras(t *testing.T) {
	policy := make(map[string]bool, len(agent.DefaultBuiltinAllow))
	for _, name := range agent.DefaultBuiltinAllow {
		policy[name] = true
	}
	var extras []string
	for _, r := range builtinRows {
		if !policy[r.Name] {
			extras = append(extras, r.Name)
		}
	}
	if len(extras) > 0 {
		t.Errorf("builtinRows has entries not in DefaultBuiltinAllow: %v", extras)
	}
}

func TestFilterBuiltinRows_CaseInsensitive(t *testing.T) {
	got := filterBuiltinRows(builtinRows, "NOTES")
	if len(got) == 0 {
		t.Fatal("expected matches for 'NOTES'")
	}
	for _, r := range got {
		if !strings.Contains(strings.ToLower(r.Name), "notes") &&
			!strings.Contains(strings.ToLower(r.Reason), "notes") {
			t.Errorf("filter leaked: %q matched %q", r.Name, "NOTES")
		}
	}
}

func TestFilterBuiltinRows_EmptyFilterPassthrough(t *testing.T) {
	got := filterBuiltinRows(builtinRows, "")
	if len(got) != len(builtinRows) {
		t.Errorf("empty filter: want %d rows got %d", len(builtinRows), len(got))
	}
}

func TestFilterWorkspaceRows_PathMatch(t *testing.T) {
	rows := []permsWorkspaceRow{
		{Path: "/Users/george/Code/carlos"},
		{Path: "/Users/george/Code/anneal"},
	}
	got := filterWorkspaceRows(rows, "anneal")
	if len(got) != 1 || got[0].Path != "/Users/george/Code/anneal" {
		t.Errorf("filter('anneal'): %+v", got)
	}
}

func TestPermsRender_BuiltinHeader(t *testing.T) {
	out := renderPermissionsOverlay(permsTabBuiltin, nil, "", false, 0, 80)
	if !strings.Contains(out, "Permissions") {
		t.Error("missing Permissions header")
	}
	if !strings.Contains(out, "[Built-in]") {
		t.Error("missing active tab marker for Built-in")
	}
	if !strings.Contains(out, "notes_search") {
		t.Error("first builtin row should render")
	}
}

func TestPermsRender_WorkspaceTabWithoutPolicyShowsHint(t *testing.T) {
	out := renderPermissionsOverlay(permsTabWorkspace, nil, "", false, 0, 80)
	if !strings.Contains(out, "[Workspace]") {
		t.Error("missing active tab marker")
	}
	if !strings.Contains(out, "not wired") {
		t.Error("nil policy should show 'not wired' hint")
	}
}

func TestPermsRender_WorkspaceEmptyShowsCTA(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	store := workspace.NewStore(filepath.Join(dir, "t.json"))
	policy := workspace.NewPolicy(store, ws)
	out := renderPermissionsOverlay(permsTabWorkspace, policy, "", false, 0, 80)
	if !strings.Contains(out, "no trusted workspaces yet") {
		t.Errorf("empty workspace tab missing CTA; got:\n%s", out)
	}
	if !strings.Contains(out, "press") || !strings.Contains(out, "t") {
		t.Errorf("CTA should mention `press t to trust`; got:\n%s", out)
	}
}

func TestPermsRender_WorkspacePopulated(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	store := workspace.NewStore(filepath.Join(dir, "t.json"))
	_ = store.Trust(ws)
	policy := workspace.NewPolicy(store, ws)
	out := renderPermissionsOverlay(permsTabWorkspace, policy, "", false, 0, 80)
	// Long paths get wrapped by lipgloss at the box width; assert on
	// the unique leaf instead of the absolute path so wrap-splits
	// don't false-fail the test.
	leaf := filepath.Base(ws)
	if !strings.Contains(out, leaf) {
		t.Errorf("workspace tab should render trusted path leaf %q; got:\n%s", leaf, out)
	}
	if !strings.Contains(out, "(current)") {
		t.Error("cwd-matching entry should be tagged (current)")
	}
}

func TestPermsRender_FooterMentionsCloseAndFilter(t *testing.T) {
	out := renderPermissionsOverlay(permsTabBuiltin, nil, "", false, 0, 80)
	if !strings.Contains(out, "esc") {
		t.Error("footer should mention `esc close`")
	}
	if !strings.Contains(out, "filter") {
		t.Error("footer should mention `/ filter`")
	}
	if !strings.Contains(out, "tab") {
		t.Error("footer should mention `tab section`")
	}
}

func TestPermsRender_WorkspaceFooterHasRowActions(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	store := workspace.NewStore(filepath.Join(dir, "t.json"))
	_ = store.Trust(ws)
	policy := workspace.NewPolicy(store, ws)
	out := renderPermissionsOverlay(permsTabWorkspace, policy, "", false, 0, 80)
	if !strings.Contains(out, "untrust") {
		t.Errorf("workspace footer should mention `d untrust`; got:\n%s", out)
	}
}

func TestPermsKey_TabCycles(t *testing.T) {
	m := newTestModel(t)
	m.showPerms = true
	m.permsTab = permsTabBuiltin
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyTab})
	if m.permsTab != permsTabWorkspace {
		t.Errorf("tab from Built-in: want Workspace got %v", m.permsTab)
	}
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyTab})
	if m.permsTab != permsTabBuiltin {
		t.Errorf("tab wraparound: want Built-in got %v", m.permsTab)
	}
}

func TestPermsKey_ShiftTabCycles(t *testing.T) {
	m := newTestModel(t)
	m.showPerms = true
	m.permsTab = permsTabBuiltin
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.permsTab != permsTabWorkspace {
		t.Errorf("shift+tab from Built-in: want Workspace got %v", m.permsTab)
	}
}

func TestPermsKey_EscClosesAndResets(t *testing.T) {
	m := newTestModel(t)
	m.showPerms = true
	m.permsCursor = 5
	m.permsFilter = "foo"
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.showPerms {
		t.Error("esc should close overlay")
	}
	if m.permsCursor != 0 || m.permsFilter != "" {
		t.Errorf("esc should reset cursor + filter; got cursor=%d filter=%q",
			m.permsCursor, m.permsFilter)
	}
}

func TestPermsKey_FilterModeAccumulates(t *testing.T) {
	m := newTestModel(t)
	m.showPerms = true
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !m.permsFilterMode {
		t.Fatal("'/' should enter filter mode")
	}
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if m.permsFilter != "no" {
		t.Errorf("filter buffer: want \"no\" got %q", m.permsFilter)
	}
}

func TestPermsKey_DownClampsToLastRow(t *testing.T) {
	m := newTestModel(t)
	m.showPerms = true
	m.permsTab = permsTabBuiltin
	// Try to walk past the end.
	for i := 0; i < len(builtinRows)+10; i++ {
		_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}
	if m.permsCursor != len(builtinRows)-1 {
		t.Errorf("cursor should clamp at last row; got %d want %d",
			m.permsCursor, len(builtinRows)-1)
	}
}

func TestPermsKey_UpClampsAtZero(t *testing.T) {
	m := newTestModel(t)
	m.showPerms = true
	for i := 0; i < 5; i++ {
		_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	}
	if m.permsCursor != 0 {
		t.Errorf("cursor should clamp at 0; got %d", m.permsCursor)
	}
}
