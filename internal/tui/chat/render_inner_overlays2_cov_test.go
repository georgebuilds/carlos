package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/frame"
)

// TestRenderInner_FrameSwitcherInView renders the full View with the
// frame switcher open, covering the renderInner switcher branch.
func TestRenderInner_FrameSwitcherInView(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active:    "work",
		Available: []string{"personal", "work", "research"},
	})
	m.openFrameSwitcher()
	out := m.View()
	if !strings.Contains(out, "frames") {
		t.Errorf("View with the switcher open should render the frames header; got:\n%s", out)
	}
	if !strings.Contains(out, "research") {
		t.Errorf("switcher View should list every frame; got:\n%s", out)
	}
}

// TestRenderInner_ModeSwitcherInView covers the renderInner mode-switcher
// branch.
func TestRenderInner_ModeSwitcherInView(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active:    "work",
		Available: []string{"work"},
		Mode:      frame.ModeSolo,
	})
	m.openModeSwitcher()
	out := m.View()
	// The mode switcher labels the three orchestrator modes.
	if !strings.Contains(out, "solo") && !strings.Contains(out, "tight") {
		t.Errorf("View with the mode switcher open should render mode cards; got:\n%s", out)
	}
}

// TestRenderInner_NewFrameWizardInView covers the renderInner new-frame
// wizard branch.
func TestRenderInner_NewFrameWizardInView(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active:    "work",
		Available: []string{"work"},
		AddFrame:  func(frame.Frame) error { return nil },
	})
	m.openNewFrameWizard("")
	out := m.View()
	for _, want := range []string{"name", "glyph", "accent"} {
		if !strings.Contains(out, want) {
			t.Errorf("View with the wizard open should render the %q field; got:\n%s", want, out)
		}
	}
}

// TestRenderInner_ChildrenSplitWideTerminal covers the showSplit
// branch: a live child + a wide enough terminal renders the right-side
// roster panel beside the transcript.
func TestRenderInner_ChildrenSplitWideTerminal(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000CHSPL1"
	seedAgent(t, log, agentID, "split", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource())
	// 140 cols -> innerW well above splitMinWidth (120).
	m = drive(t, m, 140, 40)
	m.childrenSnap = []ChildSnapshot{
		{AgentID: "child-1", LastEvent: "running glob", LastTool: "glob"},
	}
	out := m.View()
	// The roster separator bar should appear in a split layout.
	if !strings.Contains(out, "│") {
		t.Errorf("wide terminal with a live child should render the split panel separator; got:\n%s", out)
	}
}

// TestRenderInner_ChildrenFallbackNarrowTerminal covers the showFallback
// branch: a live child but a terminal below splitMinWidth collapses the
// panel to a dim footer line.
func TestRenderInner_ChildrenFallbackNarrowTerminal(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000CHFB01"
	seedAgent(t, log, agentID, "fallback", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource())
	// 90 cols -> innerW below splitMinWidth -> fallback line.
	m = drive(t, m, 90, 30)
	m.childrenSnap = []ChildSnapshot{
		{AgentID: "child-2", LastEvent: "thinking"},
	}
	// Should render without panicking and without the split separator
	// running the full height (the fallback is a single line).
	out := m.View()
	if out == "" {
		t.Error("narrow terminal with a child should still render a view")
	}
}

// TestRenderInner_ResumeOverlayInView covers the renderInner resume
// branch by stuffing the model with a session list and flipping the flag.
func TestRenderInner_ResumeOverlayInView(t *testing.T) {
	m := innerModel(t)
	m.showResume = true
	m.resumeSessions = []resumeSession{
		{
			ID:        "01HV0000000000000000RES001",
			Model:     "claude-4.7-sonnet",
			UpdatedAt: time.Now().Add(-2 * time.Hour),
			Preview:   "earlier conversation about frames",
			UserMsgs:  3,
		},
	}
	out := m.View()
	if !strings.Contains(out, "earlier conversation about frames") {
		t.Errorf("View with /resume open should render the session preview; got:\n%s", out)
	}
}
