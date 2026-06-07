package onboarding

import (
	"strings"
	"testing"
)

func TestDoneModel_RenderNameMentionsUser(t *testing.T) {
	m := newDoneModel()
	out := m.renderName("George", "")
	if !strings.Contains(out, "Ready, George.") {
		t.Errorf("done screen should greet the configured user; got:\n%s", out)
	}
}

func TestDoneModel_RenderNameMentionsPersonalFrameLive(t *testing.T) {
	m := newDoneModel()
	out := m.renderName("Boss", "")
	if !strings.Contains(out, "personal frame") {
		t.Errorf("done screen should mention the personal frame is live; got:\n%s", out)
	}
	if !strings.Contains(out, "Config written") {
		t.Errorf("done screen should mention config was written; got:\n%s", out)
	}
}

func TestDoneModel_RenderNameHasNextMovesHint(t *testing.T) {
	m := newDoneModel()
	out := m.renderName("Boss", "")
	for _, want := range []string{"Ctrl+F", "open the frame switcher", "/frame new", "add a frame"} {
		if !strings.Contains(out, want) {
			t.Errorf("done screen next-moves hint missing %q; got:\n%s", want, out)
		}
	}
}

func TestDoneModel_RenderNameEmptyFallsBackToBoss(t *testing.T) {
	m := newDoneModel()
	out := m.renderName("", "")
	if !strings.Contains(out, "Ready, Boss.") {
		t.Errorf("empty name should fall back to Boss; got:\n%s", out)
	}
}

// --- O-9: actual config path + carlos --help hint ---

func TestDoneModel_RendersExplicitConfigPath(t *testing.T) {
	m := newDoneModel()
	out := m.renderName("Boss", "/tmp/custom/config.yaml")
	if !strings.Contains(out, "/tmp/custom/config.yaml") {
		t.Errorf("done screen should print the explicit config path; got:\n%s", out)
	}
}

func TestDoneModel_EmptyConfigPathFallsBackToDefault(t *testing.T) {
	m := newDoneModel()
	out := m.renderName("Boss", "")
	// We don't pin the exact default (it changes with HOME), but the
	// fallback should still surface SOME path containing "config.yaml".
	if !strings.Contains(out, "config.yaml") {
		t.Errorf("empty configPath should fall back to a path containing config.yaml; got:\n%s", out)
	}
}

func TestDoneModel_CarlosHelpHintRendered(t *testing.T) {
	m := newDoneModel()
	out := m.renderName("Boss", "")
	for _, want := range []string{"carlos --help", "cli"} {
		if !strings.Contains(out, want) {
			t.Errorf("done screen should mention %q (O-9 hint); got:\n%s", want, out)
		}
	}
}

func TestDoneModel_NextMovesListsAllThreeRows(t *testing.T) {
	m := newDoneModel()
	out := m.renderName("Boss", "")
	// Each of the three "next moves" rows should be on its own line so
	// the user can scan them. We assert presence; layout is owned by
	// lipgloss.JoinVertical and not pinned here.
	for _, want := range []string{"Ctrl+F", "/frame new", "carlos --help"} {
		if !strings.Contains(out, want) {
			t.Errorf("next-moves row %q missing; got:\n%s", want, out)
		}
	}
}
