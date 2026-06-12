package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// delTestLog opens a fresh event log in a temp dir.
func delTestLog(t *testing.T) *SQLiteEventLog {
	t.Helper()
	log, err := OpenSQLiteEventLog(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// seedAgent inserts an agent row with an explicit heartbeat age (stale by
// default unless heartbeat is recent).
func seedAgent(t *testing.T, log *SQLiteEventLog, id, parent, root string, hb time.Time) {
	t.Helper()
	now := time.Now().UTC()
	if err := log.InsertAgent(context.Background(), AgentRow{
		ID: id, ParentID: parent, RootID: root, State: StateOrphaned, Attempt: 1,
		Title: "t", Model: "m", CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: hb,
	}); err != nil {
		t.Fatalf("insert agent %s: %v", id, err)
	}
}

func seedEvt(t *testing.T, log *SQLiteEventLog, id string, typ EventType) {
	t.Helper()
	b, _ := json.Marshal(MessagePayload{Text: "x"})
	if _, err := log.Append(context.Background(), Event{AgentID: id, TS: time.Now().UTC(), Type: typ, Payload: b}); err != nil {
		t.Fatalf("append %s: %v", typ, err)
	}
}

func countRows(t *testing.T, log *SQLiteEventLog, q string, args ...any) int {
	t.Helper()
	var n int
	if err := log.DB().QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestDeleteSession_CascadesLineageEventsAndArtifacts(t *testing.T) {
	log := delTestLog(t)
	ctx := context.Background()
	stale := time.Now().Add(-time.Hour)

	// Thread "top" with a sub-agent "kid" (shares root_id=top). Both carry
	// events; the kid also has an artifact. A separate thread "other" must
	// survive untouched.
	seedAgent(t, log, "top", "", "top", stale)
	seedAgent(t, log, "kid", "top", "top", stale)
	seedAgent(t, log, "other", "", "other", stale)
	seedEvt(t, log, "top", EvtUserMessage)
	seedEvt(t, log, "top", EvtAssistantMessage)
	seedEvt(t, log, "kid", EvtToolCall)
	seedEvt(t, log, "other", EvtUserMessage)
	if err := log.InsertArtifact(ctx, Artifact{
		ID: "art1", AgentID: "kid", Path: "/tmp/x", Kind: "file",
		SHA256: "deadbeef", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert artifact: %v", err)
	}

	n, err := DeleteSession(ctx, log, "top", false)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d agents, want 2 (thread + sub-agent)", n)
	}

	// The whole lineage is gone: agents, events, artifacts.
	if c := countRows(t, log, `SELECT COUNT(*) FROM agents WHERE root_id='top'`); c != 0 {
		t.Errorf("agents left: %d", c)
	}
	if c := countRows(t, log, `SELECT COUNT(*) FROM events WHERE agent_id IN ('top','kid')`); c != 0 {
		t.Errorf("events left: %d", c)
	}
	if c := countRows(t, log, `SELECT COUNT(*) FROM artifacts WHERE agent_id='kid'`); c != 0 {
		t.Errorf("artifacts left: %d", c)
	}
	// The sibling thread is untouched.
	if c := countRows(t, log, `SELECT COUNT(*) FROM agents WHERE id='other'`); c != 1 {
		t.Errorf("sibling thread should survive, agents=%d", c)
	}
	if c := countRows(t, log, `SELECT COUNT(*) FROM events WHERE agent_id='other'`); c != 1 {
		t.Errorf("sibling events should survive, events=%d", c)
	}
}

func TestDeleteSession_NotFound(t *testing.T) {
	log := delTestLog(t)
	if _, err := DeleteSession(context.Background(), log, "ghost", false); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("got %v, want ErrSessionNotFound", err)
	}
}

func TestDeleteSession_RefusesSubAgent(t *testing.T) {
	log := delTestLog(t)
	stale := time.Now().Add(-time.Hour)
	seedAgent(t, log, "top", "", "top", stale)
	seedAgent(t, log, "kid", "top", "top", stale)
	if _, err := DeleteSession(context.Background(), log, "kid", false); !errors.Is(err, ErrNotTopLevel) {
		t.Errorf("got %v, want ErrNotTopLevel", err)
	}
	// Nothing was deleted.
	if c := countRows(t, log, `SELECT COUNT(*) FROM agents`); c != 2 {
		t.Errorf("agents=%d, want 2 (refusal must not delete)", c)
	}
}

func TestDeleteSession_RefusesLiveSession(t *testing.T) {
	log := delTestLog(t)
	seedAgent(t, log, "top", "", "top", time.Now().UTC()) // fresh heartbeat
	if _, err := DeleteSession(context.Background(), log, "top", false); !errors.Is(err, ErrSessionLive) {
		t.Errorf("got %v, want ErrSessionLive", err)
	}
	if c := countRows(t, log, `SELECT COUNT(*) FROM agents WHERE id='top'`); c != 1 {
		t.Error("live session must not be deleted")
	}
}

func TestDeleteSession_ForceDeletesLiveSession(t *testing.T) {
	log := delTestLog(t)
	// Fresh heartbeat would normally refuse, but force=true (the owner
	// deleting its own just-detached thread) bypasses the live guard.
	seedAgent(t, log, "top", "", "top", time.Now().UTC())
	n, err := DeleteSession(context.Background(), log, "top", true)
	if err != nil {
		t.Fatalf("force delete: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d, want 1", n)
	}
	if c := countRows(t, log, `SELECT COUNT(*) FROM agents WHERE id='top'`); c != 0 {
		t.Error("force delete should remove the live session")
	}
}

func TestDeleteSession_StaleLiveBoundary(t *testing.T) {
	log := delTestLog(t)
	// Just past the staleness tolerance => deletable.
	seedAgent(t, log, "top", "", "top", time.Now().Add(-StalenessTolerance-time.Second))
	if _, err := DeleteSession(context.Background(), log, "top", false); err != nil {
		t.Errorf("a thread past the staleness window should delete, got %v", err)
	}
}

func TestDeleteSession_LookupErrorOnClosedDB(t *testing.T) {
	log := delTestLog(t)
	_ = log.Close() // subsequent queries fail (not ErrNoRows)
	if _, err := DeleteSession(context.Background(), log, "anything", false); err == nil ||
		errors.Is(err, ErrSessionNotFound) {
		t.Errorf("closed-db lookup should surface a wrapped error, got %v", err)
	}
}

func TestDeleteLineageTx_BeginErrorOnClosedDB(t *testing.T) {
	log := delTestLog(t)
	db := log.DB()
	_ = log.Close() // BeginTx now fails
	if err := deleteLineageTx(context.Background(), db, "top"); err == nil {
		t.Error("deleteLineageTx on a closed db should error at begin")
	}
}

func TestDeleteSession_DeleteErrorRollsBack(t *testing.T) {
	log := delTestLog(t)
	ctx := context.Background()
	seedAgent(t, log, "top", "", "top", time.Now().Add(-time.Hour))
	seedEvt(t, log, "top", EvtUserMessage)
	// Drop the events table so the cascade's DELETE fails after begin +
	// pragma + count have run, exercising the transaction error path.
	if _, err := log.DB().ExecContext(ctx, `DROP TABLE events`); err != nil {
		t.Fatalf("drop events: %v", err)
	}
	if _, err := DeleteSession(ctx, log, "top", false); err == nil {
		t.Error("expected an error when the cascade delete fails")
	}
	// Rolled back: the agent row is still present.
	if c := countRows(t, log, `SELECT COUNT(*) FROM agents WHERE id='top'`); c != 1 {
		t.Errorf("failed delete must roll back; agents=%d", c)
	}
}
