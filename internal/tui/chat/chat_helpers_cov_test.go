package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/tui/slash"
	"github.com/georgebuilds/carlos/internal/usershell"
)

func TestAbsDuration(t *testing.T) {
	if got := absDuration(-3 * time.Second); got != 3*time.Second {
		t.Errorf("absDuration(-3s) = %v", got)
	}
	if got := absDuration(2 * time.Second); got != 2*time.Second {
		t.Errorf("absDuration(2s) = %v", got)
	}
	if got := absDuration(0); got != 0 {
		t.Errorf("absDuration(0) = %v", got)
	}
}

func TestSlashHelpLine_ListsEveryBuiltin(t *testing.T) {
	got := slashHelpLine()
	if !strings.HasPrefix(got, "available:") {
		t.Errorf("help line should start with 'available:'; got %q", got)
	}
	for _, b := range slash.Builtins {
		if !strings.Contains(got, "/"+b.Name) {
			t.Errorf("help line missing /%s; got %q", b.Name, got)
		}
	}
}

func TestWithUserName_OverridesDefaultOnlyWhenNonEmpty(t *testing.T) {
	m := New(nil, "a", NewMemTextSource(), WithUserName("Ada"))
	if m.userName != "Ada" {
		t.Errorf("WithUserName should set userName; got %q", m.userName)
	}
	// Empty string must NOT clobber the default voice.
	def := New(nil, "a", NewMemTextSource())
	withEmpty := New(nil, "a", NewMemTextSource(), WithUserName(""))
	if withEmpty.userName != def.userName {
		t.Errorf("empty WithUserName should keep default %q; got %q", def.userName, withEmpty.userName)
	}
}

func TestWithShellHistory_Wires(t *testing.T) {
	h := &usershell.History{}
	m := New(nil, "a", NewMemTextSource(), WithShellHistory(h))
	if m.shellHistory != h {
		t.Error("WithShellHistory should wire the history walker")
	}
}

func TestFindUserShellEntry(t *testing.T) {
	m := newTestModel(t)
	m.transcript = []transcriptEntry{
		{kind: entryUserMessage, text: "hi"},
		{kind: entryUserShell, shellJobID: "job-A"},
		{kind: entryUserShell, shellJobID: "job-B"},
	}
	if got := m.findUserShellEntry("job-B"); got != 2 {
		t.Errorf("job-B index = %d want 2", got)
	}
	if got := m.findUserShellEntry("job-A"); got != 1 {
		t.Errorf("job-A index = %d want 1", got)
	}
	if got := m.findUserShellEntry("nope"); got != -1 {
		t.Errorf("missing job should be -1; got %d", got)
	}
}

func TestResolveShellJobID(t *testing.T) {
	mgr := minimalManager(t)
	defer mgr.Close()
	job, err := mgr.Submit(context.Background(), "sleep 2", usershell.Foreground)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	id := job.ID

	// Empty arg → no match.
	if got := resolveShellJobID(mgr, ""); got != "" {
		t.Errorf("empty arg should resolve nothing; got %q", got)
	}
	if got := resolveShellJobID(mgr, "j"); got != "" {
		t.Errorf("bare 'j' (empty after strip) should resolve nothing; got %q", got)
	}
	// Full ID matches.
	if got := resolveShellJobID(mgr, id); got != id {
		t.Errorf("full id should resolve to itself; got %q", got)
	}
	// Suffix (with j-prefix) matches case-insensitively.
	suffix := "j" + strings.ToUpper(id[len(id)-6:])
	if got := resolveShellJobID(mgr, suffix); got != id {
		t.Errorf("suffix %q should resolve to %q; got %q", suffix, id, got)
	}
	// No match.
	if got := resolveShellJobID(mgr, "zzzzzzzz"); got != "" {
		t.Errorf("non-matching arg should resolve nothing; got %q", got)
	}
}

func TestShellSlashForeground_NoManager(t *testing.T) {
	m := newTestModel(t)
	msg := m.shellSlashForeground("anything")()
	st, ok := msg.(statusMsg)
	if !ok || !strings.Contains(st.text, "not wired") {
		t.Errorf("no-manager /fg should warn 'not wired'; got %+v", msg)
	}
}

func TestShellSlashForeground_NoMatch(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	msg := m.shellSlashForeground("zzzz")()
	st, ok := msg.(statusMsg)
	if !ok || !strings.Contains(st.text, "no job matches") {
		t.Errorf("/fg with no match should warn; got %+v", msg)
	}
}

func TestShellSlashBackground_NoManager(t *testing.T) {
	m := newTestModel(t)
	msg := m.shellSlashBackground("")()
	st, ok := msg.(statusMsg)
	if !ok || !strings.Contains(st.text, "not wired") {
		t.Errorf("no-manager /bg should warn 'not wired'; got %+v", msg)
	}
}

func TestShellSlashBackground_NoMatch(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	msg := m.shellSlashBackground("zzzz")()
	st, ok := msg.(statusMsg)
	if !ok || !strings.Contains(st.text, "no job matches") {
		t.Errorf("/bg with no match should warn; got %+v", msg)
	}
}

func TestShellSlashForeground_MovesBackgroundJob(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	job, err := m.usershell.Submit(context.Background(), "sleep 5", usershell.Background)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForRunning(t, m.usershell, job.ID)
	msg := m.shellSlashForeground(job.ID)()
	st, ok := msg.(statusMsg)
	if !ok || !strings.Contains(st.text, "foreground") {
		t.Errorf("/fg should report foreground move; got %+v", msg)
	}
}

// waitForRunning polls the manager until the named job reaches the
// running state (PTY spawn is async) or the deadline elapses.
func waitForRunning(t *testing.T, mgr *usershell.Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, err := mgr.Get(id)
		if err == nil && s.State == usershell.StateRunning {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s never reached running state", id)
}
