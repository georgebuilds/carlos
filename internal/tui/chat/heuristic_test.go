package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
)

// longPrompt builds a body that comfortably exceeds the char threshold
// so the heuristic fires. The exact contents don't matter — just length.
func longPrompt() string {
	return "please audit the cursor module for parity with helix's selection model, " +
		"then draft a patch and write a regression test"
}

func TestShouldShowHeuristic_GatingRules(t *testing.T) {
	long := longPrompt()
	if len(long) <= heuristicCharThreshold {
		t.Fatalf("test fixture too short: %d <= %d", len(long), heuristicCharThreshold)
	}

	cases := []struct {
		name     string
		mode     string
		prompt   string
		disabled bool
		want     bool
	}{
		{"orchestrator+long fires", "orchestrator", long, false, true},
		{"solo skips", "solo", long, false, false},
		{"tight skips", "tight", long, false, false},
		{"empty mode skips", "", long, false, false},
		{"short prompt skips", "orchestrator", "what time is it", false, false},
		{"exactly threshold skips", "orchestrator", strings.Repeat("a", heuristicCharThreshold), false, false},
		{"one over threshold fires", "orchestrator", strings.Repeat("a", heuristicCharThreshold+1), false, true},
		{"disabled mutes", "orchestrator", long, true, false},
		{"whitespace-only skips", "orchestrator", strings.Repeat(" ", 200), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldShowHeuristic(c.mode, c.prompt, c.disabled); got != c.want {
				t.Errorf("shouldShowHeuristic(%q, len=%d, disabled=%v) = %v, want %v",
					c.mode, len(c.prompt), c.disabled, got, c.want)
			}
		})
	}
}

func TestHeuristicYesCountAndDefault(t *testing.T) {
	var checks [heuristicQuestionCount]bool
	if heuristicYesCount(checks) != 0 {
		t.Fatalf("zero-value yes count != 0")
	}
	if heuristicDefaultDelegate(checks) {
		t.Fatalf("zero-value should default to solo")
	}

	checks[0] = true
	checks[2] = true
	if got := heuristicYesCount(checks); got != 2 {
		t.Errorf("yes=2 got %d", got)
	}
	if heuristicDefaultDelegate(checks) {
		t.Errorf("yes=2 below threshold should default to solo")
	}

	checks[4] = true
	if got := heuristicYesCount(checks); got != 3 {
		t.Errorf("yes=3 got %d", got)
	}
	if !heuristicDefaultDelegate(checks) {
		t.Errorf("yes=3 should default to delegate")
	}

	for i := range checks {
		checks[i] = true
	}
	if got := heuristicYesCount(checks); got != heuristicQuestionCount {
		t.Errorf("all-on count got %d", got)
	}
	if !heuristicDefaultDelegate(checks) {
		t.Errorf("all-on should default to delegate")
	}
}

func newOrchestratorModel(t *testing.T) (*Model, *agent.SQLiteEventLog, string) {
	t.Helper()
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000A01"
	seedAgent(t, log, agentID, "heuristic test", "fake")
	m := New(log, agentID, NewMemTextSource(), WithFrame(FrameUI{
		Active: "work",
		Mode:   "orchestrator",
	}))
	m = drive(t, m, 120, 30)
	return m, log, agentID
}

// TestSubmit_OrchestratorOpensHeuristic walks the trigger path: long
// prompt + orchestrator mode → overlay opens and the prompt is stashed
// instead of immediately landing in the log.
func TestSubmit_OrchestratorOpensHeuristic(t *testing.T) {
	m, log, agentID := newOrchestratorModel(t)
	pre := countUserMessages(t, log, agentID)

	m.ta.SetValue(longPrompt())
	cmd := m.submit()
	if cmd != nil {
		t.Fatalf("submit returned non-nil cmd; expected overlay-only side effect")
	}
	if !m.showHeuristic {
		t.Errorf("showHeuristic not set")
	}
	if m.heuristicPending != longPrompt() {
		t.Errorf("pending prompt = %q, want %q", m.heuristicPending, longPrompt())
	}
	if v := m.ta.Value(); v != "" {
		t.Errorf("composer not cleared while overlay holds prompt: %q", v)
	}
	if got := countUserMessages(t, log, agentID); got != pre {
		t.Errorf("submit appended user_message before user picked an action: pre=%d post=%d", pre, got)
	}
}

func TestSubmit_SoloModeSkipsOverlay(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000A02"
	seedAgent(t, log, agentID, "solo test", "fake")
	m := New(log, agentID, NewMemTextSource(), WithFrame(FrameUI{
		Active: "personal",
		Mode:   "solo",
	}))
	m = drive(t, m, 120, 30)

	m.ta.SetValue(longPrompt())
	cmd := m.submit()
	if cmd == nil {
		t.Fatalf("solo submit returned nil cmd")
	}
	if m.showHeuristic {
		t.Errorf("showHeuristic set in solo mode")
	}
	if msg := cmd(); msg != nil {
		if em, ok := msg.(errMsg); ok && em.err != nil {
			t.Fatalf("submit Cmd errored: %v", em.err)
		}
	}
	if got := countUserMessages(t, log, agentID); got != 1 {
		t.Errorf("solo submit didn't append user_message: got %d", got)
	}
}

func TestSubmit_ShortPromptSkipsOverlayEvenInOrchestrator(t *testing.T) {
	m, log, agentID := newOrchestratorModel(t)

	m.ta.SetValue("what time is it")
	cmd := m.submit()
	if cmd == nil {
		t.Fatalf("short prompt submit returned nil cmd")
	}
	if m.showHeuristic {
		t.Errorf("showHeuristic set for short prompt")
	}
	_ = cmd()
	if got := countUserMessages(t, log, agentID); got != 1 {
		t.Errorf("short prompt didn't go through: got %d", got)
	}
}

func TestSubmit_DisabledMutesOverlay(t *testing.T) {
	m, log, agentID := newOrchestratorModel(t)
	m.heuristicDisabled = true

	m.ta.SetValue(longPrompt())
	cmd := m.submit()
	if cmd == nil {
		t.Fatalf("submit returned nil cmd when heuristic disabled")
	}
	if m.showHeuristic {
		t.Errorf("showHeuristic set when disabled")
	}
	_ = cmd()
	if got := countUserMessages(t, log, agentID); got != 1 {
		t.Errorf("disabled path didn't dispatch: got %d", got)
	}
}

// TestHeuristicToggleUpdatesCount asserts a key press for each question
// flips the corresponding check and updates the default action when the
// threshold is crossed.
func TestHeuristicToggleUpdatesCount(t *testing.T) {
	m, _, _ := newOrchestratorModel(t)
	m.openHeuristic(longPrompt())

	for i := 1; i <= heuristicQuestionCount; i++ {
		key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune('0' + i)}}
		next, _, handled := m.handleHeuristicKey(key)
		if !handled {
			t.Fatalf("key %d not handled", i)
		}
		m = next.(*Model)
		if !m.heuristicChecks[i-1] {
			t.Errorf("toggle for question %d didn't flip the check", i)
		}
		if got := heuristicYesCount(m.heuristicChecks); got != i {
			t.Errorf("after %d toggles yes=%d", i, got)
		}
	}
	if !heuristicDefaultDelegate(m.heuristicChecks) {
		t.Errorf("all-on should default to delegate")
	}

	next, _, _ := m.handleHeuristicKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m = next.(*Model)
	if m.heuristicChecks[0] {
		t.Errorf("re-toggle of question 1 didn't clear")
	}
}

// TestHeuristicDelegateInjectsAddendum walks the delegate commit path
// and asserts the user_message event payload starts with the addendum
// before the original prompt body.
func TestHeuristicDelegateInjectsAddendum(t *testing.T) {
	m, log, agentID := newOrchestratorModel(t)
	prompt := longPrompt()
	m.openHeuristic(prompt)

	cmd := m.heuristicCommit(true)
	if cmd == nil {
		t.Fatalf("delegate commit returned nil cmd")
	}
	if msg := cmd(); msg != nil {
		if em, ok := msg.(errMsg); ok && em.err != nil {
			t.Fatalf("delegate Cmd errored: %v", em.err)
		}
	}
	if m.showHeuristic {
		t.Errorf("overlay still open after commit")
	}
	if m.heuristicPending != "" {
		t.Errorf("pending prompt not cleared after commit: %q", m.heuristicPending)
	}

	evs, err := log.Read(context.Background(), agentID, 0)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var got string
	for _, ev := range evs {
		if ev.Type == agent.EvtUserMessage {
			var p agent.MessagePayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got = p.Text
		}
	}
	if !strings.HasPrefix(got, heuristicAddendum) {
		t.Errorf("payload missing addendum prefix; got %q", got)
	}
	if !strings.Contains(got, prompt) {
		t.Errorf("payload missing original prompt; got %q", got)
	}
}

func TestHeuristicSoloSendsPromptUnchanged(t *testing.T) {
	m, log, agentID := newOrchestratorModel(t)
	prompt := longPrompt()
	m.openHeuristic(prompt)

	cmd := m.heuristicCommit(false)
	if cmd == nil {
		t.Fatalf("solo commit returned nil cmd")
	}
	if msg := cmd(); msg != nil {
		if em, ok := msg.(errMsg); ok && em.err != nil {
			t.Fatalf("solo Cmd errored: %v", em.err)
		}
	}
	if m.showHeuristic {
		t.Errorf("overlay still open after solo commit")
	}

	evs, err := log.Read(context.Background(), agentID, 0)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var got string
	for _, ev := range evs {
		if ev.Type == agent.EvtUserMessage {
			var p agent.MessagePayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got = p.Text
		}
	}
	if got != prompt {
		t.Errorf("solo payload != original prompt; got %q want %q", got, prompt)
	}
	if strings.Contains(got, heuristicAddendum) {
		t.Errorf("solo payload leaked addendum: %q", got)
	}
}

func TestHeuristicEscapePreservesPrompt(t *testing.T) {
	m, _, _ := newOrchestratorModel(t)
	prompt := longPrompt()
	m.openHeuristic(prompt)

	next, _, handled := m.handleHeuristicKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !handled {
		t.Fatalf("esc not handled")
	}
	m = next.(*Model)
	if m.showHeuristic {
		t.Errorf("overlay still open after esc")
	}
	if v := m.ta.Value(); v != prompt {
		t.Errorf("composer not restored after esc: got %q want %q", v, prompt)
	}
	if m.heuristicPending != "" {
		t.Errorf("pending prompt not cleared after esc: %q", m.heuristicPending)
	}
}

// TestHeuristicEnterUsesDefaultAction walks the enter key path twice —
// once with zero checks (default solo) and once with three (default
// delegate) — and asserts each routes to the corresponding commit.
func TestHeuristicEnterUsesDefaultAction(t *testing.T) {
	t.Run("default solo", func(t *testing.T) {
		m, log, agentID := newOrchestratorModel(t)
		m.openHeuristic(longPrompt())
		next, cmd, handled := m.handleHeuristicKey(tea.KeyMsg{Type: tea.KeyEnter})
		if !handled || cmd == nil {
			t.Fatalf("enter not handled or nil cmd")
		}
		m = next.(*Model)
		_ = cmd()
		evs, _ := log.Read(context.Background(), agentID, 0)
		for _, ev := range evs {
			if ev.Type == agent.EvtUserMessage {
				var p agent.MessagePayload
				_ = json.Unmarshal(ev.Payload, &p)
				if strings.HasPrefix(p.Text, heuristicAddendum) {
					t.Errorf("default-solo enter injected addendum")
				}
			}
		}
	})

	t.Run("default delegate", func(t *testing.T) {
		m, log, agentID := newOrchestratorModel(t)
		m.openHeuristic(longPrompt())
		for i := 0; i < heuristicYesThreshold; i++ {
			m.heuristicChecks[i] = true
		}
		next, cmd, handled := m.handleHeuristicKey(tea.KeyMsg{Type: tea.KeyEnter})
		if !handled || cmd == nil {
			t.Fatalf("enter not handled or nil cmd")
		}
		m = next.(*Model)
		_ = cmd()
		evs, _ := log.Read(context.Background(), agentID, 0)
		var found bool
		for _, ev := range evs {
			if ev.Type == agent.EvtUserMessage {
				var p agent.MessagePayload
				_ = json.Unmarshal(ev.Payload, &p)
				if strings.HasPrefix(p.Text, heuristicAddendum) {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("default-delegate enter didn't inject addendum")
		}
	})
}

func TestHeuristicHelpToggle(t *testing.T) {
	m, _, _ := newOrchestratorModel(t)
	m.openHeuristic(longPrompt())
	if m.heuristicHelp {
		t.Fatal("help on by default")
	}
	next, _, handled := m.handleHeuristicKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !handled {
		t.Fatal("? not handled")
	}
	m = next.(*Model)
	if !m.heuristicHelp {
		t.Errorf("? didn't toggle help on")
	}
	next, _, _ = m.handleHeuristicKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = next.(*Model)
	if m.heuristicHelp {
		t.Errorf("? didn't toggle help off")
	}
}

func TestHeuristicCtrlCFallsThrough(t *testing.T) {
	m, _, _ := newOrchestratorModel(t)
	m.openHeuristic(longPrompt())
	_, _, handled := m.handleHeuristicKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if handled {
		t.Errorf("ctrl+c should fall through, not be swallowed")
	}
}

// TestRenderHeuristicOverlay_ContainsCountAndQuestions snapshots the
// rendered overlay at two terminal sizes (80x24 + 120x40) and asserts
// the load-bearing copy lands: count summary, all five questions, and
// both action keys.
func TestRenderHeuristicOverlay_ContainsCountAndQuestions(t *testing.T) {
	for _, dims := range [][2]int{{80, 24}, {120, 40}} {
		t.Run("", func(t *testing.T) {
			var checks [heuristicQuestionCount]bool
			checks[0] = true
			checks[1] = true
			out := renderHeuristicOverlay(longPrompt(), checks, false, dims[0]-4)
			for _, q := range heuristicQuestions {
				if !strings.Contains(out, q) {
					t.Errorf("%dx%d: missing question %q\n%s", dims[0], dims[1], q, out)
				}
			}
			if !strings.Contains(out, "2 of 5 favor delegation") {
				t.Errorf("%dx%d: count summary missing\n%s", dims[0], dims[1], out)
			}
			if !strings.Contains(out, "default: ") {
				t.Errorf("%dx%d: default suffix missing\n%s", dims[0], dims[1], out)
			}
			for _, want := range []string{"delegate", "solo", "esc"} {
				if !strings.Contains(out, want) {
					t.Errorf("%dx%d: missing %q\n%s", dims[0], dims[1], want, out)
				}
			}
			if !strings.Contains(out, "delegation check") {
				t.Errorf("%dx%d: header missing\n%s", dims[0], dims[1], out)
			}
		})
	}
}

func TestRenderHeuristicOverlay_DefaultSwitchesAtThreshold(t *testing.T) {
	var checks [heuristicQuestionCount]bool
	out := renderHeuristicOverlay(longPrompt(), checks, false, 100)
	if !strings.Contains(out, "0 of 5 favor delegation") {
		t.Errorf("missing zero summary\n%s", out)
	}
	for i := 0; i < heuristicYesThreshold; i++ {
		checks[i] = true
	}
	out = renderHeuristicOverlay(longPrompt(), checks, false, 100)
	if !strings.Contains(out, "3 of 5 favor delegation") {
		t.Errorf("missing 3-of-5 summary\n%s", out)
	}
}

func TestRenderHeuristicOverlay_HelpFooter(t *testing.T) {
	var checks [heuristicQuestionCount]bool
	out := renderHeuristicOverlay(longPrompt(), checks, true, 140)
	for _, want := range []string{"toggle question", "delegate", "solo", "cancel", "hide help"} {
		if !strings.Contains(out, want) {
			t.Errorf("help footer missing %q\n%s", want, out)
		}
	}
}

// TestUpdate_HeuristicRouting hits the Model.Update entrypoint with the
// overlay open and asserts an unrelated key (a number toggle) routes
// through the heuristic handler instead of the textarea.
func TestUpdate_HeuristicRouting(t *testing.T) {
	m, _, _ := newOrchestratorModel(t)
	m.openHeuristic(longPrompt())

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	mm := updated.(*Model)
	if !mm.heuristicChecks[0] {
		t.Errorf("'1' didn't reach the heuristic handler")
	}
	if v := mm.ta.Value(); v != "" {
		t.Errorf("'1' leaked to textarea: %q", v)
	}
}
