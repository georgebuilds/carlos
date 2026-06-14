package chat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tui/slash"
)

// pfScriptedProvider feeds a queue of canned bodies, one per Stream
// call, so the clarify call and the brief call can return different
// responses in sequence. Mirrors the research package's scriptedProvider
// but lives here so the chat test stays self-contained.
type pfScriptedProvider struct {
	mu        sync.Mutex
	responses []string
	streamErr error
}

func (p *pfScriptedProvider) Name() string                         { return "pf" }
func (p *pfScriptedProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }

func (p *pfScriptedProvider) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	if p.streamErr != nil {
		return nil, p.streamErr
	}
	p.mu.Lock()
	var body string
	if len(p.responses) > 0 {
		body = p.responses[0]
		p.responses = p.responses[1:]
	}
	p.mu.Unlock()
	ch := make(chan providers.Event, 2)
	go func() {
		defer close(ch)
		ch <- providers.Event{Kind: providers.EventTextDelta, Text: body}
		ch <- providers.Event{Kind: providers.EventStopReason, Stop: "end_turn"}
	}()
	return ch, nil
}

// newPreflightModel builds a chat Model wired with a research engine and
// a scripted pre-flight provider, driven to a renderable size.
func newPreflightModel(t *testing.T, agentID string, responses ...string) (*Model, *fakeResearchEngine) {
	t.Helper()
	log := openTempLog(t)
	seedAgent(t, log, agentID, "preflight", "fake")
	fe := &fakeResearchEngine{}
	prov := &pfScriptedProvider{responses: responses}
	m := New(log, agentID, NewMemTextSource(),
		WithResearchEngine(fe),
		WithPreflight(research.Preflight{Provider: prov, Model: "m"}),
	)
	m = drive(t, m, 120, 30)
	return m, fe
}

// runCmd executes a tea.Cmd and feeds the resulting message back through
// Update, returning the (possibly new) Model and any follow-up cmd. A nil
// cmd is a no-op.
func runCmd(t *testing.T, m *Model, cmd tea.Cmd) (*Model, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return m, nil
	}
	msg := cmd()
	if msg == nil {
		return m, nil
	}
	updated, next := m.Update(msg)
	return updated.(*Model), next
}

// pfKey sends a single key string (via the package key() constructor)
// through Update and returns the Model + follow-up cmd.
func pfKey(t *testing.T, m *Model, s string) (*Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(key(s))
	return updated.(*Model), cmd
}

func typeString(t *testing.T, m *Model, s string) *Model {
	t.Helper()
	for _, r := range s {
		m, _ = pfKey(t, m, string(r))
	}
	return m
}

// TestWithPreflight_SetsField proves the option wires (and that a nil
// provider leaves it unset).
func TestWithPreflight_SetsField(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000PF00001"
	seedAgent(t, log, agentID, "wires", "fake")

	m := New(log, agentID, NewMemTextSource(),
		WithPreflight(research.Preflight{Provider: &pfScriptedProvider{}, Model: "m"}))
	if m.preflight == nil {
		t.Fatal("WithPreflight didn't set the field")
	}

	m2 := New(log, agentID, NewMemTextSource(),
		WithPreflight(research.Preflight{Model: "m"}))
	if m2.preflight != nil {
		t.Fatal("WithPreflight should ignore a nil provider")
	}
}

func TestParseResearchArgs(t *testing.T) {
	tests := []struct {
		in       string
		wantQ    string
		wantFlag bool
	}{
		{"how fast is go", "how fast is go", false},
		{"--no-clarify how fast is go", "how fast is go", true},
		{"how fast --no-clarify is go", "how fast is go", true},
		{"--no-clarify", "", true},
		{"   ", "", false},
	}
	for _, tc := range tests {
		q, flag := parseResearchArgs(tc.in)
		if q != tc.wantQ || flag != tc.wantFlag {
			t.Errorf("parseResearchArgs(%q) = (%q,%v), want (%q,%v)", tc.in, q, flag, tc.wantQ, tc.wantFlag)
		}
	}
}

// TestPreflight_NoClarifyBypasses proves --no-clarify skips the
// pre-flight and dispatches research directly (no clarify/brief state).
func TestPreflight_NoClarifyBypasses(t *testing.T) {
	const agentID = "01HV0000000000000000PF00002"
	m, fe := newPreflightModel(t, agentID, "Q1?", "brief")

	c, _ := slash.Parse("/research --no-clarify what is go")
	cmd := m.dispatchSlash(c)
	if m.preflightActive() {
		t.Fatal("--no-clarify should not enter the pre-flight")
	}
	// The returned cmd is the sync research status echo.
	if cmd == nil {
		t.Fatal("expected a research dispatch cmd")
	}
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "researching") {
		t.Fatalf("expected researching status, got %#v", cmd())
	}
	waitForRun(t, fe, "what is go")
}

// TestPreflight_EngineNilBypasses proves a nil engine short-circuits to
// the "not wired" echo even if the pre-flight provider is set.
func TestPreflight_EngineNilBypasses(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000PF00003"
	seedAgent(t, log, agentID, "no engine", "fake")
	m := New(log, agentID, NewMemTextSource(),
		WithPreflight(research.Preflight{Provider: &pfScriptedProvider{}, Model: "m"}))
	m = drive(t, m, 120, 30)

	c, _ := slash.Parse("/research what is go")
	cmd := m.dispatchSlash(c)
	if m.preflightActive() {
		t.Fatal("nil engine should not enter the pre-flight")
	}
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "not wired") {
		t.Fatalf("expected not-wired status, got %#v", cmd())
	}
}

// TestPreflight_FullFlow walks the happy path: dispatch -> clarify (two
// questions) -> answer each -> brief -> edit -> confirm launches research
// with the edited brief.
func TestPreflight_FullFlow(t *testing.T) {
	const agentID = "01HV0000000000000000PF00004"
	m, fe := newPreflightModel(t, agentID,
		"Which version?\nWhich OS?",    // clarify
		"Investigate go startup time.", // brief
	)

	c, _ := slash.Parse("/research how fast is go startup")
	cmd := m.dispatchSlash(c)
	if !m.preflightActive() || !m.preflightAwaiting {
		t.Fatal("dispatch should enter pre-flight awaiting state")
	}
	// Resolve the clarify cmd (the batch's first leaf is clarifyCmd).
	m = pumpPreflightCmd(t, m, cmd)
	if m.preflightMode != preflightClarify {
		t.Fatalf("want clarify mode, got %d", m.preflightMode)
	}
	if len(m.preflightQuestions) != 2 {
		t.Fatalf("want 2 questions, got %d", len(m.preflightQuestions))
	}

	// Answer question 1.
	m = typeString(t, m, "1.21")
	m, _ = pfKey(t, m, "enter")
	if m.preflightIndex != 1 {
		t.Fatalf("want index 1 after first answer, got %d", m.preflightIndex)
	}
	// Answer question 2 -> triggers the brief request.
	m = typeString(t, m, "linux")
	var briefCmd tea.Cmd
	m, briefCmd = pfKey(t, m, "enter")
	if !m.preflightAwaiting {
		t.Fatal("after last answer the brief call should be in flight")
	}
	m = pumpPreflightCmd(t, m, briefCmd)
	if m.preflightMode != preflightBrief {
		t.Fatalf("want brief mode, got %d", m.preflightMode)
	}
	if got := strings.TrimSpace(m.ta.Value()); got != "Investigate go startup time." {
		t.Fatalf("brief draft not preloaded: %q", got)
	}

	// Edit the brief, then confirm.
	m = typeString(t, m, " on arm64")
	var launchCmd tea.Cmd
	m, launchCmd = pfKey(t, m, "enter")
	if m.preflightActive() {
		t.Fatal("confirm should tear down the pre-flight")
	}
	if launchCmd == nil {
		t.Fatal("confirm should return a research launch cmd")
	}
	_ = launchCmd()
	waitForRun(t, fe, "Investigate go startup time. on arm64")
}

// TestPreflight_ZeroQuestionsSkipsToBrief proves a specific question
// (zero clarifying questions) skips straight to the brief.
func TestPreflight_ZeroQuestionsSkipsToBrief(t *testing.T) {
	const agentID = "01HV0000000000000000PF00005"
	m, _ := newPreflightModel(t, agentID,
		"", // clarify: none
		"A tight brief.",
	)
	c, _ := slash.Parse("/research very specific question")
	cmd := m.dispatchSlash(c)
	// Pump clarify -> the handler immediately fires the brief cmd.
	m, next := runCmd(t, m, firstLeaf(cmd))
	if m.preflightMode != preflightOff || !m.preflightAwaiting {
		t.Fatalf("zero questions should go straight to brief-awaiting; mode=%d awaiting=%v", m.preflightMode, m.preflightAwaiting)
	}
	m = pumpPreflightCmd(t, m, next)
	if m.preflightMode != preflightBrief {
		t.Fatalf("want brief mode, got %d", m.preflightMode)
	}
	if strings.TrimSpace(m.ta.Value()) != "A tight brief." {
		t.Fatalf("brief draft = %q", m.ta.Value())
	}
}

// TestPreflight_ClarifyErrorSkipsToBrief proves a clarify provider error
// degrades to the brief step rather than blocking.
func TestPreflight_ClarifyErrorSkipsToBrief(t *testing.T) {
	const agentID = "01HV0000000000000000PF00006"
	log := openTempLog(t)
	seedAgent(t, log, agentID, "clarify err", "fake")
	fe := &fakeResearchEngine{}
	// First call (clarify) errors; brief uses a fresh provider via the
	// second response slot won't be reached, so script a failing
	// provider for clarify and a working one is not possible with a
	// single provider. Use a provider whose stream errors only once.
	prov := &flakeProvider{failFirst: true, body: "Recovered brief."}
	m := New(log, agentID, NewMemTextSource(),
		WithResearchEngine(fe),
		WithPreflight(research.Preflight{Provider: prov, Model: "m"}))
	m = drive(t, m, 120, 30)

	c, _ := slash.Parse("/research q")
	cmd := m.dispatchSlash(c)
	m, next := runCmd(t, m, firstLeaf(cmd)) // clarify errors -> requestBrief
	if !m.preflightAwaiting {
		t.Fatal("clarify error should fall through to the brief call")
	}
	m = pumpPreflightCmd(t, m, next)
	if m.preflightMode != preflightBrief {
		t.Fatalf("want brief mode after clarify error, got %d", m.preflightMode)
	}
	if strings.TrimSpace(m.ta.Value()) != "Recovered brief." {
		t.Fatalf("brief draft = %q", m.ta.Value())
	}
}

// TestPreflight_BriefErrorFallsBackToQuestion proves an errored brief
// preloads the original question text so the box is never blank.
func TestPreflight_BriefErrorFallsBackToQuestion(t *testing.T) {
	const agentID = "01HV0000000000000000PF00007"
	log := openTempLog(t)
	seedAgent(t, log, agentID, "brief err", "fake")
	fe := &fakeResearchEngine{}
	// Clarify succeeds (zero questions), brief errors.
	prov := &flakeProvider{failSecond: true, body: ""}
	m := New(log, agentID, NewMemTextSource(),
		WithResearchEngine(fe),
		WithPreflight(research.Preflight{Provider: prov, Model: "m"}))
	m = drive(t, m, 120, 30)

	c, _ := slash.Parse("/research my original question")
	cmd := m.dispatchSlash(c)
	m, next := runCmd(t, m, firstLeaf(cmd)) // clarify -> 0 questions -> brief
	m = pumpPreflightCmd(t, m, next)
	if m.preflightMode != preflightBrief {
		t.Fatalf("want brief mode, got %d", m.preflightMode)
	}
	if strings.TrimSpace(m.ta.Value()) != "my original question" {
		t.Fatalf("brief should fall back to question, got %q", m.ta.Value())
	}
}

// TestPreflight_EscCancels proves Esc tears down the pre-flight at the
// clarify stage and launches nothing.
func TestPreflight_EscCancels(t *testing.T) {
	const agentID = "01HV0000000000000000PF00008"
	m, fe := newPreflightModel(t, agentID, "Which version?", "brief")
	c, _ := slash.Parse("/research q")
	cmd := m.dispatchSlash(c)
	m = pumpPreflightCmd(t, m, cmd)
	if m.preflightMode != preflightClarify {
		t.Fatalf("want clarify mode, got %d", m.preflightMode)
	}
	var cancelCmd tea.Cmd
	m, cancelCmd = pfKey(t, m, "esc")
	if m.preflightActive() {
		t.Fatal("esc should clear the pre-flight")
	}
	if cancelCmd == nil {
		t.Fatal("esc should return a status cmd")
	}
	st, ok := cancelCmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "canceled") {
		t.Fatalf("expected cancel status, got %#v", cancelCmd())
	}
	if fe.callCount() != 0 {
		t.Fatalf("esc must not launch research; got %d calls", fe.callCount())
	}
}

// TestPreflight_EmptyBriefRefused proves Enter on an empty brief nudges
// rather than launching.
func TestPreflight_EmptyBriefRefused(t *testing.T) {
	const agentID = "01HV0000000000000000PF00009"
	m, fe := newPreflightModel(t, agentID, "", "")
	c, _ := slash.Parse("/research q")
	cmd := m.dispatchSlash(c)
	m, next := runCmd(t, m, firstLeaf(cmd))
	m = pumpPreflightCmd(t, m, next) // brief mode, empty draft (falls back to "q")
	// Clear the composer so the brief is empty.
	m.ta.SetValue("   ")
	var warnCmd tea.Cmd
	m, warnCmd = pfKey(t, m, "enter")
	if !m.preflightActive() {
		t.Fatal("empty brief should keep the pre-flight open")
	}
	st, ok := warnCmd().(statusMsg)
	if !ok || st.kind != statusWarn {
		t.Fatalf("expected warn status, got %#v", warnCmd())
	}
	if fe.callCount() != 0 {
		t.Fatalf("empty brief must not launch; got %d calls", fe.callCount())
	}
}

// TestPreflight_AwaitingSwallowsKeys proves keys (other than esc/ctrl+c)
// are swallowed while a model call is in flight.
func TestPreflight_AwaitingSwallowsKeys(t *testing.T) {
	const agentID = "01HV0000000000000000PF00010"
	m, _ := newPreflightModel(t, agentID, "Q?", "brief")
	c, _ := slash.Parse("/research q")
	_ = m.dispatchSlash(c) // now awaiting (clarify cmd not yet resolved)
	if !m.preflightAwaiting {
		t.Fatal("should be awaiting after dispatch")
	}
	before := m.ta.Value()
	m, cmd := pfKey(t, m, "x")
	if cmd != nil {
		t.Errorf("awaiting key should be swallowed, got cmd %v", cmd)
	}
	if m.ta.Value() != before {
		t.Errorf("awaiting key leaked into composer: %q", m.ta.Value())
	}
}

// TestPreflight_StaleResultIgnored proves a result that arrives after a
// cancel (question mismatch) is dropped.
func TestPreflight_StaleResultIgnored(t *testing.T) {
	const agentID = "01HV0000000000000000PF00011"
	m, _ := newPreflightModel(t, agentID, "Q?", "brief")
	stale := clarifyResultMsg{question: "different question", questions: []string{"x"}}
	updated, cmd := m.Update(stale)
	m = updated.(*Model)
	if cmd != nil || m.preflightActive() {
		t.Fatal("stale clarify result should be ignored")
	}
	staleBrief := briefResultMsg{question: "different", brief: "x"}
	updated, cmd = m.Update(staleBrief)
	m = updated.(*Model)
	if cmd != nil || m.preflightActive() {
		t.Fatal("stale brief result should be ignored")
	}
}

// TestPreflight_CtrlCFallsThrough proves ctrl+c is NOT handled by the
// pre-flight router (handled=false) so the quit path stays reachable.
func TestPreflight_CtrlCFallsThrough(t *testing.T) {
	const agentID = "01HV0000000000000000PF00013"
	m, _ := newPreflightModel(t, agentID, "Q?", "brief")
	c, _ := slash.Parse("/research q")
	cmd := m.dispatchSlash(c)
	m = pumpPreflightCmd(t, m, cmd)
	_, _, handled := m.handlePreflightKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if handled {
		t.Fatal("ctrl+c must fall through (handled=false) so quit stays reachable")
	}
}

// TestPreflight_AsyncLaunch proves the spawner path is taken when a
// spawner is wired: confirming the brief routes through runResearchAsync.
func TestPreflight_AsyncLaunch(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000PF00014"
	seedAgent(t, log, agentID, "async", "fake")
	fe := &fakeResearchEngine{}
	sp := &fakeSpawner{resultCh: make(chan research.ResearchResult, 1)}
	prov := &pfScriptedProvider{responses: []string{"", "A brief to run."}}
	m := New(log, agentID, NewMemTextSource(),
		WithResearchEngine(fe),
		WithResearchSpawner(sp),
		WithPreflight(research.Preflight{Provider: prov, Model: "m"}),
	)
	m = drive(t, m, 120, 30)

	c, _ := slash.Parse("/research q")
	cmd := m.dispatchSlash(c)
	m, next := runCmd(t, m, firstLeaf(cmd)) // clarify -> 0 questions -> brief
	m = pumpPreflightCmd(t, m, next)
	if m.preflightMode != preflightBrief {
		t.Fatalf("want brief mode, got %d", m.preflightMode)
	}
	var launch tea.Cmd
	m, launch = pfKey(t, m, "enter")
	if launch == nil {
		t.Fatal("confirm should return a launch cmd")
	}
	_ = launch()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sp.callCount() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if sp.callCount() != 1 {
		t.Fatalf("spawner called %d times, want 1 (async path)", sp.callCount())
	}
	if fe.callCount() != 0 {
		t.Fatalf("sync engine called %d times, want 0", fe.callCount())
	}
}

// TestPreflight_RenderClarifyIndexClamp exercises the render path where
// the index has advanced to the last question (idx clamp branch) by
// rendering after the questions are set with index at the boundary.
func TestPreflight_RenderClarifyIndexClamp(t *testing.T) {
	const agentID = "01HV0000000000000000PF00015"
	m, _ := newPreflightModel(t, agentID, "Only one?", "brief")
	c, _ := slash.Parse("/research q")
	cmd := m.dispatchSlash(c)
	m = pumpPreflightCmd(t, m, cmd)
	// Force the index past the last question to hit the clamp branch.
	m.preflightIndex = len(m.preflightQuestions)
	mustContain(t, m.View(), "Only one?")
}

// TestPreflight_View renders each face so the overlay code is exercised.
func TestPreflight_View(t *testing.T) {
	const agentID = "01HV0000000000000000PF00012"
	m, _ := newPreflightModel(t, agentID, "Which version?\nWhich OS?", "A brief.")
	c, _ := slash.Parse("/research q")
	cmd := m.dispatchSlash(c)
	// Awaiting face.
	mustContain(t, m.View(), "research pre-flight")
	mustContain(t, m.View(), "thinking")

	m = pumpPreflightCmd(t, m, cmd)
	// Clarify face.
	v := m.View()
	mustContain(t, v, "clarify")
	mustContain(t, v, "Which version?")
	mustContain(t, v, "question 1 of 2")

	// Advance to the brief face.
	m, _ = pfKey(t, m, "enter")
	m, next := pfKey(t, m, "enter")
	m = pumpPreflightCmd(t, m, next)
	mustContain(t, m.View(), "brief")
}

// --- test helpers ----------------------------------------------------------

// waitForRun blocks (up to 2s) until the fake engine has recorded a Run
// call with wantQ, asserting the research path launched with the refined
// question/brief.
func waitForRun(t *testing.T, fe *fakeResearchEngine, wantQ string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fe.mu.Lock()
		for _, q := range fe.calls {
			if q == wantQ {
				fe.mu.Unlock()
				return
			}
		}
		fe.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	fe.mu.Lock()
	got := append([]string(nil), fe.calls...)
	fe.mu.Unlock()
	t.Fatalf("engine never ran with %q; saw %v", wantQ, got)
}

// flakeProvider lets a single provider fail a chosen call in the
// clarify/brief sequence. failFirst fails the first Stream (clarify);
// failSecond fails the second (brief). Otherwise it emits body.
type flakeProvider struct {
	mu         sync.Mutex
	n          int
	failFirst  bool
	failSecond bool
	body       string
}

func (p *flakeProvider) Name() string                         { return "flake" }
func (p *flakeProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (p *flakeProvider) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	p.mu.Lock()
	p.n++
	n := p.n
	p.mu.Unlock()
	if (p.failFirst && n == 1) || (p.failSecond && n == 2) {
		return nil, errors.New("flake stream error")
	}
	body := p.body
	if n == 1 && (p.failSecond) {
		// First (clarify) call returns no questions so the flow goes
		// straight to the brief, which is the call we want to fail.
		body = ""
	}
	ch := make(chan providers.Event, 2)
	go func() {
		defer close(ch)
		ch <- providers.Event{Kind: providers.EventTextDelta, Text: body}
		ch <- providers.Event{Kind: providers.EventStopReason, Stop: "end_turn"}
	}()
	return ch, nil
}

// firstLeaf resolves a (possibly batched) tea.Cmd down to a single
// message by running it; tea.Batch returns a BatchMsg of cmds, so we run
// the clarify/brief leaf specifically. startPreflight + requestBrief
// batch [providerCmd, scheduleTextTick]; the provider leaf is the one we
// care about. We find it by running each leaf and returning the
// clarify/brief result.
func firstLeaf(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch v := msg.(type) {
	case tea.BatchMsg:
		for _, c := range v {
			if c == nil {
				continue
			}
			leaf := c()
			switch leaf.(type) {
			case clarifyResultMsg, briefResultMsg:
				return func() tea.Msg { return leaf }
			}
		}
		return nil
	case clarifyResultMsg, briefResultMsg:
		return func() tea.Msg { return msg }
	default:
		return func() tea.Msg { return msg }
	}
}

// pumpPreflightCmd resolves the provider leaf of cmd and feeds the
// result through Update, returning the new Model.
func pumpPreflightCmd(t *testing.T, m *Model, cmd tea.Cmd) *Model {
	t.Helper()
	leaf := firstLeaf(cmd)
	if leaf == nil {
		return m
	}
	msg := leaf()
	if msg == nil {
		return m
	}
	updated, _ := m.Update(msg)
	return updated.(*Model)
}
