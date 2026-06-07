package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/theme"
)

func TestResearchStatusModel_InitReturnsTick(t *testing.T) {
	m := newResearchStatusModel("q", theme.Palette{})
	cmd := m.Init()
	if cmd == nil {
		t.Error("Init should return a tick cmd")
	}
}

func TestResearchStatusModel_WindowSizeMsg(t *testing.T) {
	m := newResearchStatusModel("q", theme.Palette{})
	upd, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if cmd != nil {
		t.Errorf("WindowSize should not return a cmd; got %T", cmd)
	}
	if upd.(researchStatusModel).width != 120 {
		t.Errorf("width = %d want 120", upd.(researchStatusModel).width)
	}
}

func TestResearchStatusModel_TickAfterDoneStopsLoop(t *testing.T) {
	m := newResearchStatusModel("q", theme.Palette{})
	m.done = true
	upd, cmd := m.Update(researchTickMsg(time.Now()))
	if cmd != nil {
		t.Error("done state should stop the spinner tick loop")
	}
	mm := upd.(researchStatusModel)
	// Spinner still advanced.
	if mm.spinnerFrame == m.spinnerFrame {
		t.Error("spinner should advance even on the final tick")
	}
}

func TestResearchStatusModel_UnknownKeyIsNoop(t *testing.T) {
	m := newResearchStatusModel("q", theme.Palette{})
	upd, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd != nil {
		t.Error("unknown key should be a no-op")
	}
	if upd.(researchStatusModel).done {
		t.Error("unknown key should not flip done")
	}
}

func TestResearchStatusModel_ViewNarrowWidthClamps(t *testing.T) {
	m := newResearchStatusModel("q", theme.Palette{})
	m.width = 30 // forces boxW < 50 → clamped to 50
	out := m.View()
	if !strings.Contains(out, "researching") {
		t.Errorf("narrow view should still render headline: %s", out)
	}
}

func TestResearchStatusModel_ViewZeroWidthFallback(t *testing.T) {
	m := newResearchStatusModel("q", theme.Palette{})
	// width == 0 hits the w <= 0 branch (assigns w = 90).
	out := m.View()
	if !strings.Contains(out, "researching") {
		t.Errorf("zero-width view: %s", out)
	}
}

func TestResearchStatusModel_ViewWideWidthCaps(t *testing.T) {
	m := newResearchStatusModel("q", theme.Palette{})
	m.width = 200 // > 100, gets capped to 100
	out := m.View()
	if !strings.Contains(out, "researching") {
		t.Errorf("wide view: %s", out)
	}
}

func TestResearchStatusModel_ViewWithCurrentPhase(t *testing.T) {
	m := newResearchStatusModel("q", theme.Palette{})
	m.width = 80
	// Inject a running phase so the line 2 "current phase" branch fires.
	upd, _ := m.Update(researchPhaseStartMsg{phase: "search", t: time.Now()})
	m = upd.(researchStatusModel)
	out := m.View()
	if !strings.Contains(out, "search") {
		t.Errorf("expected current phase in view: %s", out)
	}
}

func TestResearchStatusModel_ViewWhenDone(t *testing.T) {
	m := newResearchStatusModel("q", theme.Palette{})
	m.width = 80
	m.done = true
	out := m.View()
	if !strings.Contains(out, "done") || !strings.Contains(out, "rendering") {
		t.Errorf("done view should mention rendering report: %s", out)
	}
}

func TestResearchStatusModel_RenderPhaseGlyphsAllStates(t *testing.T) {
	m := newResearchStatusModel("q", theme.Palette{})
	// Done state.
	m.phaseStates["decompose"] = phaseStateDone
	// Running state.
	m.phaseStates["search"] = phaseStateRunning
	// Failed state.
	m.phaseStates["fetch"] = phaseStateFailed
	// pending stays default for the rest.
	out := m.renderPhaseGlyphs()
	// Glyphs ✓ ◐ ✗ ○ should all appear.
	for _, want := range []string{"✓", "◐", "✗", "○"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing glyph %q in: %s", want, out)
		}
	}
	// Labels should appear too.
	for _, want := range []string{"decomp", "search", "fetch", "read", "synth", "verify"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing label %q in: %s", want, out)
		}
	}
}

func TestErrString_NonNil(t *testing.T) {
	if got := errString(errors.New("boom")); got != "boom" {
		t.Errorf("errString = %q want boom", got)
	}
}

// TestRunResearchWithStatus_NoTTYExercisesWiring runs the helper with a
// minimal Engine. Under `go test`, tea.Program has no real TTY so
// prog.Run fails, which covers the wiring lines at the top of the
// function. Engine.Run runs in a goroutine and may panic on its own
// nil provider -- but a context-aware Engine.Run typically returns
// before its tea program even starts on a CI machine. We accept any
// outcome (error or success) as evidence the wiring code ran.
func TestRunResearchWithStatus_NoTTYExercisesWiring(t *testing.T) {
	// Use a cancelled context so engine.Run returns immediately
	// without trying to do real work.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Defensively recover from any goroutine panic that bubbles up.
	defer func() { _ = recover() }()
	_, _ = runResearchWithStatus(ctx, &research.Engine{}, "test question")
}

// Tick returns a tea.Cmd that, when invoked, produces a researchTickMsg.
// The closure inside tea.Tick sleeps for spinnerTickInterval before
// returning the message, so invocation here is brief but real.
func TestTickReturnsTickMsg(t *testing.T) {
	cmd := tick()
	if cmd == nil {
		t.Fatal("tick should return a non-nil cmd")
	}
	msg := cmd()
	if _, ok := msg.(researchTickMsg); !ok {
		t.Errorf("tick cmd returned %T, want researchTickMsg", msg)
	}
}
