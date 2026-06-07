package onboarding

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestDaemonScreen_RendersNewCopy proves the consequence box copy from
// the 2026-06-06 onboarding refinements proposal renders end-to-end.
func TestDaemonScreen_RendersNewCopy(t *testing.T) {
	m := newDaemonModel()
	out := stripStyle(m.View())
	for _, want := range []string{
		"the daemon runs in the background",
		"scheduled runs",
		"telegram / ntfy / signal",
		"daily digest",
		"without the daemon",
		"enable now?",
		"[enter]",
		"no, just the tui for now",
		"[y]",
		"yes, install the background service",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("daemon copy missing %q; got:\n%s", want, out)
		}
	}
}

// TestDaemonScreen_EnterIsTheDefaultNo confirms enter still produces
// enabled=false; the proposal aligns enter (default) with "no, just the
// tui for now".
func TestDaemonScreen_EnterIsTheDefaultNo(t *testing.T) {
	m := newDaemonModel()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should advance the flow")
	}
	mm := next.(daemonModel)
	if mm.choice {
		t.Errorf("enter is the default-no; got choice=true")
	}
}
