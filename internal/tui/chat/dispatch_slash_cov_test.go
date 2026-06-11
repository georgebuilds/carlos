package chat

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/tui/slash"
)

func TestDispatchSlash_ClearResetsTranscript(t *testing.T) {
	m := newTestModel(t)
	m.transcript = []transcriptEntry{{kind: entryUserMessage, text: "old"}}
	cmd := m.dispatchSlash(slash.Command{Name: "clear"})
	if len(m.transcript) != 0 {
		t.Errorf("/clear should empty the transcript; got %d", len(m.transcript))
	}
	if cmd == nil {
		t.Fatal("/clear should return a command (the reset-append)")
	}
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "cleared") {
		t.Errorf("/clear status should confirm; got %+v", st)
	}
}

func TestDispatchSlash_ExitQuits(t *testing.T) {
	m := newTestModel(t)
	cmd := m.dispatchSlash(slash.Command{Name: "exit"})
	if !m.quitting {
		t.Error("/exit should set quitting")
	}
	if cmd == nil {
		t.Error("/exit should return tea.Quit")
	}
}

func TestDispatchSlash_JobsNoManagerWarns(t *testing.T) {
	m := newTestModel(t)
	cmd := m.dispatchSlash(slash.Command{Name: "jobs"})
	if m.showJobs {
		t.Error("/jobs without a manager should not open the overlay")
	}
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "not wired") {
		t.Errorf("/jobs without manager should warn; got %+v", st)
	}
}

func TestDispatchSlash_JobsTogglesWithManager(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	if cmd := m.dispatchSlash(slash.Command{Name: "jobs"}); cmd != nil {
		t.Errorf("/jobs toggle should return nil; got %T", cmd)
	}
	if !m.showJobs {
		t.Error("/jobs should open the overlay")
	}
	_ = m.dispatchSlash(slash.Command{Name: "jobs"})
	if m.showJobs {
		t.Error("/jobs again should close the overlay")
	}
}

func TestDispatchSlash_PermissionsToggles(t *testing.T) {
	m := newTestModel(t)
	_ = m.dispatchSlash(slash.Command{Name: "permissions"})
	if !m.showPerms || m.permsTab != permsTabBuiltin {
		t.Errorf("/permissions should open on the Built-in tab; showPerms=%v tab=%v", m.showPerms, m.permsTab)
	}
	_ = m.dispatchSlash(slash.Command{Name: "permissions"})
	if m.showPerms {
		t.Error("/permissions again should close")
	}
}

func TestDispatchSlash_ShellEmptyUsage(t *testing.T) {
	m := newTestModel(t)
	cmd := m.dispatchSlash(slash.Command{Name: "shell", Args: "  "})
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "usage: /shell") {
		t.Errorf("/shell with no body should print usage; got %+v", st)
	}
}

func TestDispatchSlash_ShellWithBodyRoutesToUserShell(t *testing.T) {
	m := newTestModel(t)
	cmd := m.dispatchSlash(slash.Command{Name: "shell", Args: "ls -la"})
	// No manager wired → "not wired" status.
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "not wired") {
		t.Errorf("/shell without manager should warn not-wired; got %+v", st)
	}
}

func TestDispatchSlash_ResearchEmptyUsage(t *testing.T) {
	m := newTestModel(t)
	cmd := m.dispatchSlash(slash.Command{Name: "research", Args: ""})
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "usage: /research") {
		t.Errorf("/research with no question should print usage; got %+v", st)
	}
}

func TestDispatchSlash_ResearchNotWired(t *testing.T) {
	m := newTestModel(t)
	cmd := m.dispatchSlash(slash.Command{Name: "research", Args: "why is the sky blue"})
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "not wired") {
		t.Errorf("/research without engine should warn; got %+v", st)
	}
}

func TestDispatchSlash_CompactNotConfigured(t *testing.T) {
	m := newTestModel(t)
	cmd := m.dispatchSlash(slash.Command{Name: "compact"})
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "summarizer") {
		t.Errorf("/compact without summarizer should warn; got %+v", st)
	}
}

func TestDispatchSlash_FgBgNoManager(t *testing.T) {
	m := newTestModel(t)
	fg := m.dispatchSlash(slash.Command{Name: "fg", Args: "1"})
	if st, ok := fg().(statusMsg); !ok || !strings.Contains(st.text, "not wired") {
		t.Errorf("/fg without manager should warn; got %+v", st)
	}
	bg := m.dispatchSlash(slash.Command{Name: "bg"})
	if st, ok := bg().(statusMsg); !ok || !strings.Contains(st.text, "not wired") {
		t.Errorf("/bg without manager should warn; got %+v", st)
	}
}

func TestDispatchSlash_ScheduleListEchoes(t *testing.T) {
	m := newTestModel(t)
	cmd := m.dispatchSlash(slash.Command{Name: "schedule", Args: "list"})
	if cmd == nil {
		t.Fatal("/schedule should return a status command")
	}
	if _, ok := cmd().(statusMsg); !ok {
		t.Error("/schedule should produce a statusMsg")
	}
}
