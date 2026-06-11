package chat

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/workspace"
)

func innerModel(t *testing.T) *Model {
	t.Helper()
	log := openTempLog(t)
	const agentID = "01HV0000000000000000INNER0"
	seedAgent(t, log, agentID, "inner", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource())
	return drive(t, m, 120, 36)
}

func TestRenderInner_JobsOverlayInView(t *testing.T) {
	m := innerModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	m.showJobs = true
	out := m.View()
	if !strings.Contains(out, "Shell jobs") {
		t.Errorf("View with showJobs should render the jobs overlay; got:\n%s", out)
	}
}

func TestRenderInner_PermissionsOverlayInView(t *testing.T) {
	m := innerModel(t)
	m.showPerms = true
	out := m.View()
	if !strings.Contains(out, "Permissions") {
		t.Errorf("View with showPerms should render the permissions overlay; got:\n%s", out)
	}
}

func TestRenderInner_HelpOverlayInView(t *testing.T) {
	m := innerModel(t)
	m.showHelp = true
	out := m.View()
	// The help box lists slash verbs; /help should be present.
	if !strings.Contains(out, "/help") && !strings.Contains(out, "help") {
		t.Errorf("View with showHelp should render the help box; got:\n%s", out)
	}
}

func TestRenderInner_FirstTrustPromptInView(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	store := workspace.NewStore(filepath.Join(dir, "t.json"))
	policy := workspace.NewPolicy(store, cwd)

	log := openTempLog(t)
	const agentID = "01HV0000000000000000INNER1"
	seedAgent(t, log, agentID, "inner1", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource(), WithWorkspacePolicy(policy))
	m = drive(t, m, 120, 36)
	m.showFirstTrust = true
	out := m.View()
	// The first-trust prompt mentions the cwd leaf.
	if !strings.Contains(out, filepath.Base(cwd)) {
		t.Errorf("View with showFirstTrust should render the trust prompt; got:\n%s", out)
	}
}

func TestRenderPermsBuiltinBody_FilterNoMatches(t *testing.T) {
	out := renderPermsBuiltinBody("zzz-no-such-tool", 0, 60)
	if !strings.Contains(out, "no matches") {
		t.Errorf("non-matching filter should show the no-matches hint; got:\n%s", out)
	}
}

func TestRenderPermsBuiltinBody_RendersRows(t *testing.T) {
	out := renderPermsBuiltinBody("", 0, 60)
	if !strings.Contains(out, "notes_search") {
		t.Errorf("empty filter should render every builtin row; got:\n%s", out)
	}
}

func TestRenderPermissionsOverlay_FilterModeCaret(t *testing.T) {
	out := renderPermissionsOverlay(permsTabBuiltin, nil, "notes", true, 0, 80)
	if !strings.Contains(out, "filter: ") {
		t.Errorf("filter mode should render the filter row; got:\n%s", out)
	}
}
