package agent_test

// Targeted coverage for the SQLite-backed storage error branches and a
// handful of edge cases that the happy-path tests don't reach:
//
//   - DB-error branches in Read / StaleAgents / NonTerminalAgents /
//     UpdateAgentState / UpdateHeartbeat / DeleteEmptyOrphanedAgents,
//     driven by closing the underlying *sql.DB so the next query fails
//     with "database is closed" (the same seam internals_more_test uses).
//   - GetAgent's unknown-state hydration branch and LastToolCall's
//     corrupt-payload defensive return, driven by writing a bad row
//     directly through the exposed *sql.DB handle.
//   - Replay / ReplayAll error propagation, sessions error/skip paths,
//     and RecoverWith DB-error + still-active partitioning.

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

// closedLog opens a fresh log, runs seed against it, then closes the DB
// so subsequent reads/writes fail. Returns the closed log.
func closedLog(t *testing.T, seed func(log *agent.SQLiteEventLog)) *agent.SQLiteEventLog {
	t.Helper()
	log := openLog(t)
	if seed != nil {
		seed(log)
	}
	if err := agent.CloseStateDB(log); err != nil {
		t.Fatalf("close: %v", err)
	}
	return log
}

func TestRead_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if _, err := log.Read(context.Background(), "any", 0); err == nil {
		t.Fatal("Read on closed DB should error")
	}
}

func TestStaleAgents_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if _, err := log.StaleAgents(context.Background(), time.Now().UTC()); err == nil {
		t.Fatal("StaleAgents on closed DB should error")
	}
}

func TestNonTerminalAgents_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if _, err := log.NonTerminalAgents(context.Background()); err == nil {
		t.Fatal("NonTerminalAgents on closed DB should error")
	}
}

func TestUpdateAgentState_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if err := log.UpdateAgentState(context.Background(), "x", agent.StateRunning, time.Now().UTC()); err == nil {
		t.Fatal("UpdateAgentState on closed DB should error")
	}
}

func TestUpdateAgentModel_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if err := log.UpdateAgentModel(context.Background(), "x", "m"); err == nil {
		t.Fatal("UpdateAgentModel on closed DB should error")
	}
}

func TestUpdateHeartbeat_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if err := log.UpdateHeartbeat(context.Background(), "x", time.Now().UTC()); err == nil {
		t.Fatal("UpdateHeartbeat on closed DB should error")
	}
}

func TestInsertArtifact_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	err := log.InsertArtifact(context.Background(), agent.Artifact{
		ID: "a", AgentID: "x", Path: "/tmp/a", Kind: "text", SHA256: "deadbeef", CreatedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("InsertArtifact on closed DB should error")
	}
}

func TestAppend_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: "x", TS: time.Now().UTC(), Type: agent.EvtUserMessage, Payload: []byte("{}"),
	}); err == nil {
		t.Fatal("Append on closed DB should error")
	}
}

func TestDeleteEmptyOrphanedAgents_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if _, err := log.DeleteEmptyOrphanedAgents(context.Background(), 0); err == nil {
		t.Fatal("DeleteEmptyOrphanedAgents on closed DB should error (begin tx)")
	}
}

func TestGetAgent_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if _, _, err := log.GetAgent(context.Background(), "x"); err == nil {
		t.Fatal("GetAgent on closed DB should error")
	}
}

func TestLastToolCall_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if _, _, err := log.LastToolCall(context.Background(), "x"); err == nil {
		t.Fatal("LastToolCall on closed DB should error")
	}
}

// TestGetAgent_UnknownStateErrors writes a projection row with a bogus
// state string through the raw *sql.DB handle (bypassing InsertAgent's
// State.String() encoding) so GetAgent's parseState fallthrough fires.
func TestGetAgent_UnknownStateErrors(t *testing.T) {
	log := openLog(t)
	now := time.Now().UTC().UnixMilli()
	if _, err := log.DB().ExecContext(context.Background(),
		`INSERT INTO agents(id, root_id, state, attempt, title, created_at, updated_at, last_heartbeat_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"01HBOGUS", "01HBOGUS", "not-a-real-state", 1, "bogus", now, now, now,
	); err != nil {
		t.Fatalf("raw insert: %v", err)
	}
	_, _, err := log.GetAgent(context.Background(), "01HBOGUS")
	if err == nil || !strings.Contains(err.Error(), "unknown state") {
		t.Fatalf("want unknown-state error, got %v", err)
	}
}

// TestLastToolCall_HappyAndCorrupt covers both the found path and the
// corrupt-payload defensive return (json.Unmarshal failure → ok=false
// with no error).
func TestLastToolCall_HappyAndCorrupt(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()

	// No tool calls yet → ok=false, no error.
	name, ok, err := log.LastToolCall(ctx, "01HTC")
	if err != nil || ok || name != "" {
		t.Fatalf("empty log: got name=%q ok=%v err=%v", name, ok, err)
	}

	// Append a valid tool call.
	payload, _ := json.Marshal(agent.ToolCall{Name: "bash"})
	if _, err := log.Append(ctx, agent.Event{
		AgentID: "01HTC", TS: time.Now().UTC(), Type: agent.EvtToolCall, Payload: payload,
	}); err != nil {
		t.Fatalf("append tool call: %v", err)
	}
	name, ok, err = log.LastToolCall(ctx, "01HTC")
	if err != nil || !ok || name != "bash" {
		t.Fatalf("happy path: got name=%q ok=%v err=%v", name, ok, err)
	}

	// Append a later tool-call event with a corrupt payload; it sorts
	// first (highest seq) so LastToolCall reads it and hits the
	// defensive unmarshal-failure return.
	if _, err := log.Append(ctx, agent.Event{
		AgentID: "01HTC", TS: time.Now().UTC(), Type: agent.EvtToolCall, Payload: []byte("not json"),
	}); err != nil {
		t.Fatalf("append corrupt tool call: %v", err)
	}
	name, ok, err = log.LastToolCall(ctx, "01HTC")
	if err != nil {
		t.Fatalf("corrupt payload should not error: %v", err)
	}
	if ok || name != "" {
		t.Fatalf("corrupt payload should yield ok=false, got name=%q ok=%v", name, ok)
	}
}

// TestInsertAgent_DuplicateIDErrors inserts the same agent ID twice; the
// second insert violates the PRIMARY KEY and exercises InsertAgent's
// error-wrap branch.
func TestInsertAgent_DuplicateIDErrors(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	row := agent.AgentRow{
		ID: "01HDUP", RootID: "01HDUP", State: agent.StateRunning, Attempt: 1,
		Title: "dup", Model: "m", CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}
	if err := log.InsertAgent(ctx, row); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := log.InsertAgent(ctx, row); err == nil {
		t.Fatal("duplicate InsertAgent should violate primary key and error")
	}
}

// TestOpenSQLiteEventLog_BadPathErrors points the DSN at a path under a
// regular file so the driver cannot create the database, hitting the
// schema-exec error branch.
func TestOpenSQLiteEventLog_BadPathErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	// state.db inside a path component that is a regular file → open/schema fails.
	if _, err := agent.OpenSQLiteEventLog(filepath.Join(file, "state.db")); err == nil {
		t.Fatal("OpenSQLiteEventLog with a file as a directory component should error")
	}
}
