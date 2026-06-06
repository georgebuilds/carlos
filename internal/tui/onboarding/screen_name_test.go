package onboarding

import (
	"strings"
	"testing"
)

func TestNameModel_ViewMentionsPersonalFrame(t *testing.T) {
	m := newNameModel("Boss")
	out := m.View()
	if !strings.Contains(out, "personal frame") {
		t.Errorf("name screen should introduce the personal frame concept; got:\n%s", out)
	}
}

func TestNameModel_ViewMentionsFrameSwitcherAffordances(t *testing.T) {
	m := newNameModel("Boss")
	out := m.View()
	for _, want := range []string{"/frame new", "Ctrl+F"} {
		if !strings.Contains(out, want) {
			t.Errorf("name screen should hint at %q; got:\n%s", want, out)
		}
	}
}

func TestNameModel_ViewPressEnterToContinue(t *testing.T) {
	m := newNameModel("Boss")
	out := m.View()
	if !strings.Contains(out, "press enter to continue") {
		t.Errorf("name screen should keep the press-enter call-to-action; got:\n%s", out)
	}
}
