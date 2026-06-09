package manage

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/theme"
)

// openTempLog spins up a fresh SQLite event log under t.TempDir()
// so every test starts with a clean database. Mirrors the helper in
// internal/tui/chat/chat_test.go.
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

// seedAgent appends a `created` state_change + InsertAgent so both
// the event log and the projection cache see the row. Returns the
// agent ID.
func seedAgent(t *testing.T, log *agent.SQLiteEventLog, id, parentID, title, model string, state agent.State) {
	t.Helper()
	rootID := parentID
	if rootID == "" {
		rootID = id
	}
	payload, err := agent.NewStateChangeCreated(agent.AgentCreated{
		ID:       id,
		ParentID: parentID,
		RootID:   rootID,
		Title:    title,
		Model:    model,
	})
	if err != nil {
		t.Fatalf("created payload: %v", err)
	}
	now := time.Now().UTC()
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: id, TS: now, Type: agent.EvtStateChange, Payload: payload,
	}); err != nil {
		t.Fatalf("append created: %v", err)
	}
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID:              id,
		ParentID:        parentID,
		RootID:          rootID,
		State:           state,
		Attempt:         1,
		Title:           title,
		Model:           model,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if state != agent.StateSpawning {
		// Reflect the desired state in the agents row, since
		// InsertAgent always writes from the AgentRow but the cache
		// honors the value passed.
		if err := log.UpdateAgentState(context.Background(), id, state, now); err != nil {
			t.Fatalf("update state: %v", err)
		}
	}
}

// driveModel runs the bubbletea Update loop manually so tests are
// synchronous. It walks Init's intended sequence: snapshot, window
// size, then the first paint.
func driveModel(t *testing.T, m *Model, w, h int) *Model {
	t.Helper()
	_ = m.Init()

	// Pull a snapshot synchronously instead of waiting on the tick.
	rows, err := m.src.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	updated, _ := m.Update(snapshotReadyMsg{rows: rows})
	m = updated.(*Model)

	updated, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = updated.(*Model)

	_ = m.View()
	return m
}

// TestRoster_RendersAllStateBadges seeds one agent per state and
// asserts the rendered view contains every state's bracketed label.
// The "color is never the sole signal" SPEC contract is enforced by
// the text label being present; the color is style cake on top.
func TestRoster_RendersAllStateBadges(t *testing.T) {
	log := openTempLog(t)

	states := []agent.State{
		agent.StateSpawning, agent.StateQueued, agent.StateRunning,
		agent.StateAwaitingInput, agent.StateBlocked, agent.StatePausedByUser,
		agent.StateCompacting, agent.StateCancelling, agent.StateDone,
		agent.StateFailed, agent.StateOrphaned,
	}
	for i, s := range states {
		id := fmt.Sprintf("01HV00000000000000000000%02d", i)
		seedAgent(t, log, id, "", fmt.Sprintf("agent %d", i), "fake", s)
	}

	src := NewSQLiteSnapshotSource(log)
	m := New(src, log, nil)
	// v0.7.4 card layout consumes ~5 rows per agent — bump the test
	// height so all 11 cards fit in one viewport without the
	// virtualization window hiding the tail of the list.
	m = driveModel(t, m, 160, 80)

	view := m.View()
	for _, s := range states {
		// Slice 9c: badge format is `[<glyph> <label>]`. Color is
		// never the sole signal — assert both the unicode shape AND
		// the human-readable label survive into the rendered output.
		if !strings.Contains(view, s.String()) {
			t.Errorf("label %q missing from view", s.String())
		}
		if g := theme.StateGlyph(s); !strings.Contains(view, g) {
			t.Errorf("glyph %q for state %s missing from view", g, s)
		}
	}
}

// TestSort_AwaitingInputFirst asserts the default priority sort
// surfaces the awaiting-input agent ahead of everything else.
func TestSort_AwaitingInputFirst(t *testing.T) {
	log := openTempLog(t)
	seedAgent(t, log, "01HV0000000000000000000001", "", "regular", "fake", agent.StateRunning)
	seedAgent(t, log, "01HV0000000000000000000002", "", "needs human", "fake", agent.StateAwaitingInput)
	seedAgent(t, log, "01HV0000000000000000000003", "", "other", "fake", agent.StateRunning)

	src := NewSQLiteSnapshotSource(log)
	m := New(src, log, nil)
	m = driveModel(t, m, 160, 60)

	if len(m.rosterRows) < 3 {
		t.Fatalf("expected 3 rows, got %d", len(m.rosterRows))
	}
	first := m.rosterRows[0].row
	if first.State != agent.StateAwaitingInput {
		t.Errorf("priority sort: first row state = %s, want awaiting-input", first.State)
	}
	if first.Title != "needs human" {
		t.Errorf("priority sort: first row title = %q, want %q", first.Title, "needs human")
	}
}

// TestFilter_NarrowsRosterLive types into the filter overlay and
// asserts only matching rows survive in rosterRows.
func TestFilter_NarrowsRosterLive(t *testing.T) {
	log := openTempLog(t)
	seedAgent(t, log, "01HV0000000000000000000001", "", "compile spec", "fake", agent.StateRunning)
	seedAgent(t, log, "01HV0000000000000000000002", "", "review pr", "fake", agent.StateRunning)
	seedAgent(t, log, "01HV0000000000000000000003", "", "compile docs", "fake", agent.StateRunning)

	src := NewSQLiteSnapshotSource(log)
	m := New(src, log, nil)
	m = driveModel(t, m, 160, 60)

	// Open filter overlay with `/`.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(*Model)
	if m.overlay != overlayFilter {
		t.Fatalf("filter overlay didn't open: %d", m.overlay)
	}

	// Type "compile" via the input model + force the live-rebuild.
	m.input.SetValue("compile")
	m.filter.Query = "compile"
	m.rebuildRoster()

	if got := len(m.rosterRows); got != 2 {
		t.Fatalf("filter 'compile' → expected 2 rows, got %d", got)
	}
	for _, rr := range m.rosterRows {
		if !strings.Contains(rr.row.Title, "compile") {
			t.Errorf("filter leaked %q", rr.row.Title)
		}
	}

	// Clear filter via ESC outside overlay (close overlay first).
	m.closeOverlay()
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*Model)
	if got := len(m.rosterRows); got != 3 {
		t.Fatalf("ESC didn't clear filter: %d rows", got)
	}
}

// TestLineage_IndentsThreeDeep seeds a 3-level parent/child chain
// and asserts the rendered roster indents grandchildren 4 spaces.
func TestLineage_IndentsThreeDeep(t *testing.T) {
	log := openTempLog(t)
	root := "01HVROOT000000000000000001"
	child := "01HVCHLD000000000000000002"
	grand := "01HVGRND000000000000000003"
	seedAgent(t, log, root, "", "root task", "fake", agent.StateRunning)
	seedAgent(t, log, child, root, "child task", "fake", agent.StateRunning)
	seedAgent(t, log, grand, child, "grand task", "fake", agent.StateRunning)

	src := NewSQLiteSnapshotSource(log)
	m := New(src, log, nil)
	m = driveModel(t, m, 160, 60)

	if len(m.rosterRows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(m.rosterRows))
	}
	// Walk + verify depth assignment.
	depthByID := map[string]int{}
	for _, rr := range m.rosterRows {
		depthByID[rr.row.ID] = rr.indent
	}
	if depthByID[root] != 0 {
		t.Errorf("root indent = %d, want 0", depthByID[root])
	}
	if depthByID[child] != 1 {
		t.Errorf("child indent = %d, want 1", depthByID[child])
	}
	if depthByID[grand] != 2 {
		t.Errorf("grand indent = %d, want 2", depthByID[grand])
	}
}

// recordingDispatcher is the test seam for verbs: every call lands
// in a slice the test can inspect.
type recordingDispatcher struct {
	steers     []struct{ ID, Msg string }
	interrupts []string
	stops      []string
	err        error
}

func (r *recordingDispatcher) Steer(id, msg string) error {
	r.steers = append(r.steers, struct{ ID, Msg string }{id, msg})
	return r.err
}
func (r *recordingDispatcher) Interrupt(id string) error {
	r.interrupts = append(r.interrupts, id)
	return r.err
}
func (r *recordingDispatcher) Stop(id string) error {
	r.stops = append(r.stops, id)
	return r.err
}

// modeReportingDispatcher embeds the recording dispatcher and also
// satisfies the ModeReporter optional interface so the manage header
// renders the "mode=X (cap N)" chip. Used by
// TestHeader_ShowsModeChipWhenReporterWired.
type modeReportingDispatcher struct {
	recordingDispatcher
	mode string
	cap  int
}

func (r *modeReportingDispatcher) Mode() string  { return r.mode }
func (r *modeReportingDispatcher) SpawnCap() int { return r.cap }

// TestHeader_ShowsModeChipWhenReporterWired asserts the manage header
// gains a "mode=... (cap N)" chip when the wired VerbDispatcher also
// implements ModeReporter. Production wires *agent.Supervisor which
// implements both; the recording double in the prior test does not, so
// that test path keeps reading the chip-less header.
func TestHeader_ShowsModeChipWhenReporterWired(t *testing.T) {
	log := openTempLog(t)
	seedAgent(t, log, "01HV0000000000000000000001", "", "demo", "fake", agent.StateRunning)

	rep := &modeReportingDispatcher{mode: "orchestrator", cap: 5}
	src := NewSQLiteSnapshotSource(log)
	m := New(src, log, rep)
	m = driveModel(t, m, 160, 60)

	view := m.View()
	if !strings.Contains(view, "mode=orchestrator") {
		t.Errorf("header missing 'mode=orchestrator' chip; view:\n%s", view)
	}
	if !strings.Contains(view, "cap 5") {
		t.Errorf("header missing 'cap 5' chip; view:\n%s", view)
	}
}

// TestHeader_SkipsModeChipWithoutReporter confirms the header gracefully
// omits the chip when the wired dispatcher is a plain VerbDispatcher
// (recording double, noop fallback, etc.).
func TestHeader_SkipsModeChipWithoutReporter(t *testing.T) {
	log := openTempLog(t)
	seedAgent(t, log, "01HV0000000000000000000001", "", "demo", "fake", agent.StateRunning)

	src := NewSQLiteSnapshotSource(log)
	m := New(src, log, &recordingDispatcher{})
	m = driveModel(t, m, 160, 60)

	if view := m.View(); strings.Contains(view, "mode=") {
		t.Errorf("plain dispatcher should not surface mode chip; view:\n%s", view)
	}
}

// TestVerbs_TriggerSupervisorCalls walks the s/i/x keys, commits each
// overlay, and asserts the recording dispatcher saw the expected
// calls.
func TestVerbs_TriggerSupervisorCalls(t *testing.T) {
	log := openTempLog(t)
	const id = "01HV0000000000000000000001"
	seedAgent(t, log, id, "", "test agent", "fake", agent.StateRunning)

	rec := &recordingDispatcher{}
	src := NewSQLiteSnapshotSource(log)
	m := New(src, log, rec)
	m = driveModel(t, m, 160, 60)

	// --- Steer
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(*Model)
	if m.overlay != overlaySteer {
		t.Fatalf("s didn't open steer overlay: %d", m.overlay)
	}
	m.input.SetValue("focus on the tests")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatalf("steer commit returned nil cmd")
	}
	// Drain the cmd to deliver the Steer call.
	msg := cmd()
	if _, ok := msg.(VerbResult); !ok {
		t.Errorf("steer cmd produced %T, want VerbResult", msg)
	}
	if len(rec.steers) != 1 {
		t.Fatalf("Steer not called: %d records", len(rec.steers))
	}
	if rec.steers[0].ID != id {
		t.Errorf("Steer id = %q, want %q", rec.steers[0].ID, id)
	}
	if rec.steers[0].Msg != "focus on the tests" {
		t.Errorf("Steer msg = %q", rec.steers[0].Msg)
	}

	// --- Interrupt (open + y)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	m = updated.(*Model)
	if m.overlay != overlayInterruptConfirm {
		t.Fatalf("i didn't open interrupt overlay: %d", m.overlay)
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatalf("interrupt y returned nil cmd")
	}
	_ = cmd()
	if len(rec.interrupts) != 1 || rec.interrupts[0] != id {
		t.Errorf("Interrupt not called for %q: %v", id, rec.interrupts)
	}

	// --- Stop (open + y)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(*Model)
	if m.overlay != overlayStopConfirm {
		t.Fatalf("x didn't open stop overlay: %d", m.overlay)
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatalf("stop y returned nil cmd")
	}
	_ = cmd()
	if len(rec.stops) != 1 || rec.stops[0] != id {
		t.Errorf("Stop not called for %q: %v", id, rec.stops)
	}
}

// TestVerbs_NotImplementedSurfacesInStatus pipes a not-implemented
// error through dispatchSteer and asserts the resulting VerbResult
// renders gracefully (no crash, status text mentions the error).
func TestVerbs_NotImplementedSurfacesInStatus(t *testing.T) {
	log := openTempLog(t)
	const id = "01HV0000000000000000000001"
	seedAgent(t, log, id, "", "test agent", "fake", agent.StateRunning)

	rec := &recordingDispatcher{err: fmt.Errorf("supervisor.Steer: not implemented")}
	src := NewSQLiteSnapshotSource(log)
	m := New(src, log, rec)
	m = driveModel(t, m, 160, 60)

	cmd := dispatchSteer(m.sup, id, "anything")
	res := cmd().(VerbResult)
	if res.Err == nil {
		t.Fatalf("expected err to flow through")
	}
	line := res.String()
	if !strings.Contains(line, "not implemented") {
		t.Errorf("status line %q missing 'not implemented'", line)
	}
}

// TestFocus_SwitchesAgentsOnEnter walks the focus-change path: select
// row 2 with j, press enter, assert the focus pane binds to that ID.
func TestFocus_SwitchesAgentsOnEnter(t *testing.T) {
	log := openTempLog(t)
	const id1 = "01HV0000000000000000000001"
	const id2 = "01HV0000000000000000000002"
	seedAgent(t, log, id1, "", "first", "fake", agent.StateRunning)
	seedAgent(t, log, id2, "", "second", "fake", agent.StateRunning)

	src := NewSQLiteSnapshotSource(log)
	m := New(src, log, nil)
	m = driveModel(t, m, 160, 60)

	// Move cursor down to row 1 (second agent).
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(*Model)
	if m.cursor != 1 {
		t.Fatalf("cursor after j = %d, want 1", m.cursor)
	}

	// Enter focuses the selected agent.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)

	got := m.focus.AgentID()
	want := m.rosterRows[1].row.ID
	if got != want {
		t.Fatalf("focus.AgentID = %q, want %q", got, want)
	}
}

// TestSparkline_UpdatesAsTokenUsageEvents asserts the ring buffer
// accumulates token deltas and that RenderSparkline produces visibly
// varying output when activity spikes.
func TestSparkline_UpdatesAsTokenUsageEvents(t *testing.T) {
	r := &TokenRing{}
	// Build a clear ramp: oldest cells empty, newest cells loud.
	for i := 0; i < numSparkBuckets; i++ {
		// Push numRingPerCell deltas into the current slot then
		// advance so the next pile lands in a fresh slot.
		for j := 0; j < numRingPerCell; j++ {
			r.Add(int64(i * 10))
			r.Advance()
		}
	}

	out := RenderSparkline(r, agent.StateRunning)
	if out == "" {
		t.Fatal("sparkline empty")
	}
	// Visible width matches numSparkBuckets (lipgloss strips ANSI).
	// We don't import lipgloss here; instead check there's at least
	// one of the highest-elevation glyphs in the output.
	if !strings.ContainsRune(out, sparkBlocks[len(sparkBlocks)-1]) {
		t.Errorf("ramp should peak at the full block; got %q", out)
	}
}

// TestSparkline_FocusedAgentTokenUsageEvent runs the live path: a
// token_usage event delivered to the focus pane bumps the ring.
func TestSparkline_FocusedAgentTokenUsageEvent(t *testing.T) {
	log := openTempLog(t)
	const id = "01HV0000000000000000000001"
	seedAgent(t, log, id, "", "tokens", "fake", agent.StateRunning)

	src := NewSQLiteSnapshotSource(log)
	m := New(src, log, nil)
	m = driveModel(t, m, 160, 60)

	// Move cursor to the only row and focus.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)

	// Manually deliver a token_usage event into the focus pane.
	payload, _ := json.Marshal(agent.TokenUsage{DeltaOut: 1234})
	updated, _ = m.Update(focusEventMsg{ev: agent.Event{
		AgentID: id,
		Type:    agent.EvtTokenUsage,
		TS:      time.Now().UTC(),
		Payload: payload,
	}})
	m = updated.(*Model)

	ring := m.focus.Ring()
	snap := ring.Snapshot()
	var total int64
	for _, v := range snap {
		total += v
	}
	if total != 1234 {
		t.Errorf("ring total = %d, want 1234", total)
	}
}

// TestPerf_100Agents_RosterUnder50ms asserts a coarse-grained
// rendering budget for the busy case. Not a benchmark — just a
// guardrail that virtualization keeps the View hot path fast.
func TestPerf_100Agents_RosterUnder50ms(t *testing.T) {
	rows := make([]agent.AgentRow, 100)
	now := time.Now().UTC()
	for i := range rows {
		rows[i] = agent.AgentRow{
			ID:              fmt.Sprintf("01HV%022d", i),
			RootID:          fmt.Sprintf("01HV%022d", i),
			State:           agent.StateRunning,
			Attempt:         1,
			Title:           fmt.Sprintf("agent %d busy with a long-ish task", i),
			Model:           "fake-model-v1",
			TokensIn:        int64(i * 100),
			TokensOut:       int64(i * 50),
			CostCents:       int64(i),
			CreatedAt:       now.Add(-time.Duration(i) * time.Second),
			UpdatedAt:       now,
			LastHeartbeatAt: now,
		}
	}
	src := &StaticSnapshotSource{Rows: rows}
	m := New(src, nil, nil)
	updated, _ := m.Update(snapshotReadyMsg{rows: rows})
	m = updated.(*Model)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	m = updated.(*Model)

	start := time.Now()
	for i := 0; i < 5; i++ {
		_ = m.View()
	}
	avg := time.Since(start) / 5
	if avg > 50*time.Millisecond {
		t.Errorf("View() avg = %v over 5 frames, want <50ms", avg)
	}
}

// TestTooSmallTerminal asserts the manage view refuses to render
// below the minimum cell budget. Same posture as chat + onboarding.
func TestTooSmallTerminal(t *testing.T) {
	src := &StaticSnapshotSource{}
	m := New(src, nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m = updated.(*Model)
	view := m.View()
	if !strings.Contains(view, "at least") {
		t.Errorf("too-small message missing:\n%s", view)
	}
}

// TestVerbResult_StringIncludesAgentIDAndOutcome exercises the
// VerbResult formatting matrix.
func TestVerbResult_StringIncludesAgentIDAndOutcome(t *testing.T) {
	cases := []struct {
		r    VerbResult
		want string
	}{
		{VerbResult{Verb: "steer", AgentID: "01HVabc12345678"}, "steered 01HVabc1"},
		{VerbResult{Verb: "interrupt", AgentID: "01HVabc12345678"}, "interrupting 01HVabc1"},
		{VerbResult{Verb: "stop", AgentID: "01HVabc12345678"}, "stopping 01HVabc1"},
		{VerbResult{Verb: "steer", AgentID: "01HVabc12345678", Err: fmt.Errorf("not implemented")}, "not implemented"},
	}
	for _, c := range cases {
		got := c.r.String()
		if !strings.Contains(got, c.want) {
			t.Errorf("VerbResult.String() = %q, want substring %q", got, c.want)
		}
	}
}

// TestWindow_ScrollToKeepsCursorInView checks the virtualization
// math: scrolling to an out-of-window index slides the top edge so
// the index lands at the bottom; scrolling above slides up.
func TestWindow_ScrollToKeepsCursorInView(t *testing.T) {
	w := Window{Total: 100, Visible: 10, Top: 0}
	// Scroll to 50 → top should slide to 41 (50 visible at bottom).
	w = w.ScrollTo(50)
	if w.Top != 41 {
		t.Errorf("ScrollTo(50) Top = %d, want 41", w.Top)
	}
	if !w.Contains(50) {
		t.Errorf("ScrollTo(50) did not put 50 in view: %+v", w)
	}
	// Scroll to 5 (above) → top slides up to 5.
	w = w.ScrollTo(5)
	if w.Top != 5 {
		t.Errorf("ScrollTo(5) Top = %d, want 5", w.Top)
	}
}

// TestFilter_MatchesIntentStateAndID covers each match axis once.
func TestFilter_MatchesIntentStateAndID(t *testing.T) {
	rows := []agent.AgentRow{
		{ID: "abc", Title: "compile the spec", State: agent.StateRunning},
		{ID: "def", Title: "run tests", State: agent.StateAwaitingInput},
		{ID: "ghi", Title: "deploy", State: agent.StateDone},
	}
	f := Filter{Query: "spec"}
	if got := len(f.Apply(rows)); got != 1 {
		t.Errorf("intent match → %d rows, want 1", got)
	}
	f = Filter{Query: "awaiting"}
	if got := len(f.Apply(rows)); got != 1 {
		t.Errorf("state match → %d rows, want 1", got)
	}
	f = Filter{Query: "GHI"}
	if got := len(f.Apply(rows)); got != 1 {
		t.Errorf("id match (case-insensitive) → %d rows, want 1", got)
	}
	f = Filter{}
	if got := len(f.Apply(rows)); got != 3 {
		t.Errorf("empty filter → %d rows, want 3", got)
	}
}

// TestSort_KeysShufflesRoster exercises 1/2/3 sort overrides via
// keystrokes and asserts the rosterRows order changes.
func TestSort_KeysShufflesRoster(t *testing.T) {
	rows := []agent.AgentRow{
		{ID: "ccc", Title: "c", State: agent.StateRunning, CostCents: 100, TokensOut: 10},
		{ID: "aaa", Title: "a", State: agent.StateRunning, CostCents: 300, TokensOut: 30},
		{ID: "bbb", Title: "b", State: agent.StateRunning, CostCents: 200, TokensOut: 20},
	}
	src := &StaticSnapshotSource{Rows: rows}
	m := New(src, nil, nil)
	updated, _ := m.Update(snapshotReadyMsg{rows: rows})
	m = updated.(*Model)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	m = updated.(*Model)

	// `1` sorts by id ascending.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m = updated.(*Model)
	if m.rosterRows[0].row.ID != "aaa" {
		t.Errorf("sort=id first row = %q, want aaa", m.rosterRows[0].row.ID)
	}

	// `3` sorts by cost (descending by default since big-cost matters).
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	m = updated.(*Model)
	if m.rosterRows[0].row.ID != "aaa" {
		t.Errorf("sort=cost first row = %q (cost 300), want aaa", m.rosterRows[0].row.ID)
	}
}

// TestVerbResult_ConfirmDefaultIsNo asserts that pressing enter
// (without typing y) in an interrupt-confirm overlay does NOT fire
// the verb. Stray-Return safety per SPEC.
func TestVerbResult_ConfirmDefaultIsNo(t *testing.T) {
	log := openTempLog(t)
	const id = "01HV0000000000000000000001"
	seedAgent(t, log, id, "", "test", "fake", agent.StateRunning)

	rec := &recordingDispatcher{}
	src := NewSQLiteSnapshotSource(log)
	m := New(src, log, rec)
	m = driveModel(t, m, 160, 60)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(*Model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)
	if cmd != nil {
		_ = cmd()
	}
	if len(rec.stops) != 0 {
		t.Errorf("enter (no y) fired Stop: %v", rec.stops)
	}
}

// TestFocusPane_ApplyEvent_TranscriptEntries seeds events directly
// into a FocusPane and asserts the rendered transcript surfaces the
// expected markers.
func TestFocusPane_ApplyEvent_TranscriptEntries(t *testing.T) {
	f := NewFocusPane()
	f.Bind("01HVabc12345678")
	f.Resize(80, 24)

	now := time.Now().UTC()

	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "01HVabc12345678", RootID: "01HVabc12345678", Title: "test", Model: "fake",
	})
	f.ApplyEvent(agent.Event{Type: agent.EvtStateChange, TS: now, Payload: created})

	toolPayload, _ := json.Marshal(agent.ToolCall{Name: "bash"})
	f.ApplyEvent(agent.Event{Type: agent.EvtToolCall, TS: now, Payload: toolPayload})

	usrPayload, _ := json.Marshal(map[string]string{"text": "hi from test"})
	f.ApplyEvent(agent.Event{Type: agent.EvtUserMessage, TS: now, Payload: usrPayload})

	view := f.View()
	for _, want := range []string{"spawned", "bash", "hi from test", "👤"} {
		if !strings.Contains(view, want) {
			t.Errorf("focus view missing %q:\n%s", want, view)
		}
	}
}

// TestSparkline_RingAdvanceZeroesNewSlot is a tiny invariant: after
// 60 Advance() calls the buffer has rolled all the way over.
func TestSparkline_RingAdvanceZeroesNewSlot(t *testing.T) {
	r := &TokenRing{}
	r.Add(100)
	for i := 0; i < ringSize; i++ {
		r.Advance()
	}
	r.Add(50)
	snap := r.Snapshot()
	var sum int64
	for _, v := range snap {
		sum += v
	}
	if sum != 50 {
		t.Errorf("ring sum after rollover = %d, want 50", sum)
	}
}

// TestRunawayThreshold_DerivedFromCostDistribution checks the cost
// percentile heuristic underlying the priority sort.
func TestRunawayThreshold_DerivedFromCostDistribution(t *testing.T) {
	rows := make([]agent.AgentRow, 10)
	for i := range rows {
		rows[i] = agent.AgentRow{CostCents: int64(i * 10)}
	}
	thr := runawayThreshold(rows)
	// Index-method 90th percentile on N=10 is the 9th value (0-based
	// index 8) → cost 80. Document expectation matches the impl.
	if thr != 80 {
		t.Errorf("90th percentile of 0..90 step 10 (index method) = %d, want 80", thr)
	}

	// Zero-cost universe → threshold == 0, so the runaway bucket is
	// empty and the priority sort doesn't false-flag every row.
	zero := []agent.AgentRow{{}, {}, {}}
	if got := runawayThreshold(zero); got != 0 {
		t.Errorf("zero-cost universe threshold = %d, want 0", got)
	}
}
