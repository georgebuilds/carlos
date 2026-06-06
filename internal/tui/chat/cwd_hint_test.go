package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/usershell"
)

func newHintModel(t *testing.T, matcher func(string) string) (*Model, *usershell.Manager) {
	t.Helper()
	mgr := usershell.New(usershell.Options{Cwd: t.TempDir(), Log: nil})
	t.Cleanup(func() { _ = mgr.Close() })
	m := New(struct{ agent.EventLog }{}, "test", NewMemTextSource(),
		WithUserShell(mgr),
		WithFrame(FrameUI{
			Active:    "personal",
			Available: []string{"personal", "work"},
			MatchCwd:  matcher,
		}),
	)
	m.width = 120
	m.height = 30
	return m, mgr
}

func TestRefreshCwdHint_SetsWhenCwdMatchesOtherFrame(t *testing.T) {
	m, _ := newHintModel(t, func(cwd string) string {
		if strings.Contains(cwd, "/work/") {
			return "work"
		}
		return ""
	})
	m.refreshCwdHint("/Users/george/Code/work/api")
	if m.footerHint == "" {
		t.Fatal("expected footer hint to be set")
	}
	if !strings.Contains(m.footerHint, "work") {
		t.Errorf("hint should name target frame; got %q", m.footerHint)
	}
	if !strings.Contains(m.footerHint, "Ctrl+F") {
		t.Errorf("hint should mention Ctrl+F; got %q", m.footerHint)
	}
}

func TestRefreshCwdHint_NoFireWhenCwdMatchesActiveFrame(t *testing.T) {
	m, _ := newHintModel(t, func(string) string { return "personal" })
	m.refreshCwdHint("/anywhere")
	if m.footerHint != "" {
		t.Errorf("hint should be empty for active-frame match; got %q", m.footerHint)
	}
}

func TestRefreshCwdHint_OncePerPath(t *testing.T) {
	calls := 0
	m, _ := newHintModel(t, func(string) string {
		calls++
		return "work"
	})
	m.refreshCwdHint("/work/x")
	if m.footerHint == "" {
		t.Fatal("first call should set hint")
	}
	m.footerHint = "" // simulate next render dropped the hint
	m.refreshCwdHint("/work/x")
	if m.footerHint != "" {
		t.Errorf("second call on same path should NOT re-fire hint; got %q", m.footerHint)
	}
}

func TestLockCwdHints_SuppressesFutureFires(t *testing.T) {
	m, _ := newHintModel(t, func(string) string { return "work" })
	m.lockCwdHints()
	m.refreshCwdHint("/work/x")
	if m.footerHint != "" {
		t.Errorf("locked hints should stay silent; got %q", m.footerHint)
	}
}

func TestRefreshCwdHint_NilMatcherIsNoOp(t *testing.T) {
	m, _ := newHintModel(t, nil)
	m.refreshCwdHint("/anywhere")
	if m.footerHint != "" {
		t.Errorf("nil matcher should leave hint empty; got %q", m.footerHint)
	}
}

func TestTryInterceptCd_UpdatesManagerCwd(t *testing.T) {
	dest := t.TempDir()
	m, mgr := newHintModel(t, nil)
	handled, msg := m.tryInterceptCdCommand("cd " + dest)
	if !handled {
		t.Fatal("simple cd should be intercepted")
	}
	if !strings.Contains(msg, dest) {
		t.Errorf("status should name the new cwd; got %q", msg)
	}
	if got := mgr.Cwd(); got != dest {
		t.Errorf("manager cwd = %q, want %q", got, dest)
	}
}

func TestTryInterceptCd_RejectsCompoundCommand(t *testing.T) {
	m, _ := newHintModel(t, nil)
	for _, cmd := range []string{"cd foo; ls", "cd foo && ls", "cd foo | head", "cd foo > out", "cd `pwd`"} {
		handled, _ := m.tryInterceptCdCommand(cmd)
		if handled {
			t.Errorf("%q should not be intercepted (shell handles it)", cmd)
		}
	}
}

func TestTryInterceptCd_RelativePath(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "child")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	m, mgr := newHintModel(t, nil)
	mgr.SetCwd(base)
	if handled, _ := m.tryInterceptCdCommand("cd child"); !handled {
		t.Fatal("relative cd should be intercepted")
	}
	if got := mgr.Cwd(); got != sub {
		t.Errorf("manager cwd = %q, want %q", got, sub)
	}
}

func TestTryInterceptCd_NonexistentPathReportsError(t *testing.T) {
	m, _ := newHintModel(t, nil)
	handled, msg := m.tryInterceptCdCommand("cd /definitely/does/not/exist/xyz")
	if !handled {
		t.Fatal("cd should still be handled even when path is bad")
	}
	if !strings.Contains(msg, "cd:") {
		t.Errorf("error message should be present; got %q", msg)
	}
}

func TestTryInterceptCd_NoUsershellIsNoOp(t *testing.T) {
	m := New(struct{ agent.EventLog }{}, "test", NewMemTextSource())
	if handled, _ := m.tryInterceptCdCommand("cd /tmp"); handled {
		t.Errorf("intercept should be inert without a manager")
	}
}
