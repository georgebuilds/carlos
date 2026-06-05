package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tui/slash"
)

// fakeResearchEngine is the test seam for the ResearchEngine
// interface. It records every Run invocation + replays a canned
// (Report, error) tuple so we can assert /research flow shape without
// dragging the real provider / HTTP / search backends into a chat
// test.
type fakeResearchEngine struct {
	mu        sync.Mutex
	calls     []string
	report    *research.Report
	err       error
	startedCh chan struct{}
}

func (f *fakeResearchEngine) Run(ctx context.Context, q string) (*research.Report, error) {
	f.mu.Lock()
	f.calls = append(f.calls, q)
	if f.startedCh != nil {
		close(f.startedCh)
		f.startedCh = nil
	}
	r, err := f.report, f.err
	f.mu.Unlock()
	return r, err
}

func (f *fakeResearchEngine) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// TestWithResearchEngine_SetsField proves the option wires through.
func TestWithResearchEngine_SetsField(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000RES001"
	seedAgent(t, log, agentID, "wires", "fake")

	fe := &fakeResearchEngine{}
	m := New(log, agentID, NewMemTextSource(), WithResearchEngine(fe))
	if m.researchEngine == nil {
		t.Fatal("WithResearchEngine didn't set the field")
	}
	if m.researchEngine != fe {
		t.Error("WithResearchEngine stored a different value")
	}
}

// TestResearchSlash_NilEngineReturnsNotWired covers the dev-aid path:
// chat constructed without WithResearchEngine still handles /research
// cleanly — just an info echo.
func TestResearchSlash_NilEngineReturnsNotWired(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000RES002"
	seedAgent(t, log, agentID, "no engine", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	c, _ := slash.Parse("/research what is go")
	cmd := m.dispatchSlash(c)
	if cmd == nil {
		t.Fatal("dispatchSlash(/research) returned nil with no engine")
	}
	st, ok := cmd().(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", cmd())
	}
	if !strings.Contains(st.text, "not wired") {
		t.Errorf("status text should say 'not wired': %q", st.text)
	}
	if st.kind != statusWarn {
		t.Errorf("kind = %d, want statusWarn", st.kind)
	}
}

// TestResearchSlash_EmptyArgsReturnsUsage covers the typed-too-fast
// case: /research with no question body should surface a usage line.
func TestResearchSlash_EmptyArgsReturnsUsage(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000RES003"
	seedAgent(t, log, agentID, "empty args", "fake")
	fe := &fakeResearchEngine{}
	m := New(log, agentID, NewMemTextSource(), WithResearchEngine(fe))
	m = drive(t, m, 120, 30)

	// "/research" alone (no body) and "/research   " (whitespace) both
	// route through the same empty-args guard.
	for _, raw := range []string{"/research", "/research   "} {
		c, _ := slash.Parse(raw)
		cmd := m.dispatchSlash(c)
		if cmd == nil {
			t.Fatalf("%q: dispatchSlash returned nil", raw)
		}
		st, ok := cmd().(statusMsg)
		if !ok {
			t.Fatalf("%q: expected statusMsg, got %T", raw, cmd())
		}
		if !strings.Contains(st.text, "usage: /research") {
			t.Errorf("%q: status missing usage line: %q", raw, st.text)
		}
		if st.kind != statusWarn {
			t.Errorf("%q: kind = %d, want statusWarn", raw, st.kind)
		}
	}
	if fe.callCount() != 0 {
		t.Errorf("engine called for empty args: %d times", fe.callCount())
	}
}

// TestResearchSlash_WritesAssistantMessageOnSuccess is the happy-path
// integration: a configured engine returns a Report → the chat writes
// a single EvtAssistantMessage carrying the rendered markdown.
func TestResearchSlash_WritesAssistantMessageOnSuccess(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000RES004"
	seedAgent(t, log, agentID, "happy", "fake")

	report := &research.Report{
		Question:  "What is Go?",
		Synthesis: "Go is a statically typed language [p1].",
		Sources: []research.Source{
			{ID: "s1", Title: "Go spec", URL: "https://go.dev/ref/spec"},
		},
		Passages: []research.Passage{
			{ID: "p1", SourceID: "s1", Text: "Go was designed at Google.", Relevance: 9},
		},
	}
	fe := &fakeResearchEngine{report: report}

	m := New(log, agentID, NewMemTextSource(), WithResearchEngine(fe))
	m = drive(t, m, 120, 30)

	c, _ := slash.Parse("/research What is Go?")
	cmd := m.dispatchSlash(c)
	if cmd == nil {
		t.Fatal("dispatchSlash returned nil for /research with engine wired")
	}
	// Status message: surfaced immediately, says researching.
	msg := cmd()
	st, ok := msg.(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", msg)
	}
	if !strings.Contains(st.text, "researching") || !strings.Contains(st.text, "What is Go?") {
		t.Errorf("status text shape unexpected: %q", st.text)
	}

	// The engine runs in a goroutine; poll the log until the
	// EvtAssistantMessage lands. 2s is generous (the fake returns
	// instantly).
	deadline := time.Now().Add(2 * time.Second)
	var assistantMsgs []agent.Event
	for time.Now().Before(deadline) {
		evs, err := log.Read(context.Background(), agentID, 0)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		assistantMsgs = assistantMsgs[:0]
		for _, ev := range evs {
			if ev.Type == agent.EvtAssistantMessage {
				assistantMsgs = append(assistantMsgs, ev)
			}
		}
		if len(assistantMsgs) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(assistantMsgs) != 1 {
		t.Fatalf("expected 1 EvtAssistantMessage, got %d", len(assistantMsgs))
	}
	var p agent.MessagePayload
	if err := json.Unmarshal(assistantMsgs[0].Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	// The rendered Report should include the question, the synthesis,
	// and the source line.
	for _, want := range []string{"What is Go?", "statically typed", "Go spec", "https://go.dev/ref/spec"} {
		if !strings.Contains(p.Text, want) {
			t.Errorf("rendered report missing %q:\n%s", want, p.Text)
		}
	}

	if fe.callCount() != 1 {
		t.Errorf("engine called %d times, want 1", fe.callCount())
	}
}

// TestResearchSlash_WritesAssistantMessageOnError exercises the failure
// path: the engine returns nothing useful → the chat still surfaces a
// reading 🧢 line so the user isn't left staring at a blank chat.
func TestResearchSlash_WritesAssistantMessageOnError(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000RES005"
	seedAgent(t, log, agentID, "errpath", "fake")

	fe := &fakeResearchEngine{err: errors.New("provider unreachable")}
	m := New(log, agentID, NewMemTextSource(), WithResearchEngine(fe))
	m = drive(t, m, 120, 30)

	c, _ := slash.Parse("/research will this work")
	cmd := m.dispatchSlash(c)
	if cmd == nil {
		t.Fatal("dispatchSlash returned nil for /research")
	}
	_ = cmd() // drain status

	// Poll for the failure assistant message.
	deadline := time.Now().Add(2 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		evs, err := log.Read(context.Background(), agentID, 0)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		for _, ev := range evs {
			if ev.Type == agent.EvtAssistantMessage {
				var p agent.MessagePayload
				_ = json.Unmarshal(ev.Payload, &p)
				got = p.Text
				break
			}
		}
		if got != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got == "" {
		t.Fatal("no EvtAssistantMessage written on engine error")
	}
	if !strings.Contains(got, "research failed") || !strings.Contains(got, "provider unreachable") {
		t.Errorf("error message shape unexpected:\n%s", got)
	}
}

// TestRenderReportMarkdown_Shape pins the rendered output shape so
// future tweaks land deliberately rather than silently. Mirrors the
// engine_test report shape.
func TestRenderReportMarkdown_Shape(t *testing.T) {
	r := &research.Report{
		Question: "What is WebGPU?",
		Query:    research.Query{Sub: []string{"sub a", "sub b"}},
		Sources: []research.Source{
			{ID: "s1", Title: "spec", URL: "https://w3.org/webgpu"},
			{ID: "s2", Title: "", URL: "https://example.org"},
		},
		Passages: []research.Passage{
			{ID: "p1", SourceID: "s1", Text: "WebGPU is a graphics API.", Relevance: 8},
		},
		Synthesis: "WebGPU is the next-gen API [p1].",
		Concerns:  []string{"only 1 source reached"},
	}
	out := RenderReportMarkdown(r)
	for _, want := range []string{
		"# Research report: What is WebGPU?",
		"## Sub-queries",
		"- sub a",
		"- sub b",
		"## Synthesis",
		"WebGPU is the next-gen API [p1].",
		"## Sources",
		"**s1**",
		"spec",
		"https://w3.org/webgpu",
		"(untitled)", // empty Source.Title fallback
		"## Passages",
		"**[p1]** (relevance 8, source s1):",
		"## Engine concerns",
		"only 1 source reached",
		"## Budget",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in rendered output:\n%s", want, out)
		}
	}
}

// TestRenderReportMarkdown_NilSafe — defensive: callers shouldn't pass
// nil, but if they do (e.g. a test-fixture bug) we should return ""
// rather than panic.
func TestRenderReportMarkdown_NilSafe(t *testing.T) {
	if got := RenderReportMarkdown(nil); got != "" {
		t.Errorf("RenderReportMarkdown(nil) = %q, want empty string", got)
	}
}

// TestResearchBuiltin_RegisteredInSlash guards the /help discovery
// path: removing the builtin would silently drop /research from the
// help overlay even though the dispatch still routes it.
func TestResearchBuiltin_RegisteredInSlash(t *testing.T) {
	spec, ok := slash.Lookup("research")
	if !ok {
		t.Fatal("/research not registered in slash.Builtins")
	}
	if spec.ArgsHint == "" {
		t.Error("/research builtin has empty ArgsHint — /help line will look wrong")
	}
	if !strings.Contains(spec.Description, "research") {
		t.Errorf("description missing 'research': %q", spec.Description)
	}
}

// fakeSpawner records every Spawn call and produces a controllable
// done-channel + agent-id pair for tests of the async /research path.
// Tests drive completion by sending into resultCh (which the fake forwards
// onto the chan it returned to runResearchAsync).
type fakeSpawner struct {
	mu       sync.Mutex
	calls    []string
	agentID  string // returned ID; if empty a fixed default is used
	spawnErr error  // if non-nil, Spawn returns this without producing a channel
	resultCh chan research.ResearchResult
}

func (f *fakeSpawner) Spawn(ctx context.Context, q string) (string, <-chan research.ResearchResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, q)
	id := f.agentID
	if id == "" {
		id = "r-fake-001"
	}
	err := f.spawnErr
	ch := f.resultCh
	f.mu.Unlock()
	if err != nil {
		return "", nil, err
	}
	return id, ch, nil
}

func (f *fakeSpawner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// TestWithResearchSpawner_SetsField proves the option wires through.
func TestWithResearchSpawner_SetsField(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000RES010"
	seedAgent(t, log, agentID, "spawner wires", "fake")

	sp := &fakeSpawner{resultCh: make(chan research.ResearchResult, 1)}
	m := New(log, agentID, NewMemTextSource(), WithResearchSpawner(sp))
	if m.spawner == nil {
		t.Fatal("WithResearchSpawner didn't set the field")
	}
	if m.spawner != sp {
		t.Error("WithResearchSpawner stored a different value")
	}
}

// TestResearchSlash_PrefersAsyncWhenSpawnerWired exercises the
// supervisor-aware branch: with both engine + spawner wired,
// dispatchSlash takes the async path → Spawn is called, status text
// says "research started".
func TestResearchSlash_PrefersAsyncWhenSpawnerWired(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000RES011"
	seedAgent(t, log, agentID, "prefer async", "fake")

	fe := &fakeResearchEngine{}
	resultCh := make(chan research.ResearchResult, 1)
	sp := &fakeSpawner{resultCh: resultCh, agentID: "r-fake-async-001"}

	m := New(log, agentID, NewMemTextSource(),
		WithResearchEngine(fe),
		WithResearchSpawner(sp))
	m = drive(t, m, 120, 30)

	c, _ := slash.Parse("/research what is async")
	cmd := m.dispatchSlash(c)
	if cmd == nil {
		t.Fatal("dispatchSlash returned nil for /research with spawner wired")
	}
	st, ok := cmd().(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", cmd())
	}
	if !strings.Contains(st.text, "research started") {
		t.Errorf("status text should say 'research started': %q", st.text)
	}
	// Poll for the spawn call: the goroutine fires before returning the
	// statusMsg but is async with the cmd() invocation above.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sp.callCount() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if sp.callCount() != 1 {
		t.Errorf("spawner called %d times, want 1", sp.callCount())
	}
	// Sync goroutine cleanup: deliver a fake result so the drain
	// goroutine exits cleanly.
	resultCh <- research.ResearchResult{AgentID: "r-fake-async-001"}
	close(resultCh)
	// Sync path engine.Run should NOT have been called.
	if fe.callCount() != 0 {
		t.Errorf("sync engine called %d times, want 0 (async path should bypass)", fe.callCount())
	}
}

// TestResearchSlash_FallbackToSyncWhenNoSpawner confirms the slice-11f
// sync path still runs when the spawner is unset, even with the engine
// wired. This is the back-compat contract for the dev-aid chat surface.
func TestResearchSlash_FallbackToSyncWhenNoSpawner(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000RES012"
	seedAgent(t, log, agentID, "sync fallback", "fake")

	fe := &fakeResearchEngine{report: &research.Report{
		Question: "Q", Synthesis: "S",
	}}

	m := New(log, agentID, NewMemTextSource(), WithResearchEngine(fe))
	m = drive(t, m, 120, 30)

	c, _ := slash.Parse("/research fallback")
	cmd := m.dispatchSlash(c)
	if cmd == nil {
		t.Fatal("dispatchSlash returned nil")
	}
	st, ok := cmd().(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", cmd())
	}
	if !strings.Contains(st.text, "researching") {
		t.Errorf("expected sync-path 'researching' status, got %q", st.text)
	}

	// Engine should be called via the sync path.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fe.callCount() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if fe.callCount() != 1 {
		t.Errorf("sync engine called %d times, want 1", fe.callCount())
	}
}

// TestApplyEvent_ResearchPhase_AppendsFirstThenCollapses verifies the
// in-place collapse: the first phase event creates an entry; subsequent
// phase events for the same sub-agent overwrite it; events for a
// different sub-agent create a separate entry.
func TestApplyEvent_ResearchPhase_AppendsFirstThenCollapses(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000RES013"
	seedAgent(t, log, agentID, "collapse", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	const subA = "r-aaaa-001"
	const subB = "r-bbbb-002"

	push := func(subAgent, phase string, done bool, elapsedMs int, errStr string) {
		pl := agent.ResearchPhasePayload{
			Phase:   phase,
			Done:    done,
			Elapsed: time.Duration(elapsedMs) * time.Millisecond,
			Err:     errStr,
		}
		payload, err := json.Marshal(pl)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		m.applyEvent(agent.Event{
			AgentID: subAgent,
			TS:      time.Now().UTC(),
			Type:    agent.EvtResearchPhase,
			Payload: payload,
		})
	}

	push(subA, "decompose", false, 0, "")
	if got := countProgressEntries(m); got != 1 {
		t.Fatalf("after first phase: progress entries = %d, want 1", got)
	}
	push(subA, "decompose", true, 200, "")
	push(subA, "search", false, 0, "")
	push(subA, "search", true, 4500, "")
	if got := countProgressEntries(m); got != 1 {
		t.Fatalf("after subA phases: progress entries = %d, want 1 (collapsed)", got)
	}
	// Different sub-agent → distinct entry.
	push(subB, "decompose", false, 0, "")
	if got := countProgressEntries(m); got != 2 {
		t.Fatalf("after subB starts: progress entries = %d, want 2 (one per sub-agent)", got)
	}
	push(subA, "verify", true, 12_000, "")
	push(subB, "decompose", true, 100, "failed: bad query")
	if got := countProgressEntries(m); got != 2 {
		t.Fatalf("final: progress entries = %d, want 2", got)
	}

	// Find subA's row + assert the terminal text shape (verify-done).
	var subARow, subBRow transcriptEntry
	for _, e := range m.transcript {
		if e.kind != entryResearchProgress {
			continue
		}
		switch e.subAgentID {
		case subA:
			subARow = e
		case subB:
			subBRow = e
		}
	}
	if !strings.Contains(subARow.text, "research done") {
		t.Errorf("subA final text should say 'research done', got %q", subARow.text)
	}
	if subARow.isError {
		t.Error("subA terminal row marked error")
	}
	if !strings.Contains(subBRow.text, "research failed") {
		t.Errorf("subB final text should say 'research failed', got %q", subBRow.text)
	}
	if !subBRow.isError {
		t.Error("subB terminal row not marked error")
	}
}

// TestApplyEvent_ResearchPhase_BadPayloadSurfacesSystemNote covers the
// defensive branch in applyEvent: malformed JSON shouldn't crash, it
// should land as a system-note row.
func TestApplyEvent_ResearchPhase_BadPayloadSurfacesSystemNote(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000RES014"
	seedAgent(t, log, agentID, "bad payload", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.applyEvent(agent.Event{
		AgentID: "r-junk",
		TS:      time.Now().UTC(),
		Type:    agent.EvtResearchPhase,
		Payload: []byte("not json"),
	})
	if countProgressEntries(m) != 0 {
		t.Error("bad payload should NOT create a progress entry")
	}
	found := false
	for _, e := range m.transcript {
		if e.kind == entrySystemNote && strings.Contains(e.text, "bad payload") {
			found = true
			break
		}
	}
	if !found {
		t.Error("bad payload should surface as a system note")
	}
}

// TestFormatResearchProgress_AllStates pins the format helper output
// shape for every state transition so future tweaks land deliberately.
func TestFormatResearchProgress_AllStates(t *testing.T) {
	cases := []struct {
		name    string
		payload agent.ResearchPhasePayload
		want    string
	}{
		{
			name:    "in-flight start",
			payload: agent.ResearchPhasePayload{Phase: "search"},
			want:    "🔬 research: search",
		},
		{
			name:    "non-terminal done",
			payload: agent.ResearchPhasePayload{Phase: "search", Done: true, Elapsed: 3500 * time.Millisecond},
			want:    "🔬 research: search · 3.5s",
		},
		{
			name:    "terminal verify done",
			payload: agent.ResearchPhasePayload{Phase: "verify", Done: true, Elapsed: 12500 * time.Millisecond},
			want:    "🔬 research done · 13s",
		},
		{
			name:    "failed",
			payload: agent.ResearchPhasePayload{Phase: "fetch", Done: true, Err: "context canceled"},
			want:    "🔬 research failed: context canceled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatResearchProgress(tc.payload)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFormatResearchProgress_TruncatesLongErr makes sure a multi-KB
// engine error doesn't blow up the transcript row.
func TestFormatResearchProgress_TruncatesLongErr(t *testing.T) {
	long := strings.Repeat("x", 1000)
	got := formatResearchProgress(agent.ResearchPhasePayload{
		Phase: "fetch", Done: true, Err: long,
	})
	if len(got) > 200 {
		t.Errorf("expected truncated output, got length %d: %q", len(got), got)
	}
	if !strings.Contains(got, "…") {
		t.Error("expected ellipsis suffix on long error")
	}
}

// TestRunResearchAsync_SpawnErrorWritesAssistantMessage covers the
// rare-but-real case where the spawner itself returns an error before
// any phase events fire. The chat should surface the failure rather
// than silently leaving the conversation hanging.
func TestRunResearchAsync_SpawnErrorWritesAssistantMessage(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000RES015"
	seedAgent(t, log, agentID, "spawn err", "fake")

	fe := &fakeResearchEngine{}
	sp := &fakeSpawner{spawnErr: errors.New("engine not wired upstream")}

	m := New(log, agentID, NewMemTextSource(),
		WithResearchEngine(fe),
		WithResearchSpawner(sp))
	m = drive(t, m, 120, 30)

	c, _ := slash.Parse("/research will spawn fail")
	cmd := m.dispatchSlash(c)
	if cmd == nil {
		t.Fatal("dispatchSlash returned nil")
	}
	_ = cmd() // drain status

	// Poll for the failure assistant message.
	deadline := time.Now().Add(2 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		evs, err := log.Read(context.Background(), agentID, 0)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		for _, ev := range evs {
			if ev.Type == agent.EvtAssistantMessage {
				var p agent.MessagePayload
				_ = json.Unmarshal(ev.Payload, &p)
				got = p.Text
				break
			}
		}
		if got != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got == "" {
		t.Fatal("no EvtAssistantMessage written on spawn error")
	}
	if !strings.Contains(got, "research failed") || !strings.Contains(got, "engine not wired upstream") {
		t.Errorf("error message shape unexpected:\n%s", got)
	}
}

// countProgressEntries is a tiny test helper for the collapse
// algorithm assertions above.
func countProgressEntries(m *Model) int {
	n := 0
	for _, e := range m.transcript {
		if e.kind == entryResearchProgress {
			n++
		}
	}
	return n
}
