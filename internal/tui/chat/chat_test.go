package chat

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/tui/slash"
)

// openTempLog spins up a fresh SQLite event log in a t.TempDir() so
// every test starts with a known-empty database.
func openTempLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	dir := t.TempDir()
	log, err := agent.OpenSQLiteEventLog(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// seedAgent writes a `created` event for agentID so the projection has a
// row to report. Returns the seq of the created event.
func seedAgent(t *testing.T, log *agent.SQLiteEventLog, agentID, title, model string) int64 {
	t.Helper()
	payload, err := agent.NewStateChangeCreated(agent.AgentCreated{
		ID:     agentID,
		RootID: agentID,
		Title:  title,
		Model:  model,
	})
	if err != nil {
		t.Fatalf("created payload: %v", err)
	}
	seq, err := log.Append(context.Background(), agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtStateChange,
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("append created: %v", err)
	}
	return seq
}

func seedUserMessage(t *testing.T, log *agent.SQLiteEventLog, agentID, text string) {
	t.Helper()
	payload, err := json.Marshal(agent.MessagePayload{Text: text})
	if err != nil {
		t.Fatalf("marshal user msg: %v", err)
	}
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtUserMessage,
		Payload: payload,
	}); err != nil {
		t.Fatalf("append user msg: %v", err)
	}
}

func seedToolCall(t *testing.T, log *agent.SQLiteEventLog, agentID, toolName string) {
	t.Helper()
	payload, err := json.Marshal(agent.ToolCall{Name: toolName})
	if err != nil {
		t.Fatalf("marshal tool call: %v", err)
	}
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtToolCall,
		Payload: payload,
	}); err != nil {
		t.Fatalf("append tool call: %v", err)
	}
}

// drive synchronously feeds Init's normal sequence to the model without
// running a bubbletea Program: it manually issues the backfill Read and
// the WindowSizeMsg, then calls View() once so the viewport sizes itself
// and composeTranscript runs.
func drive(t *testing.T, m *Model, width, height int) *Model {
	t.Helper()
	_ = m.Init()

	evs, err := m.log.Read(context.Background(), m.agentID, 0)
	if err != nil {
		t.Fatalf("backfill read: %v", err)
	}
	updated, _ := m.Update(backfillMsg{events: evs})
	m = updated.(*Model)

	updated, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = updated.(*Model)

	_ = m.View()
	return m
}

// TestReadOnlyViewer_RendersSeededTranscript walks the read-only path:
// seed a created + user_message + tool_call, construct the model, drive
// Init + WindowSize, snapshot key substrings in the rendered View.
func TestReadOnlyViewer_RendersSeededTranscript(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000001"
	seedAgent(t, log, agentID, "test agent", "claude-4.7-sonnet")
	seedUserMessage(t, log, agentID, "hello carlos")
	seedToolCall(t, log, agentID, "bash")

	src := NewMemTextSource()
	src.Append(agentID, "Hi! I'll take a look.")

	m := New(log, agentID, src)
	m = drive(t, m, 120, 30)

	view := m.View()

	mustContain(t, view, "hello carlos") // user message
	mustContain(t, view, "👤")          // user avatar
	mustContain(t, view, "🔧")          // tool-call icon (slice: tool visibility)
	mustContain(t, view, "bash")         // tool name
	mustContain(t, view, "🧢")          // live assistant avatar
	mustContain(t, view, "Hi! I'll take a look.")
	mustContain(t, view, "spawning")   // state badge label from projection (slice 9c: glyph + label inside brackets)
	mustContain(t, view, "◐")          // slice 9c: spawning glyph from theme.StateGlyph
	mustContain(t, view, "claude-4.7") // model id in header
}

// TestReadOnlyViewer_TooSmallTerminal asserts the chat view refuses to
// render below the minimum cell budget instead of producing broken
// output. Matches onboarding's "decline-to-render" posture.
func TestReadOnlyViewer_TooSmallTerminal(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000000XX"
	seedAgent(t, log, agentID, "tiny", "fake")

	m := New(log, agentID, NewMemTextSource())
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	m = updated.(*Model)
	view := m.View()

	if !strings.Contains(view, "at least") {
		t.Fatalf("expected too-small message, got:\n%s", view)
	}
}

// TestReadOnlyViewer_NoInputLineWhenReadOnly verifies WithReadOnly()
// suppresses the textarea row — chat-as-pure-viewer should not even
// hint at input capability.
func TestReadOnlyViewer_NoInputLineWhenReadOnly(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000002"
	seedAgent(t, log, agentID, "viewer-only", "fake")

	m := New(log, agentID, NewMemTextSource(), WithReadOnly())
	m = drive(t, m, 120, 30)
	view := m.View()

	if strings.Contains(view, "type a message") {
		t.Fatalf("read-only view leaked input placeholder:\n%s", view)
	}
}

// TestSubmit_AppendsUserMessageEvent simulates typing into the textarea
// and pressing enter. Asserts:
//   - the textarea is cleared
//   - a user_message event lands in the log
//   - the event flows back through the subscription path into the
//     transcript (we run the pumped event manually to keep the test sync)
func TestSubmit_AppendsUserMessageEvent(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000005"
	seedAgent(t, log, agentID, "submit test", "fake")

	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.ta.SetValue("hello from test")
	cmd := m.submit()
	if cmd == nil {
		t.Fatalf("submit returned nil cmd for non-empty input")
	}
	// Drain the side-effect synchronously.
	msg := cmd()
	if em, ok := msg.(errMsg); ok && em.err != nil {
		t.Fatalf("submit Cmd produced errMsg: %v", em.err)
	}

	// Textarea should now be empty.
	if v := m.ta.Value(); v != "" {
		t.Fatalf("textarea not cleared post-submit: %q", v)
	}

	// Log should have grown by exactly one user_message event.
	evs, err := log.Read(context.Background(), agentID, 0)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var userMsgs int
	for _, ev := range evs {
		if ev.Type == agent.EvtUserMessage {
			userMsgs++
			var p agent.MessagePayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("unmarshal user payload: %v", err)
			}
			if p.Text != "hello from test" {
				t.Errorf("payload text = %q, want %q", p.Text, "hello from test")
			}
		}
	}
	if userMsgs != 1 {
		t.Fatalf("expected 1 user_message event, got %d", userMsgs)
	}

	// Feed the event into Update so the transcript reflects it (in
	// production the subscription pump does this; here we do it by hand
	// to keep the test deterministic).
	for _, ev := range evs {
		if ev.Type == agent.EvtUserMessage {
			updated, _ := m.Update(eventMsg{ev: ev})
			m = updated.(*Model)
		}
	}
	if v := m.View(); !strings.Contains(v, "hello from test") {
		t.Fatalf("submitted text missing from view:\n%s", v)
	}
}

// TestSubmit_EmptyInputIsNoOp guards the common typo-and-bail case.
func TestSubmit_EmptyInputIsNoOp(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000006"
	seedAgent(t, log, agentID, "empty submit", "fake")

	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	m.ta.SetValue("   \n  ")
	if cmd := m.submit(); cmd != nil {
		t.Fatalf("submit returned non-nil cmd for whitespace-only input")
	}
}

// TestSubmit_SlashCommandNeverHitsLog verifies a slash-prefixed line is
// recognized BEFORE it could be appended as a user_message. Without
// this guard, the user typing `/help` would persist a row to the event
// log — wrong by SPEC ("any input line beginning with `/` is a slash
// command — a directive to carlos itself, not a prompt sent to the
// model").
func TestSubmit_SlashCommandNeverHitsLog(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000007"
	seedAgent(t, log, agentID, "slash test", "fake")
	preCount := countUserMessages(t, log, agentID)

	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	// /help opens the overlay (slice 9d): no cmd, no log write.
	// Use /clear instead — it echoes a statusMsg AND demonstrates
	// the "slash commands never touch the log" invariant.
	m.ta.SetValue("/clear")
	cmd := m.submit()
	if cmd == nil {
		t.Fatalf("submit returned nil for slash command")
	}
	msg := cmd()
	st, ok := msg.(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg from /clear, got %T (%v)", msg, msg)
	}
	if !strings.Contains(st.text, "cleared") {
		t.Errorf("status text missing 'cleared': %q", st.text)
	}

	// /help also doesn't append a user_message — separate assertion
	// since it's the overlay-toggle verb, not a status-echo verb.
	m.ta.SetValue("/help")
	_ = m.submit() // nil cmd is fine; it just sets m.showHelp
	if !m.showHelp {
		t.Error("/help didn't set showHelp")
	}

	if got := countUserMessages(t, log, agentID); got != preCount {
		t.Fatalf("slash command appended user_message event: pre=%d post=%d", preCount, got)
	}
}

// TestDispatchSlash_KnownVerbsEcho cycles every Builtin and asserts
// dispatchSlash returns SOME tea.Msg. /exit returns nil (Quit cmd
// special-cases the model), so we skip it; everything else echoes a
// statusMsg (clear/help with content, others with the "pending — slice
// 1f" note).
func TestDispatchSlash_KnownVerbsEcho(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000008"
	seedAgent(t, log, agentID, "echo verbs", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	// /agents is excluded — it triggers a Quit + sets OpenManageRequested
	// (slice 7g), covered by TestDispatchSlash_AgentsOpensManage.
	// /help is excluded — it opens the overlay (slice 9d) instead of
	// echoing a status, covered by TestDispatchSlash_HelpOpensOverlay.
	for _, b := range []string{"clear", "compact", "model", "review", "insights", "skills", "memory", "schedule", "daemon"} {
		c, err := slash.Parse("/" + b)
		if err != nil {
			t.Fatalf("parse /%s: %v", b, err)
		}
		cmd := m.dispatchSlash(c)
		if cmd == nil {
			t.Errorf("dispatchSlash(/%s) returned nil", b)
			continue
		}
		msg := cmd()
		if _, ok := msg.(statusMsg); !ok {
			t.Errorf("dispatchSlash(/%s) returned %T, want statusMsg", b, msg)
		}
	}
}

// TestDispatchSlash_HelpOpensOverlay proves /help sets the
// showHelp flag (slice 9d) and that any subsequent keystroke
// dismisses it.
func TestDispatchSlash_HelpOpensOverlay(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV000000000000000000000B"
	seedAgent(t, log, agentID, "help overlay", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	if m.showHelp {
		t.Fatal("showHelp true before /help fired")
	}
	c, _ := slash.Parse("/help")
	_ = m.dispatchSlash(c)
	if !m.showHelp {
		t.Error("showHelp false after /help")
	}
	// A non-ctrl-c keypress should dismiss.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	mm := next.(*Model)
	if mm.showHelp {
		t.Error("showHelp still true after a keystroke")
	}
}

// TestDispatchSlash_AgentsOpensManage proves the slice-7g handoff:
// /agents sets the openManage flag and returns tea.Quit. The caller
// (cmd/carlos.runChatDevAid) then relaunches into the manage TUI.
func TestDispatchSlash_AgentsOpensManage(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV000000000000000000000A"
	seedAgent(t, log, agentID, "open manage", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	if m.OpenManageRequested() {
		t.Fatal("OpenManageRequested true before /agents fired")
	}
	c, _ := slash.Parse("/agents")
	cmd := m.dispatchSlash(c)
	if cmd == nil {
		t.Fatal("/agents returned nil cmd (expected tea.Quit)")
	}
	if !m.OpenManageRequested() {
		t.Error("OpenManageRequested false after /agents")
	}
	if !m.quitting {
		t.Error("quitting false after /agents")
	}
}

// TestDispatchSlash_UnknownVerbWarns ensures the unknown-command path
// returns a warning-colored statusMsg pointing the user at /help.
func TestDispatchSlash_UnknownVerbWarns(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000009"
	seedAgent(t, log, agentID, "unknown verb", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	c, _ := slash.Parse("/florp")
	cmd := m.dispatchSlash(c)
	st := cmd().(statusMsg)
	if st.kind != statusWarn {
		t.Errorf("unknown verb status kind = %d, want statusWarn", st.kind)
	}
	if !strings.Contains(st.text, "unknown") {
		t.Errorf("status text missing 'unknown': %q", st.text)
	}
}

// TestSlashHelpTip_AppearsInFooter verifies the discoverability hint
// is present in the input-mode footer string. We check the raw footer
// (not the full View) so border / viewport content can't shadow the
// substring match.
func TestSlashHelpTip_AppearsInFooter(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000010"
	seedAgent(t, log, agentID, "footer tip", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	footer := m.renderFooter(100)
	if !strings.Contains(footer, "type /help for commands") {
		t.Fatalf("input-mode footer missing /help tip:\n%q", footer)
	}
}

func countUserMessages(t *testing.T, log *agent.SQLiteEventLog, agentID string) int {
	t.Helper()
	evs, err := log.Read(context.Background(), agentID, 0)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var n int
	for _, ev := range evs {
		if ev.Type == agent.EvtUserMessage {
			n++
		}
	}
	return n
}


// TestRenderEmptyState_GreetsByUserName confirms the slice-9g empty
// state panel addresses the user by their onboarded name (with "Boss"
// as the brand-voice default) and surfaces the example prompts so a
// fresh /clear or first launch doesn't paint a dead-empty viewport.
func TestRenderEmptyState_GreetsByUserName(t *testing.T) {
	out := renderEmptyState("Ada", 80, 30, false)
	if !strings.Contains(out, "Hey Ada") {
		t.Errorf("empty state missing name greeting: %q", out)
	}
	if !strings.Contains(out, "🧢") {
		t.Error("empty state missing cap glyph")
	}
	if !strings.Contains(out, "/help") {
		t.Error("empty state missing /help hint")
	}
}

func TestRenderEmptyState_DefaultsToBoss(t *testing.T) {
	out := renderEmptyState("", 80, 30, false)
	if !strings.Contains(out, "Hey Boss") {
		t.Errorf("empty userName should default to Boss; got %q", out)
	}
}

// TestRenderAvatarBlock_WrapIndentsContinuationLines is the regression
// check for the field-reported bug: multi-line assistant responses had
// the second+ lines flush to column 0 instead of aligned under the
// body. After the fix, continuation lines start with `indent` spaces
// (avatar + ": " = 4 cells).
func TestRenderAvatarBlock_WrapIndentsContinuationLines(t *testing.T) {
	// Width 30 forces a wrap after ~26 body cells (30 - indent 4).
	text := "I'm doing great, thank you for asking! How can I help you today?"
	out := renderAvatarBlock("🧢", ":", text, colorAgent, 30)
	lines := strings.Split(stripStyle(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 wrapped lines; got %d:\n%s", len(lines), out)
	}
	// Line 0 starts with the avatar.
	if !strings.HasPrefix(lines[0], "🧢:") {
		t.Errorf("line 0 missing avatar prefix: %q", lines[0])
	}
	// Lines 1+ start with the gutter (4 spaces) — no avatar.
	for i, ln := range lines[1:] {
		if !strings.HasPrefix(ln, "    ") {
			t.Errorf("line %d not indented to gutter: %q", i+1, ln)
		}
		if strings.Contains(ln, "🧢") {
			t.Errorf("line %d shouldn't have a duplicate avatar: %q", i+1, ln)
		}
	}
}

func TestWordWrap_RespectsExplicitNewlines(t *testing.T) {
	out := wordWrap("paragraph one\nparagraph two", 40)
	if len(out) != 2 {
		t.Fatalf("explicit \\n should yield 2 lines; got %d (%v)", len(out), out)
	}
	if out[0] != "paragraph one" || out[1] != "paragraph two" {
		t.Errorf("lines = %v", out)
	}
}

func TestWordWrap_BreaksOnSpaces(t *testing.T) {
	out := wordWrap("alpha beta gamma delta", 11)
	// 11-cell width fits "alpha beta" (10) but not "alpha beta gamma".
	if len(out) < 2 {
		t.Fatalf("expected ≥2 wrapped lines; got %d (%v)", len(out), out)
	}
	if !strings.HasPrefix(out[0], "alpha") {
		t.Errorf("first line should start with alpha: %q", out[0])
	}
}

// stripStyle drops lipgloss SGR escape sequences so tests can match
// glyph + indent shape without worrying about colors.
func stripStyle(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			in = true
		case in && r == 'm':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TestComposeTranscript_StableOutput is a lower-level snapshot: the
// rendering helper is independent of bubbles, so we can pin it without
// stubbing the viewport.
func TestComposeTranscript_StableOutput(t *testing.T) {
	entries := []transcriptEntry{
		{kind: entryUserMessage, text: "alpha"},
		// New tool-card shape: result folds INTO the call entry so
		// there's one bordered card per round-trip, not two rows.
		{
			kind:       entryToolCall,
			tool:       "bash",
			toolInput:  `{"cmd":"ls"}`,
			toolResult: "total 12\ndrwxr-xr-x  3 user",
			hasResult:  true,
		},
	}
	got := composeTranscript(entries, "live agent text", "", nil, 80)

	// "bash" + the input preview + the line-count status all live in
	// the bordered card; "alpha" is the user line; "live agent text"
	// is the streaming-assistant block.
	for _, want := range []string{"alpha", "bash", `"cmd":"ls"`, "2 lines", "live agent text"} {
		if !strings.Contains(got, want) {
			t.Errorf("composeTranscript missing %q:\n%s", want, got)
		}
	}
}

// TestToolCardStatus_DerivedFromResult pins the status-suffix logic
// across the four observable states (running, error, no-output,
// multi-line). The status text is what gives the user the "did
// anything come back?" cue without expanding the card.
func TestToolCardStatus_DerivedFromResult(t *testing.T) {
	cases := []struct {
		name string
		e    transcriptEntry
		want string
	}{
		{"running", transcriptEntry{}, "running…"},
		{"error", transcriptEntry{hasResult: true, isError: true, toolResult: "tool error: x"}, "error"},
		{"empty", transcriptEntry{hasResult: true}, "no output"},
		{"single line", transcriptEntry{hasResult: true, toolResult: "ok"}, "1 line"},
		{"multi line", transcriptEntry{hasResult: true, toolResult: "a\nb\nc\n"}, "3 lines"},
	}
	for _, c := range cases {
		if got := toolCardStatus(c.e); got != c.want {
			t.Errorf("%s: status = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestFindLatestToolCall_FoldsResultIntoCall proves the
// applyEvent→findLatestToolCall→merge pipeline collapses a
// tool_call + tool_result pair into one entry rather than two rows.
func TestFindLatestToolCall_FoldsResultIntoCall(t *testing.T) {
	m := &Model{}
	// Two preceding calls: an unrelated one, then a "bash" that's
	// already had its result folded in. The next bash result should
	// find NO matching pending call → fall through to defensive
	// standalone card.
	m.transcript = []transcriptEntry{
		{kind: entryToolCall, tool: "read", hasResult: false},
		{kind: entryToolCall, tool: "bash", hasResult: true},
	}
	if got := m.findLatestToolCall("bash"); got != -1 {
		t.Errorf("findLatestToolCall(bash) = %d, want -1 (the only bash entry already has a result)", got)
	}
	if got := m.findLatestToolCall("read"); got != 0 {
		t.Errorf("findLatestToolCall(read) = %d, want 0", got)
	}
}

// TestApplyEvent_StateChangeBadgeFollowsProjection seeds a created
// event, asserts the badge says [spawning], then appends a transition
// to running, asserts the badge updates.
func TestApplyEvent_StateChangeBadgeFollowsProjection(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000003"
	seedAgent(t, log, agentID, "stateful", "fake")

	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	// Slice 9c: badge is `[<glyph> <label>]`, glyph from theme.StateGlyph.
	if got := m.View(); !strings.Contains(got, "spawning") || !strings.Contains(got, "◐") {
		t.Fatalf("initial badge missing spawning glyph/label: %s", got)
	}

	// Append a transition: spawning → running. Replay through Update.
	to := agent.StateRunning
	payload, err := agent.NewStateChangeTransition(to)
	if err != nil {
		t.Fatalf("transition payload: %v", err)
	}
	ev := agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtStateChange,
		Payload: payload,
	}
	updated, _ := m.Update(eventMsg{ev: ev})
	m = updated.(*Model)
	if got := m.View(); !strings.Contains(got, "running") || !strings.Contains(got, "●") {
		t.Fatalf("badge did not update to running glyph/label: %s", got)
	}
}

// TestTextSource_LiveTextAppearsInTranscript verifies the TextSource
// seam: text appended after model construction is visible in the next
// rerender via the textTickMsg path.
func TestTextSource_LiveTextAppearsInTranscript(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000004"
	seedAgent(t, log, agentID, "live", "fake")

	src := NewMemTextSource()
	m := New(log, agentID, src)
	m = drive(t, m, 120, 30)

	src.Append(agentID, "streaming token...")
	// Tick fires the re-render path.
	updated, _ := m.Update(textTickMsg{})
	m = updated.(*Model)

	if got := m.View(); !strings.Contains(got, "streaming token...") {
		t.Fatalf("live text not surfaced: %s", got)
	}
}

func mustContain(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("expected to find %q in:\n%s", sub, s)
	}
}
