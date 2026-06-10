package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/theme"
)

func TestShortPhaseLabel(t *testing.T) {
	cases := map[string]string{
		"decompose":  "decomp",
		"route":      "route",
		"synthesize": "synth",
		"search":     "search",
		"verify":     "verify",
		"fetch":      "fetch",
		"read":       "read",
		"unknown":    "unknown",
	}
	for in, want := range cases {
		if got := shortPhaseLabel(in); got != want {
			t.Errorf("shortPhaseLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatResearchDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0.0s"},
		{500 * time.Millisecond, "0.5s"},
		{45 * time.Second, "45.0s"},
		{90 * time.Second, "1m30s"},
		{-1 * time.Second, "0.0s"},
	}
	for _, tc := range cases {
		if got := formatResearchDuration(tc.d); got != tc.want {
			t.Errorf("formatResearchDuration(%v) = %q want %q", tc.d, got, tc.want)
		}
	}
}

func TestTruncateOneLineForResearch(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"hi", 10, "hi"},
		{"hello world", 5, "hell…"},
		{"foo\nbar", 8, "foo bar"},
		{"abc", 1, "…"},
		{"abc", 0, "abc"},
	}
	for _, tc := range cases {
		if got := truncateOneLineForResearch(tc.in, tc.max); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

func TestResearchStatusModel_PhaseTransitions(t *testing.T) {
	m := newResearchStatusModel("test", theme.Palette{})
	// Start a phase.
	updated, _ := m.Update(researchPhaseStartMsg{phase: "search", t: time.Now()})
	m = updated.(researchStatusModel)
	if m.phaseStates["search"] != phaseStateRunning {
		t.Errorf("search not running: %v", m.phaseStates)
	}
	if m.currentPhase != "search" {
		t.Errorf("currentPhase: %q", m.currentPhase)
	}
	// Done.
	updated, _ = m.Update(researchPhaseDoneMsg{phase: "search", elapsed: time.Second})
	m = updated.(researchStatusModel)
	if m.phaseStates["search"] != phaseStateDone {
		t.Errorf("expected done state; got %v", m.phaseStates["search"])
	}
	if m.currentPhase != "" {
		t.Errorf("currentPhase should clear after done: %q", m.currentPhase)
	}
}

func TestResearchStatusModel_PhaseFailureRecorded(t *testing.T) {
	m := newResearchStatusModel("q", theme.Palette{})
	updated, _ := m.Update(researchPhaseStartMsg{phase: "fetch", t: time.Now()})
	m = updated.(researchStatusModel)
	updated, _ = m.Update(researchPhaseDoneMsg{
		phase:   "fetch",
		elapsed: 2 * time.Second,
		errMsg:  "timeout",
	})
	m = updated.(researchStatusModel)
	if m.phaseStates["fetch"] != phaseStateFailed {
		t.Errorf("expected failed state; got %v", m.phaseStates["fetch"])
	}
	if m.phaseErrs["fetch"] != "timeout" {
		t.Errorf("phase err: %q", m.phaseErrs["fetch"])
	}
}

func TestResearchStatusModel_DoneTriggersQuit(t *testing.T) {
	m := newResearchStatusModel("q", theme.Palette{})
	_, cmd := m.Update(researchDoneMsg{})
	if cmd == nil {
		t.Error("done should return a Quit cmd")
	}
}

func TestResearchStatusModel_CtrlCQuits(t *testing.T) {
	m := newResearchStatusModel("q", theme.Palette{})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("ctrl+c should return a Quit cmd")
	}
}

func TestResearchStatusModel_SpinnerAdvances(t *testing.T) {
	m := newResearchStatusModel("q", theme.Palette{})
	prev := m.spinnerFrame
	updated, _ := m.Update(researchTickMsg(time.Now()))
	m = updated.(researchStatusModel)
	if m.spinnerFrame == prev {
		t.Error("spinner frame did not advance")
	}
}

func TestResearchStatusModel_ViewContainsHeadline(t *testing.T) {
	m := newResearchStatusModel("steak in tulsa", theme.Palette{})
	m.width = 90
	out := m.View()
	if !strings.Contains(out, "researching") {
		t.Errorf("headline missing: %s", out)
	}
	if !strings.Contains(out, "decomp") || !strings.Contains(out, "verify") {
		t.Errorf("phase glyphs missing: %s", out)
	}
}

func TestErrString(t *testing.T) {
	if got := errString(nil); got != "" {
		t.Errorf("nil errString: %q", got)
	}
}
