package chat

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
)

// makeAgentToolCallEvent builds an EvtToolCall event for the "agent"
// sub-agent tool with the given JSON input. Mirrors makeToolCallEvent
// in activity_strip_test.go but threads a deterministic timestamp so
// elapsed-time assertions are stable.
func makeAgentToolCallEvent(t *testing.T, inputJSON string, at time.Time) agent.Event {
	t.Helper()
	payload, err := json.Marshal(agent.ToolCall{Name: agentToolName, Input: []byte(inputJSON)})
	if err != nil {
		t.Fatalf("marshal tool call: %v", err)
	}
	return agent.Event{
		TS:      at,
		Type:    agent.EvtToolCall,
		Payload: payload,
	}
}

// makeAgentToolResultEvent emits the matching EvtToolResult event with
// the supplied body + error flag. Used to drive the card through its
// running → done / failed state transition.
func makeAgentToolResultEvent(t *testing.T, output string, isErr bool, at time.Time) agent.Event {
	t.Helper()
	payload, err := json.Marshal(agent.ToolResult{Name: agentToolName, Output: []byte(output), IsError: isErr})
	if err != nil {
		t.Fatalf("marshal tool result: %v", err)
	}
	return agent.Event{
		TS:      at,
		Type:    agent.EvtToolResult,
		Payload: payload,
	}
}

// TestAgentEntryIsTagged pins the ingest path: an "agent" tool_call
// with a well-formed objective surfaces as a transcriptEntry with
// isAgent=true and agentObjective populated.
func TestAgentEntryIsTagged(t *testing.T) {
	m := newStripTestModel()
	at := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	m.applyEvent(makeAgentToolCallEvent(t, `{"objective":"do X","output_format":"text"}`, at))

	e := firstToolEntry(t, m)
	if !e.isAgent {
		t.Errorf("isAgent = false, want true")
	}
	if e.agentObjective != "do X" {
		t.Errorf("agentObjective = %q, want %q", e.agentObjective, "do X")
	}
	if !e.toolCalledAt.Equal(at) {
		t.Errorf("toolCalledAt = %v, want %v", e.toolCalledAt, at)
	}
	if e.isSkill {
		t.Errorf("agent tool call wrongly tagged as skill")
	}
}

// TestAgentEntryMalformedInput verifies that a parse failure does NOT
// suppress the isAgent flag; the entry must still peel out of the
// strip even if the objective text is missing. Otherwise a malformed
// call would silently collapse back into the strip.
func TestAgentEntryMalformedInput(t *testing.T) {
	m := newStripTestModel()
	m.applyEvent(makeAgentToolCallEvent(t, `[not valid json`, time.Now().UTC()))

	e := firstToolEntry(t, m)
	if !e.isAgent {
		t.Errorf("isAgent = false on malformed input; want true (tag survives parse failure)")
	}
	if e.agentObjective != "" {
		t.Errorf("agentObjective = %q, want empty", e.agentObjective)
	}
}

// TestAgentEntryResultTracksTimestamps proves the EvtToolResult handler
// stamps toolResultAt so the card can freeze its duration display when
// the sub-agent finishes.
func TestAgentEntryResultTracksTimestamps(t *testing.T) {
	m := newStripTestModel()
	call := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	done := call.Add(7 * time.Second)
	m.applyEvent(makeAgentToolCallEvent(t, `{"objective":"x"}`, call))
	m.applyEvent(makeAgentToolResultEvent(t, `{"final_text":"ok"}`, false, done))

	e := firstToolEntry(t, m)
	if !e.hasResult {
		t.Errorf("hasResult = false, want true")
	}
	if !e.toolResultAt.Equal(done) {
		t.Errorf("toolResultAt = %v, want %v", e.toolResultAt, done)
	}
}

// TestAgentEntryResultWithoutMatchingCallFallsBackToCard handles the
// replay-of-truncated-log path: a stray EvtToolResult with no prior
// matching call should still tag the fallback row as isAgent so the
// renderer doesn't silently re-collapse it into the strip.
func TestAgentEntryResultWithoutMatchingCallFallsBackToCard(t *testing.T) {
	m := newStripTestModel()
	m.applyEvent(makeAgentToolResultEvent(t, `{"error":"orphaned"}`, true, time.Now().UTC()))

	e := firstToolEntry(t, m)
	if !e.isAgent {
		t.Errorf("orphaned result row not tagged isAgent; want true")
	}
	if !e.isError {
		t.Errorf("isError = false, want true (error flag must propagate)")
	}
}

// TestAgentCardRendersRunning paints a still-in-flight card and pins
// the visible vocabulary: 🧢 emoji, "agent", "running", and the
// objective text all appear.
func TestAgentCardRendersRunning(t *testing.T) {
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: "scaffolding webgpu shader module for compute pass",
		toolCalledAt:   time.Now().Add(-12 * time.Second),
	}
	got := renderAgentCard(e, 80, nil)
	for _, want := range []string{agentCardEmoji, "agent", "running", "scaffolding webgpu shader"} {
		if !strings.Contains(got, want) {
			t.Errorf("running card missing %q in:\n%s", want, got)
		}
	}
}

// TestAgentCardRendersDone flips hasResult on and asserts the "done in"
// vocabulary takes over and the duration appears.
func TestAgentCardRendersDone(t *testing.T) {
	call := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: "scaffolding webgpu shader module",
		toolCalledAt:   call,
		toolResultAt:   call.Add(47 * time.Second),
		hasResult:      true,
	}
	got := renderAgentCard(e, 80, nil)
	if !strings.Contains(got, "done in") {
		t.Errorf("done card missing 'done in':\n%s", got)
	}
	if !strings.Contains(got, "47s") {
		t.Errorf("done card missing duration '47s':\n%s", got)
	}
	if !strings.Contains(got, "scaffolding") {
		t.Errorf("done card missing objective:\n%s", got)
	}
}

// TestAgentCardRendersFailed exercises the isError branch and confirms
// the error text from toolResult wins over the objective on the body
// line; a failed run's "context canceled" is more informative than
// the stale objective.
func TestAgentCardRendersFailed(t *testing.T) {
	call := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: "build a thing",
		toolResult:     "context canceled",
		toolCalledAt:   call,
		toolResultAt:   call.Add(8 * time.Second),
		hasResult:      true,
		isError:        true,
	}
	got := renderAgentCard(e, 80, nil)
	if !strings.Contains(got, "failed in") {
		t.Errorf("failed card missing 'failed in':\n%s", got)
	}
	if !strings.Contains(got, "context canceled") {
		t.Errorf("failed card missing error text:\n%s", got)
	}
	if !strings.Contains(got, "8s") {
		t.Errorf("failed card missing duration:\n%s", got)
	}
}

// TestAgentCardRendersFailedJSONError parses the typical agent tool's
// {"error":"..."} payload and surfaces just the error message, not the
// raw JSON.
func TestAgentCardRendersFailedJSONError(t *testing.T) {
	call := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: "build a thing",
		toolResult:     `{"agent_id":"x","error":"depth cap exceeded"}`,
		toolCalledAt:   call,
		toolResultAt:   call.Add(3 * time.Second),
		hasResult:      true,
		isError:        true,
	}
	got := renderAgentCard(e, 80, nil)
	if !strings.Contains(got, "depth cap exceeded") {
		t.Errorf("failed card missing parsed error: %q in:\n%s", "depth cap exceeded", got)
	}
}

// TestAgentCardRendersFailedFallsBackToObjective covers the no-body
// error case: when toolResult is empty (or yields no error text) the
// body falls back to the objective so the card stays informative.
func TestAgentCardRendersFailedFallsBackToObjective(t *testing.T) {
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: "research X",
		toolResult:     "",
		toolCalledAt:   time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		toolResultAt:   time.Date(2025, 1, 1, 12, 0, 5, 0, time.UTC),
		hasResult:      true,
		isError:        true,
	}
	got := renderAgentCard(e, 80, nil)
	if !strings.Contains(got, "research X") {
		t.Errorf("failed card missing objective fallback:\n%s", got)
	}
}

// TestFormatAgentDuration walks the breakpoint table in the spec.
func TestFormatAgentDuration(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"sub-second", 50 * time.Millisecond, "<1s"},
		{"zero", 0, "<1s"},
		{"negative clamps", -1 * time.Second, "<1s"},
		{"one second", 1 * time.Second, "1s"},
		{"twelve seconds", 12 * time.Second, "12s"},
		{"fifty-nine seconds", 59 * time.Second, "59s"},
		{"one minute exact", 60 * time.Second, "1m 0s"},
		{"ninety-five seconds", 95 * time.Second, "1m 35s"},
		{"599s still has seconds", 599 * time.Second, "9m 59s"},
		{"ten minutes flat", 600 * time.Second, "10m+"},
		{"166 minutes", 9999 * time.Second, "166m+"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatAgentDuration(c.in); got != c.want {
				t.Errorf("formatAgentDuration(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestAgentEntryBreaksStripFold proves the wiring: a transcript with
// mixed tool calls and a sub-agent in the middle splits into a strip
// before, a standalone card, then a strip after. The strips MUST NOT
// be a single fold over the agent entry.
func TestAgentEntryBreaksStripFold(t *testing.T) {
	entries := []transcriptEntry{
		{kind: entryToolCall, tool: "bash", hasResult: true, toolResult: "ok"},
		{kind: entryToolCall, tool: "glob", hasResult: true, toolResult: "a\nb"},
		{
			kind:           entryToolCall,
			tool:           agentToolName,
			isAgent:        true,
			agentObjective: "go deep",
			toolCalledAt:   time.Now(),
		},
		{kind: entryToolCall, tool: "read", hasResult: true, toolResult: "x"},
		{kind: entryToolCall, tool: "write", hasResult: true, toolResult: "y"},
	}
	got := composeTranscript(entries, "", "", nil, nil, 100)

	if !strings.Contains(got, agentCardEmoji) {
		t.Errorf("expected agent card emoji in output:\n%s", got)
	}
	// The bash + glob strip lands BEFORE the agent card and contains
	// both tool names. The read + write strip lands AFTER and contains
	// the other two.
	agentIdx := strings.Index(got, agentCardEmoji)
	if agentIdx < 0 {
		t.Fatalf("agent emoji not found")
	}
	before := got[:agentIdx]
	after := got[agentIdx:]
	if !strings.Contains(before, "bash") || !strings.Contains(before, "glob") {
		t.Errorf("expected bash+glob strip BEFORE agent card; got before=\n%s", before)
	}
	if !strings.Contains(after, "read") || !strings.Contains(after, "write") {
		t.Errorf("expected read+write strip AFTER agent card; got after=\n%s", after)
	}
	// The bash+glob run must NOT include 'read' or 'write'; that
	// would indicate the strip absorbed the agent entry's neighbors.
	if strings.Contains(before, "read") || strings.Contains(before, "write") {
		t.Errorf("strip before agent leaked the post-agent tools:\n%s", before)
	}
}

// TestAgentEntryIsSoloRenderedWhenNoNeighbors confirms a lone agent
// entry (no surrounding tool calls) still surfaces as a card and not
// as some degenerate single-entry strip.
func TestAgentEntryIsSoloRenderedWhenNoNeighbors(t *testing.T) {
	entries := []transcriptEntry{
		{
			kind:           entryToolCall,
			tool:           agentToolName,
			isAgent:        true,
			agentObjective: "scout the repo",
			toolCalledAt:   time.Now(),
		},
	}
	got := composeTranscript(entries, "", "", nil, nil, 100)
	if !strings.Contains(got, agentCardEmoji) {
		t.Errorf("solo agent entry should render as card:\n%s", got)
	}
	// The strip's chevron glyph must not appear; it would mean
	// the entry leaked into the strip path.
	if strings.Contains(got, stripGlyphTool+"  "+agentToolName) {
		t.Errorf("solo agent entry leaked into strip:\n%s", got)
	}
}

// TestAgentCardWithLongObjective truncates the body line so the card
// never wraps past its content width. The visible body must end with
// the ellipsis sentinel and stay within the inner box width.
func TestAgentCardWithLongObjective(t *testing.T) {
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: strings.Repeat("scaffold and audit ", 30), // ~570 chars
		toolCalledAt:   time.Now(),
	}
	got := renderAgentCard(e, 60, nil)

	// Each rendered line must fit within the card's outer width
	// (inner box plus side margin). We strip ANSI so width math is
	// only counting visible cells.
	maxWidth := 60
	for _, ln := range strings.Split(got, "\n") {
		stripped := stripChatANSI(ln)
		if lipgloss.Width(stripped) > maxWidth {
			t.Errorf("rendered line exceeds width %d: %d cells: %q", maxWidth, lipgloss.Width(stripped), stripped)
		}
	}
	// The body line must end with the truncation marker since the
	// objective vastly overflows the budget.
	if !strings.Contains(got, "…") {
		t.Errorf("expected truncation ellipsis in output:\n%s", got)
	}
}

// TestAgentCardElapsedUpdatesAcrossRenders swaps the package-private
// clock seam to advance time between two renders. The same entry's
// "running Ns" string must show two different durations.
func TestAgentCardElapsedUpdatesAcrossRenders(t *testing.T) {
	saved := nowFn
	t.Cleanup(func() { nowFn = saved })

	call := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: "x",
		toolCalledAt:   call,
	}

	// First render at t = call + 3s
	nowFn = func() time.Time { return call.Add(3 * time.Second) }
	first := renderAgentCard(e, 80, nil)
	if !strings.Contains(first, "3s") {
		t.Errorf("first render missing '3s':\n%s", first)
	}

	// Second render at t = call + 8s
	nowFn = func() time.Time { return call.Add(8 * time.Second) }
	second := renderAgentCard(e, 80, nil)
	if !strings.Contains(second, "8s") {
		t.Errorf("second render missing '8s':\n%s", second)
	}

	if first == second {
		t.Errorf("expected first and second renders to differ; got identical:\n%s", first)
	}
}

// TestParseAgentObjective walks the helper directly so the JSON
// extractor has its own unit coverage independent of the apply-event
// pipeline.
func TestParseAgentObjective(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"happy", []byte(`{"objective":"do X"}`), "do X"},
		{"trimmed", []byte(`{"objective":"   trimmed   "}`), "trimmed"},
		{"missing field", []byte(`{"other":"x"}`), ""},
		{"empty body", []byte(``), ""},
		{"nil", nil, ""},
		{"malformed", []byte(`{`), ""},
		{"empty objective", []byte(`{"objective":""}`), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseAgentObjective(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestStripRollupNeverFoldsAgent guards the defense-in-depth rule:
// even if an agent entry sneaks into a strip group, it must not fold
// with a neighboring same-name tool call. Production code already
// peels agents out via composeTranscript, but the rollup helper is
// called by other tests/callers and must hold the invariant alone.
func TestStripRollupNeverFoldsAgent(t *testing.T) {
	es := []transcriptEntry{
		{tool: agentToolName, isAgent: true, hasResult: true},
		{tool: agentToolName, isAgent: true, hasResult: true},
	}
	out := stripRollup(es)
	if len(out) != 2 {
		t.Errorf("two agent entries folded into %d segments; want 2 (no folding)", len(out))
	}
}

// TestStripRollupAgentBreaksFold also confirms an agent entry sitting
// between two same-name regular tool calls splits the rollup into
// three segments instead of one.
func TestStripRollupAgentBreaksFold(t *testing.T) {
	es := []transcriptEntry{
		{tool: "bash", hasResult: true},
		{tool: agentToolName, isAgent: true, hasResult: true},
		{tool: "bash", hasResult: true},
	}
	out := stripRollup(es)
	if len(out) != 3 {
		t.Errorf("expected 3 segments after agent split; got %d (%+v)", len(out), out)
	}
}

// TestExtractAgentErrorText pins the small JSON walker that pulls the
// error message out of the agent tool's structured failure payload.
func TestExtractAgentErrorText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"json error", `{"agent_id":"x","error":"oops"}`, "oops"},
		{"plain string", "context canceled", "context canceled"},
		{"multi-line plain", "first line\nsecond line", "first line"},
		{"empty", "", ""},
		{"json without error", `{"agent_id":"x"}`, `{"agent_id":"x"}`},
		{"json with whitespace error", `{"error":"   "}`, `{"error":"   "}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractAgentErrorText(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestRenderAgentCard_NarrowWidth still produces a card at a very
// tight viewport: the floor kicks in and the card renders rather
// than crashing or producing zero-width content.
func TestRenderAgentCard_NarrowWidth(t *testing.T) {
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: "x",
		toolCalledAt:   time.Now(),
	}
	got := renderAgentCard(e, 20, nil)
	if got == "" {
		t.Fatalf("narrow width produced empty render")
	}
	if !strings.Contains(got, agentCardEmoji) {
		t.Errorf("narrow card missing emoji:\n%s", got)
	}
}

// TestAgentCardStateRunningWithoutCalledAt defends against a corrupt
// or pre-stamping entry: a missing toolCalledAt must not panic and
// the state line still produces a "running" label.
func TestAgentCardStateRunningWithoutCalledAt(t *testing.T) {
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: "x",
	}
	state, dur, _ := agentCardState(e)
	if state != "running" {
		t.Errorf("state = %q, want %q", state, "running")
	}
	if dur != "<1s" {
		t.Errorf("dur with zero toolCalledAt = %q, want %q", dur, "<1s")
	}
}

// TestFirstNonEmptyLine_AllEmpty exercises the all-blank fallthrough
// so the helper's defensive `return ""` is covered.
func TestFirstNonEmptyLine_AllEmpty(t *testing.T) {
	if got := firstNonEmptyLine("\n\n   \n"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestClampAgentLine_Overflow covers the "line exceeds budget" branch
// even though the implementation today is a passthrough (the strip's
// best-effort policy applies). Coverage AND a forward-compat assertion
// that the caller never gets nil when the budget is blown.
func TestClampAgentLine_Overflow(t *testing.T) {
	long := strings.Repeat("x", 200)
	if got := clampAgentLine(long, 10); got != long {
		t.Errorf("clampAgentLine returned mutated content on overflow; want passthrough")
	}
}

// TestRenderAgentCard_ContentFloorClamps exercises the contentW < 16
// AND bodyBudget < 4 floor branches by squeezing the viewport width
// well below the practical minimum. The card must still produce non-
// empty output so the renderer never silently drops a sub-agent row.
func TestRenderAgentCard_ContentFloorClamps(t *testing.T) {
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: "x",
		toolCalledAt:   time.Now(),
	}
	got := renderAgentCard(e, 1, nil) // absurdly tight; floors kick in
	if got == "" {
		t.Fatalf("absurd width produced empty card")
	}
}

// TestAgentCardStateDoneWithoutResultAt covers the (paranoid) replay
// case where a hasResult entry never got its toolResultAt stamped.
// State must read "done in" with a "<1s" placeholder rather than a
// negative or undefined duration.
func TestAgentCardStateDoneWithoutResultAt(t *testing.T) {
	e := transcriptEntry{
		kind:         entryToolCall,
		tool:         agentToolName,
		isAgent:      true,
		hasResult:    true,
		toolCalledAt: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	state, dur, _ := agentCardState(e)
	if state != "done in" {
		t.Errorf("state = %q, want %q", state, "done in")
	}
	if dur != "<1s" {
		t.Errorf("dur with zero toolResultAt = %q, want %q", dur, "<1s")
	}
}

// TestAgentCardIntegrationApplyAndRender drives the full pipeline:
// EvtToolCall → renderEntry → assert visible. This is the closest the
// unit tests get to the production rerenderViewport path.
func TestAgentCardIntegrationApplyAndRender(t *testing.T) {
	m := newStripTestModel()
	at := time.Now().UTC().Add(-5 * time.Second)
	m.applyEvent(makeAgentToolCallEvent(t, `{"objective":"draft the schema"}`, at))

	// renderEntry resolves the transcriptEntry via the renderEntry
	// switch, which is the production seam composeTranscript hands
	// each entry to for solo rendering.
	e := firstToolEntry(t, m)
	out := renderEntry(e, nil, nil, 80)
	if !strings.Contains(out, "draft the schema") {
		t.Errorf("rendered card missing objective:\n%s", out)
	}
	if !strings.Contains(out, agentCardEmoji) {
		t.Errorf("rendered card missing emoji:\n%s", out)
	}
}

// TestAgentCardRunningUsesLastTool pins the live-tool body line: a
// running card whose matching child snapshot has a non-empty LastTool
// must surface "running {tool}" in the body, NOT the static objective.
// This is the feature the spec was written for.
func TestAgentCardRunningUsesLastTool(t *testing.T) {
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: "do thing",
		toolCalledAt:   time.Now().Add(-3 * time.Second),
	}
	snaps := []ChildSnapshot{
		{AgentID: "child", State: agent.StateRunning, LastEvent: "do thing", LastTool: "bash"},
	}
	got := renderAgentCard(e, 80, snaps)
	if !strings.Contains(got, "running bash") {
		t.Errorf("expected body to contain 'running bash'; got:\n%s", got)
	}
	// The static objective must not surface on the body line when the
	// live tool is available, otherwise the user sees stale info.
	// Check the body line specifically: the header has "running"
	// already, so the body assertion has to scope to "↳ ".
	if !strings.Contains(got, "↳") {
		t.Fatalf("rendered card missing body arrow:\n%s", got)
	}
	bodyIdx := strings.Index(got, "↳")
	body := got[bodyIdx:]
	if strings.Contains(body, "do thing") {
		t.Errorf("body line still contains the static objective; got body:\n%s", body)
	}
}

// TestAgentCardRunningFallsBackToObjectiveWhenNoMatch covers the no-
// snapshot path: with snaps empty, the body reads as the static
// objective.
func TestAgentCardRunningFallsBackToObjectiveWhenNoMatch(t *testing.T) {
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: "do thing",
		toolCalledAt:   time.Now().Add(-3 * time.Second),
	}
	got := renderAgentCard(e, 80, nil)
	if !strings.Contains(got, "do thing") {
		t.Errorf("expected body to fall back to objective; got:\n%s", got)
	}
}

// TestAgentCardRunningFallsBackWhenLastToolEmpty covers the
// just-spawned path: the snapshot matches by title but the sub-agent
// has not yet emitted any tool calls, so LastTool is empty. Body
// falls back to objective.
func TestAgentCardRunningFallsBackWhenLastToolEmpty(t *testing.T) {
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: "do thing",
		toolCalledAt:   time.Now().Add(-3 * time.Second),
	}
	snaps := []ChildSnapshot{
		{AgentID: "child", State: agent.StateRunning, LastEvent: "do thing", LastTool: ""},
	}
	got := renderAgentCard(e, 80, snaps)
	if !strings.Contains(got, "do thing") {
		t.Errorf("expected body to fall back to objective when LastTool is empty; got:\n%s", got)
	}
}

// TestAgentCardRunningSkipsAmbiguousMatch covers the duplicate-title
// concurrent-spawn edge case: when two children share the same Title
// (rare orchestrator fan-out), pick neither and fall back to the
// objective so the body never picks a wrong tool.
func TestAgentCardRunningSkipsAmbiguousMatch(t *testing.T) {
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: "do thing",
		toolCalledAt:   time.Now().Add(-3 * time.Second),
	}
	snaps := []ChildSnapshot{
		{AgentID: "child-a", State: agent.StateRunning, LastEvent: "do thing", LastTool: "bash"},
		{AgentID: "child-b", State: agent.StateRunning, LastEvent: "do thing", LastTool: "glob"},
	}
	got := renderAgentCard(e, 80, snaps)
	if strings.Contains(got, "running bash") || strings.Contains(got, "running glob") {
		t.Errorf("ambiguous match must not pick either tool; got:\n%s", got)
	}
	if !strings.Contains(got, "do thing") {
		t.Errorf("ambiguous match should fall back to objective; got:\n%s", got)
	}
}

// TestAgentCardDoneIgnoresLastTool covers the post-completion path:
// once hasResult is true, we surface the objective rather than a
// stale tool name. The card has finished its run; the live signal is
// no longer relevant.
func TestAgentCardDoneIgnoresLastTool(t *testing.T) {
	call := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	e := transcriptEntry{
		kind:           entryToolCall,
		tool:           agentToolName,
		isAgent:        true,
		agentObjective: "do thing",
		toolCalledAt:   call,
		toolResultAt:   call.Add(5 * time.Second),
		hasResult:      true,
	}
	snaps := []ChildSnapshot{
		{AgentID: "child", State: agent.StateDone, LastEvent: "do thing", LastTool: "bash"},
	}
	got := renderAgentCard(e, 80, snaps)
	if strings.Contains(got, "running bash") {
		t.Errorf("done card should not surface running tool; got:\n%s", got)
	}
	if !strings.Contains(got, "do thing") {
		t.Errorf("done card should surface objective; got:\n%s", got)
	}
}

// TestMatchAgentChild walks the matching helper directly across its
// happy / fallback / ambiguous / empty-objective branches.
func TestMatchAgentChild(t *testing.T) {
	cases := []struct {
		name      string
		objective string
		snaps     []ChildSnapshot
		wantOK    bool
		wantTool  string
	}{
		{
			name:      "happy single match",
			objective: "do thing",
			snaps: []ChildSnapshot{
				{LastEvent: "do thing", LastTool: "bash"},
			},
			wantOK:   true,
			wantTool: "bash",
		},
		{
			name:      "trims whitespace on both sides",
			objective: "  do thing  ",
			snaps: []ChildSnapshot{
				{LastEvent: "do thing", LastTool: "bash"},
			},
			wantOK:   true,
			wantTool: "bash",
		},
		{
			name:      "no match",
			objective: "do thing",
			snaps: []ChildSnapshot{
				{LastEvent: "other", LastTool: "bash"},
			},
			wantOK: false,
		},
		{
			name:      "empty objective never matches",
			objective: "",
			snaps: []ChildSnapshot{
				{LastEvent: "do thing", LastTool: "bash"},
			},
			wantOK: false,
		},
		{
			name:      "ambiguous match yields none",
			objective: "do thing",
			snaps: []ChildSnapshot{
				{LastEvent: "do thing", LastTool: "bash"},
				{LastEvent: "do thing", LastTool: "glob"},
			},
			wantOK: false,
		},
		{
			name:      "nil snaps",
			objective: "do thing",
			snaps:     nil,
			wantOK:    false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := transcriptEntry{agentObjective: c.objective}
			hit, ok := matchAgentChild(e, c.snaps)
			if ok != c.wantOK {
				t.Errorf("ok = %v, want %v", ok, c.wantOK)
			}
			if c.wantOK && hit.LastTool != c.wantTool {
				t.Errorf("LastTool = %q, want %q", hit.LastTool, c.wantTool)
			}
		})
	}
}
