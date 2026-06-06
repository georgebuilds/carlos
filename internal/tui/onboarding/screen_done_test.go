package onboarding

import (
	"strings"
	"testing"
)

func TestDoneModel_RenderNameMentionsUser(t *testing.T) {
	m := newDoneModel()
	out := m.renderName("George")
	if !strings.Contains(out, "Ready, George.") {
		t.Errorf("done screen should greet the configured user; got:\n%s", out)
	}
}

func TestDoneModel_RenderNameMentionsPersonalFrameLive(t *testing.T) {
	m := newDoneModel()
	out := m.renderName("Boss")
	if !strings.Contains(out, "personal frame") {
		t.Errorf("done screen should mention the personal frame is live; got:\n%s", out)
	}
	if !strings.Contains(out, "Config written") {
		t.Errorf("done screen should mention config was written; got:\n%s", out)
	}
}

func TestDoneModel_RenderNameHasNextMovesHint(t *testing.T) {
	m := newDoneModel()
	out := m.renderName("Boss")
	for _, want := range []string{"Ctrl+F", "open the frame switcher", "/frame new", "add a frame"} {
		if !strings.Contains(out, want) {
			t.Errorf("done screen next-moves hint missing %q; got:\n%s", want, out)
		}
	}
}

func TestDoneModel_RenderNameEmptyFallsBackToBoss(t *testing.T) {
	m := newDoneModel()
	out := m.renderName("")
	if !strings.Contains(out, "Ready, Boss.") {
		t.Errorf("empty name should fall back to Boss; got:\n%s", out)
	}
}
