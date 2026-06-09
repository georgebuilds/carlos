package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

func TestHumanizeSessionAge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		input   time.Time
		wantSub string // substring the returned label must contain
	}{
		{"zero", time.Time{}, "—"},
		{"just-now", now.Add(-10 * time.Second), "just now"},
		{"minutes", now.Add(-12 * time.Minute), "12m ago"},
		{"hours", now.Add(-5 * time.Hour), "5h ago"},
		{"days", now.Add(-3 * 24 * time.Hour), "3d ago"},
		{"weeks-plus-returns-date", now.Add(-30 * 24 * time.Hour), "-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := humanizeSessionAge(tc.input)
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("got %q want substring %q", got, tc.wantSub)
			}
		})
	}
}

// TestRenderResumeCard_BothBranches paints the selected and idle
// branches and asserts the card body contains the model name and
// preview text. Style escapes are stripped through lipgloss already
// in the rendered string; we only check substrings.
func TestRenderResumeCard_BothBranches(t *testing.T) {
	s := resumeSession{
		ID:        "01HVDEVDEVDEVDEVDEVDEVDEV1",
		Model:     "openrouter/google/gemini-3.5-flash",
		State:     agent.StateRunning,
		UpdatedAt: time.Now().Add(-3 * time.Minute),
		Preview:   "what's left on the migration",
		UserMsgs:  7,
	}
	idle := renderResumeCard(s, 80, false)
	sel := renderResumeCard(s, 80, true)
	if idle == "" || sel == "" {
		t.Fatal("card render produced empty output")
	}
	if !strings.Contains(idle, "gemini-3.5-flash") {
		t.Error("idle card should display the trimmed model name")
	}
	if !strings.Contains(idle, "what's left") {
		t.Error("idle card should include preview text")
	}
	if !strings.Contains(sel, "gemini-3.5-flash") {
		t.Error("selected card also shows model")
	}
}

// TestRenderResumeCard_EmptyPreview falls back to the placeholder.
func TestRenderResumeCard_EmptyPreview(t *testing.T) {
	s := resumeSession{
		ID:        "01HVDEVDEVDEVDEVDEVDEVDEV2",
		Model:     "claude-opus-4-7",
		State:     agent.StateRunning,
		UpdatedAt: time.Now(),
	}
	out := renderResumeCard(s, 80, false)
	if !strings.Contains(out, "(no messages yet)") {
		t.Errorf("empty preview should render placeholder; got:\n%s", out)
	}
}

// TestResumeRequested_DefaultEmpty pins the contract: a chat that
// closed without a picker commit returns "" so the runtime loop
// doesn't try to swap sessions.
func TestResumeRequested_DefaultEmpty(t *testing.T) {
	m := &Model{}
	if got := m.ResumeRequested(); got != "" {
		t.Errorf("default should be empty; got %q", got)
	}
}

// TestCloseResumePicker_ClearsState — Esc out should reset the
// overlay flag + list + cursor without nuking the selected id (which
// also stays empty since Enter never fired).
func TestCloseResumePicker_ClearsState(t *testing.T) {
	m := &Model{
		showResume:     true,
		resumeSessions: []resumeSession{{ID: "x"}},
		resumeCursor:   0,
	}
	m.closeResumePicker()
	if m.showResume {
		t.Error("showResume should be false after close")
	}
	if m.resumeSessions != nil {
		t.Errorf("resumeSessions should be nil; got %v", m.resumeSessions)
	}
	if m.resumeCursor != 0 {
		t.Errorf("resumeCursor should be 0; got %d", m.resumeCursor)
	}
	if m.resumeSelected != "" {
		t.Errorf("close shouldn't touch resumeSelected; got %q", m.resumeSelected)
	}
}

// TestOpenResumePicker_NotSQLiteLogReturnsStatus exercises the
// defensive branch: the dev-aid chat passes a MemEventLog, not a
// SQLiteEventLog, so /resume should echo a clean warning instead of
// type-assertion panicking.
func TestOpenResumePicker_NotSQLiteLogReturnsStatus(t *testing.T) {
	m := &Model{log: &fakeNonSQLiteLog{}}
	cmd := m.openResumePicker()
	if cmd == nil {
		t.Fatal("expected a status cmd")
	}
	msg, ok := cmd().(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", cmd())
	}
	if !strings.Contains(msg.text, "only available") {
		t.Errorf("expected 'only available' echo; got %q", msg.text)
	}
}

// insertProjectionRow is a test-only shortcut: seedAgent writes the
// event but ListUserSessions reads the `agents` projection table,
// which is populated by InsertAgent (the production supervisor calls
// this after the created event). We mirror that here so the picker
// finds the seeded session.
func insertProjectionRow(t *testing.T, log *agent.SQLiteEventLog, id, title, model string) {
	t.Helper()
	now := time.Now().UTC()
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID:              id,
		RootID:          id,
		State:           agent.StateRunning,
		Attempt:         1,
		Title:           title,
		Model:           model,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("insert projection row: %v", err)
	}
}

// TestOpenResumePicker_EmptyLogStatusEcho exercises the "no other
// sessions" branch: a freshly-opened log with only the current
// agent (which the picker excludes) yields a friendly status echo.
func TestOpenResumePicker_EmptyLogStatusEcho(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HVCURRENTRESUMETESTSESSION"
	seedAgent(t, log, agentID, "current chat", "anthropic:claude-opus-4-7")
	insertProjectionRow(t, log, agentID, "current chat", "anthropic:claude-opus-4-7")
	m := &Model{log: log, agentID: agentID}
	cmd := m.openResumePicker()
	if cmd == nil {
		t.Fatal("expected status cmd")
	}
	msg := cmd().(statusMsg)
	if !strings.Contains(msg.text, "no other sessions") {
		t.Errorf("expected 'no other sessions' hint; got %q", msg.text)
	}
}

// TestOpenResumePicker_PopulatesSessions seeds two top-level
// sessions, opens the picker, and confirms one (other-than-current)
// shows up in resumeSessions with the right preview text.
func TestOpenResumePicker_PopulatesSessions(t *testing.T) {
	log := openTempLog(t)
	const current = "01HVCURRENTRESUMETESTSESSION"
	const other = "01HVOTHERRESUMETESTSESSIONXX"
	seedAgent(t, log, current, "current", "anthropic:claude-opus-4-7")
	insertProjectionRow(t, log, current, "current", "anthropic:claude-opus-4-7")
	seedAgent(t, log, other, "older", "openrouter:google/gemini-3.5-flash")
	insertProjectionRow(t, log, other, "older", "openrouter:google/gemini-3.5-flash")
	seedUserMessage(t, log, other, "how do I migrate the schema")

	m := &Model{log: log, agentID: current}
	if cmd := m.openResumePicker(); cmd != nil {
		// The non-empty branch returns nil cmd; only the error +
		// empty paths return a status echo.
		t.Errorf("expected nil cmd when sessions present; got %v", cmd())
	}
	if !m.showResume {
		t.Error("showResume should flip true")
	}
	if len(m.resumeSessions) != 1 {
		t.Fatalf("expected one other session; got %d", len(m.resumeSessions))
	}
	if m.resumeSessions[0].ID != other {
		t.Errorf("wrong session surfaced: %q", m.resumeSessions[0].ID)
	}
	if m.resumeSessions[0].Preview != "how do I migrate the schema" {
		t.Errorf("preview should reflect last user msg; got %q", m.resumeSessions[0].Preview)
	}
}

// TestHandleResumeKey_NavigatesAndCommits walks the picker key router
// through ↓, Enter to confirm the selected entry lands in
// resumeSelected + the chat sets the quit flag.
func TestHandleResumeKey_NavigatesAndCommits(t *testing.T) {
	m := &Model{
		showResume: true,
		resumeSessions: []resumeSession{
			{ID: "01HVID1RESUMEPICKERTESTAAAA"},
			{ID: "01HVID2RESUMEPICKERTESTBBBB"},
		},
		resumeCursor: 0,
	}
	tea_keys := []string{"down", "enter"}
	for _, k := range tea_keys {
		_, _, handled := m.handleResumeKey(key(k))
		if !handled {
			t.Errorf("key %q should be handled", k)
		}
	}
	if m.resumeSelected != "01HVID2RESUMEPICKERTESTBBBB" {
		t.Errorf("wanted second entry selected; got %q", m.resumeSelected)
	}
	if !m.quitting {
		t.Error("Enter should set quitting=true to bounce out of chat.Run")
	}
}

// TestHandleResumeKey_EscClosesWithoutPick exercises the Esc branch.
func TestHandleResumeKey_EscClosesWithoutPick(t *testing.T) {
	m := &Model{
		showResume:     true,
		resumeSessions: []resumeSession{{ID: "x"}},
	}
	_, _, handled := m.handleResumeKey(key("esc"))
	if !handled {
		t.Error("esc should be handled")
	}
	if m.showResume {
		t.Error("esc should close the picker")
	}
	if m.resumeSelected != "" {
		t.Error("esc must not commit a pick")
	}
}

// TestHandleResumeKey_NavigationWraps proves ↑ from index 0 wraps
// to the last, and ↓ from the end wraps to 0.
func TestHandleResumeKey_NavigationWraps(t *testing.T) {
	m := &Model{
		showResume: true,
		resumeSessions: []resumeSession{
			{ID: "a"}, {ID: "b"}, {ID: "c"},
		},
	}
	_, _, _ = m.handleResumeKey(key("up"))
	if m.resumeCursor != 2 {
		t.Errorf("up from 0 should wrap to len-1; got %d", m.resumeCursor)
	}
	_, _, _ = m.handleResumeKey(key("down"))
	if m.resumeCursor != 0 {
		t.Errorf("down past end should wrap to 0; got %d", m.resumeCursor)
	}
}

// TestHandleResumeKey_EnterNoSessionsFalls falls back to esc-like
// close when the list is empty (defensive).
func TestHandleResumeKey_EnterNoSessionsCloses(t *testing.T) {
	m := &Model{showResume: true}
	_, _, _ = m.handleResumeKey(key("enter"))
	if m.showResume {
		t.Error("enter with empty list should close the picker")
	}
	if m.resumeSelected != "" {
		t.Error("no pick should be committed")
	}
}

// TestRenderResumeOverlay_NonEmpty confirms the overlay paints
// header + footer cues; we don't assert exact text since the
// rendering uses lipgloss ANSI escapes.
func TestRenderResumeOverlay_NonEmpty(t *testing.T) {
	m := &Model{
		resumeSessions: []resumeSession{
			{ID: "01HVTEST", Model: "claude-opus-4-7"},
		},
	}
	out := renderResumeOverlay(m, 80, 30)
	if out == "" {
		t.Fatal("expected non-empty overlay render")
	}
	if !strings.Contains(out, "resume a session") {
		t.Errorf("expected header; got:\n%s", out)
	}
}

// fakeNonSQLiteLog implements agent.EventLog without being the
// SQLite type — proves the type assertion branch is exercised.
type fakeNonSQLiteLog struct{}

func (fakeNonSQLiteLog) Append(_ context.Context, _ agent.Event) (int64, error) { return 0, nil }
func (fakeNonSQLiteLog) Read(_ context.Context, _ string, _ int64) ([]agent.Event, error) {
	return nil, nil
}
func (fakeNonSQLiteLog) Subscribe(_ string) (<-chan agent.Event, func(), error) {
	return nil, func() {}, nil
}
func (fakeNonSQLiteLog) Close() error { return nil }
