package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// Covers the SQLiteEventLog projection-cache helpers' error paths:
// UpdateAgentState / UpdateHeartbeat on a missing row, lifecycle.OpenStateDB
// edge cases, parseState fallthrough, and the legacy session helpers.

func openLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = agent.CloseStateDB(log) })
	return log
}

func TestSQLiteEventLog_UpdateAgentState_MissingRowErrors(t *testing.T) {
	log := openLog(t)
	err := log.UpdateAgentState(context.Background(), "ghost", agent.StateRunning, time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "no row") {
		t.Fatalf("want no-row err, got %v", err)
	}
}

func TestSQLiteEventLog_UpdateAgentModel_MissingRowErrors(t *testing.T) {
	log := openLog(t)
	err := log.UpdateAgentModel(context.Background(), "ghost", "claude-opus-4-7")
	if err == nil || !strings.Contains(err.Error(), "no row") {
		t.Fatalf("want no-row err, got %v", err)
	}
}

// TestSQLiteEventLog_UpdateAgentModel_UpdatesRow seeds a real agent
// row, swaps its model, and confirms the change lands.
func TestSQLiteEventLog_UpdateAgentModel_UpdatesRow(t *testing.T) {
	log := openLog(t)
	now := time.Now().UTC()
	row := agent.AgentRow{
		ID:              "01HVTESTTESTTESTTESTTESTTEST",
		RootID:          "01HVTESTTESTTESTTESTTESTTEST",
		State:           agent.StateRunning,
		Attempt:         1,
		Title:           "test chat",
		Model:           "anthropic:claude-opus-4-7",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}
	if err := log.InsertAgent(context.Background(), row); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := log.UpdateAgentModel(context.Background(), row.ID, "openrouter:google/gemini-3.5-flash"); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, ok, err := log.GetAgent(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("getagent: %v", err)
	}
	if !ok {
		t.Fatal("expected row to exist post-update")
	}
	if got.Model != "openrouter:google/gemini-3.5-flash" {
		t.Errorf("model did not update; got %q", got.Model)
	}
}

func TestSQLiteEventLog_UpdateHeartbeat_MissingRowErrors(t *testing.T) {
	log := openLog(t)
	err := log.UpdateHeartbeat(context.Background(), "ghost", time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "no row") {
		t.Fatalf("want no-row err, got %v", err)
	}
}

func TestSQLiteEventLog_GetAgent_Missing(t *testing.T) {
	log := openLog(t)
	_, ok, err := log.GetAgent(context.Background(), "ghost")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok {
		t.Fatalf("ok=true for missing agent")
	}
}

func TestSQLiteEventLog_InsertAgent_PreservesParentID(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	// Insert root first to satisfy the FK constraint on parent_id.
	root := agent.AgentRow{
		ID:              "root",
		RootID:          "root",
		State:           agent.StateRunning,
		Attempt:         1,
		Title:           "r",
		Model:           "m",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}
	if err := log.InsertAgent(ctx, root); err != nil {
		t.Fatalf("insert root: %v", err)
	}

	child := agent.AgentRow{
		ID:              "child",
		ParentID:        "root",
		RootID:          "root",
		State:           agent.StateRunning,
		Attempt:         1,
		Title:           "c",
		Model:           "m",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}
	if err := log.InsertAgent(ctx, child); err != nil {
		t.Fatalf("insert child: %v", err)
	}

	r, ok, err := log.GetAgent(ctx, "child")
	if err != nil || !ok {
		t.Fatalf("get child: ok=%v err=%v", ok, err)
	}
	if r.ParentID != "root" {
		t.Errorf("ParentID = %q, want root", r.ParentID)
	}
	if r.RootID != "root" {
		t.Errorf("RootID = %q, want root", r.RootID)
	}
}

func TestSQLiteEventLog_StaleAgents_FiltersTerminal(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	staleTS := now.Add(-1 * time.Hour)

	// Seed two terminal + one non-terminal agents, all "stale".
	for _, tc := range []struct {
		id    string
		state agent.State
	}{
		{"a", agent.StateRunning},
		{"b", agent.StateDone},
		{"c", agent.StateFailed},
	} {
		r := agent.AgentRow{
			ID: tc.id, RootID: tc.id, State: tc.state, Attempt: 1,
			Title: tc.id, CreatedAt: staleTS, UpdatedAt: staleTS, LastHeartbeatAt: staleTS,
		}
		if err := log.InsertAgent(ctx, r); err != nil {
			t.Fatalf("insert %s: %v", tc.id, err)
		}
	}
	ids, err := log.StaleAgents(ctx, now)
	if err != nil {
		t.Fatalf("stale: %v", err)
	}
	if len(ids) != 1 || ids[0] != "a" {
		t.Errorf("StaleAgents = %v, want [a]", ids)
	}

	// NonTerminalAgents should also report only "a".
	active, err := log.NonTerminalAgents(ctx)
	if err != nil {
		t.Fatalf("non-terminal: %v", err)
	}
	if len(active) != 1 || active[0] != "a" {
		t.Errorf("NonTerminalAgents = %v, want [a]", active)
	}
}

func TestOpenStateDB_PreservesExistingChmodFailureNonFatal(t *testing.T) {
	// MkdirAll respects an existing dir; OpenStateDB chmods to 0700
	// best-effort. Make the dir up-front with a tighter mode and assert
	// open still succeeds.
	dir := t.TempDir()
	nested := filepath.Join(dir, "sub")
	if err := mkdirAt(nested, 0o755); err != nil {
		t.Fatalf("pre-mkdir: %v", err)
	}
	dbPath := filepath.Join(nested, "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
}

func mkdirAt(path string, mode uint32) error {
	return os.MkdirAll(path, os.FileMode(mode))
}

func TestRecover_NilLogReturnsError(t *testing.T) {
	if _, err := agent.RecoverWith(context.Background(), nil, time.Now(), time.Second); err == nil {
		t.Fatal("RecoverWith(nil log) should error")
	}
}

func TestRecover_ExercisesProductionRecover(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	// Seed a stale agent so Recover has work to do.
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "stale", RootID: "stale", Title: "x", Model: "m",
	})
	if _, err := log.Append(ctx, agent.Event{
		AgentID: "stale", TS: now.Add(-2 * time.Hour),
		Type: agent.EvtStateChange, Payload: created,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := log.InsertAgent(ctx, agent.AgentRow{
		ID: "stale", RootID: "stale", State: agent.StateRunning, Attempt: 1,
		Title: "x", CreatedAt: now.Add(-2 * time.Hour),
		UpdatedAt:       now.Add(-2 * time.Hour),
		LastHeartbeatAt: now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	rep, err := agent.Recover(ctx, log)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(rep.Orphaned) != 1 {
		t.Errorf("Orphaned = %v, want one stale agent", rep.Orphaned)
	}
}

func TestSession_PreviewDecodeMalformedReturnsEmpty(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	// Seed a session row.
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "session-1", RootID: "session-1", Title: "T", Model: "m",
	})
	if _, err := log.Append(ctx, agent.Event{
		AgentID: "session-1", TS: now,
		Type: agent.EvtStateChange, Payload: created,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := log.InsertAgent(ctx, agent.AgentRow{
		ID: "session-1", RootID: "session-1", State: agent.StateRunning,
		Attempt:   1,
		Title:     "T",
		CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Append a malformed user_message event so the preview decoder hits
	// its error branch.
	if _, err := log.Append(ctx, agent.Event{
		AgentID: "session-1", TS: now,
		Type: agent.EvtUserMessage, Payload: []byte("not-json"),
	}); err != nil {
		t.Fatalf("append user msg: %v", err)
	}
	sessions, err := agent.ListUserSessions(ctx, log, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	if sessions[0].UserMsgs != 1 {
		t.Errorf("UserMsgs = %d, want 1", sessions[0].UserMsgs)
	}
	if sessions[0].Preview != "" {
		t.Errorf("malformed payload should yield empty preview, got %q", sessions[0].Preview)
	}
}

func TestSession_MostRecentEmptyReturnsErrNoSessions(t *testing.T) {
	log := openLog(t)
	_, err := agent.MostRecentUserSession(context.Background(), log)
	if err == nil {
		t.Fatal("expected ErrNoSessions on empty log")
	}
}

func TestSession_ExcludeFiltersThatAgent(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, id := range []string{"a", "b"} {
		created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
			ID: id, RootID: id, Title: id, Model: "m",
		})
		if _, err := log.Append(ctx, agent.Event{
			AgentID: id, TS: now, Type: agent.EvtStateChange, Payload: created,
		}); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
		if err := log.InsertAgent(ctx, agent.AgentRow{
			ID: id, RootID: id, State: agent.StateRunning, Attempt: 1,
			Title: id, CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
		}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	sessions, err := agent.ListUserSessions(ctx, log, "a")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "b" {
		t.Errorf("after exclude=a: %+v, want [b]", sessions)
	}
}

func TestCountEvents_WorksOnEmptyLog(t *testing.T) {
	log := openLog(t)
	n, err := agent.CountEvents(context.Background(), log)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("CountEvents = %d, want 0", n)
	}
}

func TestMaxSeq_EmptyLog(t *testing.T) {
	log := openLog(t)
	n, err := agent.MaxSeq(context.Background(), log.DB())
	if err != nil {
		t.Fatalf("maxseq: %v", err)
	}
	if n != 0 {
		t.Errorf("MaxSeq = %d, want 0", n)
	}
}

func TestSubscribe_DeliversThenUnsubStops(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	ch, unsub, err := log.Subscribe("alpha")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Append one event; subscriber should see it.
	if _, err := log.Append(ctx, agent.Event{
		AgentID: "alpha", TS: time.Now().UTC().Truncate(time.Millisecond),
		Type: agent.EvtHeartbeat, Payload: []byte(`{}`),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	select {
	case ev := <-ch:
		if ev.AgentID != "alpha" {
			t.Errorf("ev.AgentID = %q, want alpha", ev.AgentID)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive event")
	}

	unsub()
	// Subscribe with a different agent id but same channel buffer for
	// fresh state; just verify unsub is idempotent.
	unsub()
}

func TestSubscribe_NoSubscribersIsCheap(t *testing.T) {
	log := openLog(t)
	// Append without any subscriber: just exercises the publish-fast-path.
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: "noone", TS: time.Now().UTC().Truncate(time.Millisecond),
		Type: agent.EvtHeartbeat, Payload: []byte(`{}`),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func TestSubscribe_DropsOnFullChannel(t *testing.T) {
	log := openLog(t)
	ch, unsub, _ := log.Subscribe("flood")
	defer unsub()
	// Fill the channel without draining; subsequent Append should not
	// block, the publish select should drop the event.
	// The channel cap is 64 (per the eventlog code), but we don't
	// hardcode it; instead, append 200 events and assert we don't deadlock.
	for i := 0; i < 200; i++ {
		_, err := log.Append(context.Background(), agent.Event{
			AgentID: "flood", TS: time.Now().UTC().Truncate(time.Millisecond),
			Type: agent.EvtHeartbeat, Payload: []byte(`{}`),
		})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// Drain to ensure no deadlock on goroutine GC.
	drained := 0
	for {
		select {
		case <-ch:
			drained++
		default:
			if drained == 0 {
				t.Error("subscriber drained 0 events; expected at least one")
			}
			return
		}
	}
}

// TestLastToolCall_EmptyLog covers the no-rows path: a fresh log with
// no events for the agent must report ok=false without error.
func TestLastToolCall_EmptyLog(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	name, ok, err := log.LastToolCall(ctx, "ghost")
	if err != nil {
		t.Fatalf("LastToolCall: %v", err)
	}
	if ok {
		t.Errorf("ok = true on empty log; want false")
	}
	if name != "" {
		t.Errorf("name = %q on empty log; want empty", name)
	}
}

// TestLastToolCall_NonToolEventsIgnored seeds a few non-tool events
// (heartbeat, state_change) and confirms the helper still reports
// ok=false because none of them are tool_call rows.
func TestLastToolCall_NonToolEventsIgnored(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "a", RootID: "a", Title: "x", Model: "m",
	})
	if _, err := log.Append(ctx, agent.Event{
		AgentID: "a", TS: now,
		Type: agent.EvtStateChange, Payload: created,
	}); err != nil {
		t.Fatalf("append created: %v", err)
	}
	if _, err := log.Append(ctx, agent.Event{
		AgentID: "a", TS: now.Add(time.Second),
		Type: agent.EvtHeartbeat, Payload: []byte(`{}`),
	}); err != nil {
		t.Fatalf("append hb: %v", err)
	}

	name, ok, err := log.LastToolCall(ctx, "a")
	if err != nil {
		t.Fatalf("LastToolCall: %v", err)
	}
	if ok {
		t.Errorf("ok = true on non-tool events; want false")
	}
	if name != "" {
		t.Errorf("name = %q; want empty", name)
	}
}

// TestLastToolCall_ReturnsMostRecent appends a sequence of tool calls
// and confirms each subsequent call sees the latest. A trailing tool
// result must NOT shift the answer back to that result's name; we
// surface the most recent call, not the most recent observation.
func TestLastToolCall_ReturnsMostRecent(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	mustAppendToolCall := func(name string, at time.Time) {
		t.Helper()
		payload, err := json.Marshal(agent.ToolCall{Name: name})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := log.Append(ctx, agent.Event{
			AgentID: "a", TS: at,
			Type: agent.EvtToolCall, Payload: payload,
		}); err != nil {
			t.Fatalf("append %s: %v", name, err)
		}
	}

	// First tool call: bash.
	mustAppendToolCall("bash", now)
	got, ok, err := log.LastToolCall(ctx, "a")
	if err != nil || !ok || got != "bash" {
		t.Fatalf("first call: got=%q ok=%v err=%v; want bash/true/nil", got, ok, err)
	}

	// Second tool call: glob (later seq).
	mustAppendToolCall("glob", now.Add(time.Second))
	got, ok, err = log.LastToolCall(ctx, "a")
	if err != nil || !ok || got != "glob" {
		t.Fatalf("second call: got=%q ok=%v err=%v; want glob/true/nil", got, ok, err)
	}

	// A trailing tool_result event for "glob" must NOT shift the
	// answer; we ask for the last *call*, not the last observation.
	resultPayload, err := json.Marshal(agent.ToolResult{Name: "glob", Output: []byte("ok")})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if _, err := log.Append(ctx, agent.Event{
		AgentID: "a", TS: now.Add(2 * time.Second),
		Type: agent.EvtToolResult, Payload: resultPayload,
	}); err != nil {
		t.Fatalf("append result: %v", err)
	}
	got, ok, err = log.LastToolCall(ctx, "a")
	if err != nil || !ok || got != "glob" {
		t.Errorf("after tool result: got=%q ok=%v err=%v; want still glob/true/nil", got, ok, err)
	}
}

// TestLastToolCall_CorruptPayloadFallsThroughSilently confirms the
// defensive json.Unmarshal branch: a broken payload returns ok=false
// rather than propagating an error, so a single bad row never poisons
// the inline child-snapshot path.
func TestLastToolCall_CorruptPayloadFallsThroughSilently(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	if _, err := log.Append(ctx, agent.Event{
		AgentID: "a", TS: now,
		Type: agent.EvtToolCall, Payload: []byte(`{not-json`),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, ok, err := log.LastToolCall(ctx, "a")
	if err != nil {
		t.Fatalf("LastToolCall: %v", err)
	}
	if ok {
		t.Errorf("ok = true on corrupt payload; want false")
	}
	if got != "" {
		t.Errorf("got = %q on corrupt payload; want empty", got)
	}
}
