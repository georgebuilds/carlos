package chat

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/workspace"
)

// permsModel builds a model whose workspace policy is rooted at a temp
// cwd, optionally pre-trusting it, and opens the perms overlay on the
// Workspace tab.
func permsModel(t *testing.T, trustCwd bool) (*Model, string) {
	t.Helper()
	dir := t.TempDir()
	cwd := t.TempDir()
	store := workspace.NewStore(filepath.Join(dir, "trust.json"))
	if trustCwd {
		if err := store.Trust(cwd); err != nil {
			t.Fatalf("trust: %v", err)
		}
	}
	policy := workspace.NewPolicy(store, cwd)
	m := newTestModel(t)
	m.workspace = policy
	m.showPerms = true
	m.permsTab = permsTabWorkspace
	return m, cwd
}

func TestActiveRowsCount_BuiltinAndWorkspace(t *testing.T) {
	m, _ := permsModel(t, true)
	m.permsTab = permsTabBuiltin
	if got := m.activeRowsCount(); got != len(builtinRows) {
		t.Errorf("builtin row count = %d want %d", got, len(builtinRows))
	}
	m.permsTab = permsTabWorkspace
	if got := m.activeRowsCount(); got != 1 {
		t.Errorf("workspace row count = %d want 1", got)
	}
}

func TestPermsTrustCwd_TrustsAndFlipsPolicy(t *testing.T) {
	m, cwd := permsModel(t, false)
	if m.workspace.IsTrusted() {
		t.Fatal("precondition: cwd should start untrusted")
	}
	_, cmd, handled := m.permsTrustCwd()
	if !handled || cmd == nil {
		t.Fatal("permsTrustCwd should issue a command")
	}
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "trusted") {
		t.Errorf("trust should report success; got %+v", st)
	}
	if !m.workspace.IsTrusted() {
		t.Error("policy should be flipped to trusted")
	}
	// And the store should now list the cwd.
	ok2, _ := m.workspace.Store().IsTrusted(cwd)
	if !ok2 {
		t.Error("store should now contain the trusted cwd")
	}
}

func TestPermsTrustCwd_NoPolicyNoOp(t *testing.T) {
	m := newTestModel(t)
	_, cmd, handled := m.permsTrustCwd()
	if !handled || cmd != nil {
		t.Errorf("no-policy trust should be a no-op; cmd=%v", cmd)
	}
}

func TestPermsUntrustHighlighted_RemovesEntry(t *testing.T) {
	m, cwd := permsModel(t, true)
	if !m.workspace.IsTrusted() {
		t.Fatal("precondition: cwd should be trusted")
	}
	m.permsCursor = 0
	_, cmd, handled := m.permsUntrustHighlighted()
	if !handled || cmd == nil {
		t.Fatal("untrust should issue a command")
	}
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "untrusted") {
		t.Errorf("untrust should report success; got %+v", st)
	}
	// The current cwd was untrusted → policy flips off.
	if m.workspace.IsTrusted() {
		t.Error("untrusting the current cwd should flip the policy off")
	}
	ok2, _ := m.workspace.Store().IsTrusted(cwd)
	if ok2 {
		t.Error("store should no longer contain the cwd")
	}
}

func TestPermsUntrustHighlighted_NoPolicyNoOp(t *testing.T) {
	m := newTestModel(t)
	_, cmd, handled := m.permsUntrustHighlighted()
	if !handled || cmd != nil {
		t.Errorf("no-policy untrust should be a no-op; cmd=%v", cmd)
	}
}

func TestPermsUntrustHighlighted_EmptyRowsNoOp(t *testing.T) {
	m, _ := permsModel(t, false) // no trusted rows
	m.permsCursor = 0
	_, cmd, handled := m.permsUntrustHighlighted()
	if !handled || cmd != nil {
		t.Errorf("untrust with no rows should be a no-op; cmd=%v", cmd)
	}
}

func TestPermsKey_DTriggersUntrustOnWorkspaceTab(t *testing.T) {
	m, _ := permsModel(t, true)
	m.permsCursor = 0
	_, cmd, handled := m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if !handled || cmd == nil {
		t.Fatal("'d' on Workspace tab should trigger untrust command")
	}
}

func TestPermsKey_DIsNoOpOnBuiltinTab(t *testing.T) {
	m, _ := permsModel(t, true)
	m.permsTab = permsTabBuiltin
	_, cmd, handled := m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if !handled || cmd != nil {
		t.Errorf("'d' on Built-in tab should be a no-op; cmd=%v", cmd)
	}
}

func TestPermsKey_TTriggersTrustOnWorkspaceTab(t *testing.T) {
	m, _ := permsModel(t, false)
	_, cmd, handled := m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if !handled || cmd == nil {
		t.Fatal("'t' on Workspace tab should trigger trust command")
	}
}

func TestPermsKey_TIsNoOpOnBuiltinTab(t *testing.T) {
	m, _ := permsModel(t, false)
	m.permsTab = permsTabBuiltin
	_, cmd, handled := m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if !handled || cmd != nil {
		t.Errorf("'t' on Built-in tab should be a no-op; cmd=%v", cmd)
	}
}

func TestPermsKey_GAndShiftGJump(t *testing.T) {
	m, _ := permsModel(t, true)
	m.permsTab = permsTabBuiltin
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if m.permsCursor != len(builtinRows)-1 {
		t.Errorf("G should jump to last builtin row; got %d", m.permsCursor)
	}
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if m.permsCursor != 0 {
		t.Errorf("g should jump to top; got %d", m.permsCursor)
	}
}

func TestPermsKey_FilterBackspaceEscEnterSpace(t *testing.T) {
	m, _ := permsModel(t, true)
	m.permsTab = permsTabBuiltin
	// Enter filter mode and type "no tes".
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("no")})
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeySpace})
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tes")})
	if m.permsFilter != "no tes" {
		t.Fatalf("filter buffer = %q want 'no tes'", m.permsFilter)
	}
	// Backspace trims.
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.permsFilter != "no te" {
		t.Errorf("backspace failed; got %q", m.permsFilter)
	}
	// Enter exits filter mode but keeps the overlay + filter.
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.permsFilterMode {
		t.Error("enter should exit filter mode")
	}
	if !m.showPerms {
		t.Error("enter in filter mode must NOT close the overlay")
	}
	// Re-enter and esc out of filter mode (overlay stays open).
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	_, _, _ = m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.permsFilterMode {
		t.Error("esc in filter mode should leave filter mode")
	}
	if !m.showPerms {
		t.Error("esc in filter mode must NOT close the overlay")
	}
}

func TestPermsKey_CtrlCFallsThrough(t *testing.T) {
	m, _ := permsModel(t, true)
	_, _, handled := m.handlePermsOverlayKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if handled {
		t.Error("ctrl+c must fall through so the user can still quit")
	}
}
