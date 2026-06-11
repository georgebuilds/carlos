package agent_test

// Coverage for sessions.go + lifecycle.go + projection.go error and
// edge branches:
//
//   - ListUserSessions / MostRecentUserSession DB-error propagation.
//   - The unknown-state projection-skip branch in ListUserSessions.
//   - firstUserMessage's preview-decode error tolerance (the preview
//     stays blank, count still lands).
//   - RecoverWith DB-error paths and the stale-vs-active partitioning
//     (still-active filter loop).
//   - OpenStateDB bad-path and verifySchema-on-corrupt-DB branches.
//   - Replay / ReplayAll error propagation and the unsorted Snapshot
//     comparator.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

func TestListUserSessions_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if _, err := agent.ListUserSessions(context.Background(), log, ""); err == nil {
		t.Fatal("ListUserSessions on closed DB should error")
	}
}

func TestMostRecentUserSession_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if _, err := agent.MostRecentUserSession(context.Background(), log); err == nil {
		t.Fatal("MostRecentUserSession on closed DB should error")
	}
}

// TestListUserSessions_UnknownStateRowSkipped writes a top-level agent
// row with a bogus state directly through the DB handle so the picker
// hits the parseState-skip branch (drop the row, don't fail the list).
func TestListUserSessions_UnknownStateRowSkipped(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	now := time.Now().UTC().UnixMilli()
	if _, err := log.DB().ExecContext(ctx,
		`INSERT INTO agents(id, root_id, state, attempt, title, created_at, updated_at, last_heartbeat_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"01HBAD", "01HBAD", "corrupt-state", 1, "bad row", now, now, now,
	); err != nil {
		t.Fatalf("raw insert bad row: %v", err)
	}
	// Plus one good row so we can confirm the list survives the skip.
	seedAgent(t, ctx, log, "01HGOOD", "good row", agent.StateRunning, time.Now().UTC())

	got, err := agent.ListUserSessions(ctx, log, "")
	if err != nil {
		t.Fatalf("list should not fail on a bad row: %v", err)
	}
	if len(got) != 1 || got[0].ID != "01HGOOD" {
		t.Fatalf("bad-state row should be dropped, got %+v", got)
	}
}

// TestListUserSessions_PreviewDecodeErrorTolerated seeds a session whose
// first EvtUserMessage payload is non-JSON. The count still lands (the
// COUNT query is independent of payload shape) but the preview decode
// returns "" rather than erroring the picker.
func TestListUserSessions_PreviewDecodeErrorTolerated(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	seedAgent(t, ctx, log, "01HCORRUPT", "corrupt preview", agent.StateRunning, time.Now().UTC())
	if _, err := log.Append(ctx, agent.Event{
		AgentID: "01HCORRUPT", TS: time.Now().UTC(), Type: agent.EvtUserMessage, Payload: []byte("\x00\x01 not json"),
	}); err != nil {
		t.Fatalf("append corrupt user msg: %v", err)
	}
	got, err := agent.ListUserSessions(ctx, log, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 session, got %d", len(got))
	}
	if got[0].UserMsgs != 1 {
		t.Errorf("count should still land: got %d", got[0].UserMsgs)
	}
	if got[0].Preview != "" {
		t.Errorf("corrupt payload preview should be empty, got %q", got[0].Preview)
	}
}

// TestListUserSessions_EmptyPayloadPreview seeds a user message with an
// empty payload, hitting decodeUserMessagePreview's len==0 early return.
func TestListUserSessions_EmptyPayloadPreview(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	seedAgent(t, ctx, log, "01HEMPTY", "empty payload", agent.StateRunning, time.Now().UTC())
	if _, err := log.Append(ctx, agent.Event{
		AgentID: "01HEMPTY", TS: time.Now().UTC(), Type: agent.EvtUserMessage, Payload: []byte{},
	}); err != nil {
		t.Fatalf("append empty user msg: %v", err)
	}
	got, _ := agent.ListUserSessions(ctx, log, "")
	if len(got) != 1 || got[0].Preview != "" || got[0].UserMsgs != 1 {
		t.Fatalf("empty-payload session: %+v", got)
	}
}

// --- RecoverWith error + partitioning paths ---

func TestRecoverWith_NilLogErrors(t *testing.T) {
	if _, err := agent.RecoverWith(context.Background(), nil, time.Now().UTC(), time.Second); err == nil {
		t.Fatal("RecoverWith(nil log) should error")
	}
}

func TestRecoverWith_ClosedDBStaleScanErrors(t *testing.T) {
	log := closedLog(t, nil)
	if _, err := agent.RecoverWith(context.Background(), log, time.Now().UTC(), time.Second); err == nil {
		t.Fatal("RecoverWith stale scan on closed DB should error")
	}
}

// TestRecoverWith_PartitionsStaleAndActive seeds one stale agent (old
// heartbeat) and one fresh agent. RecoverWith should orphan the stale
// one and report the fresh one as still-active (exercising the
// isStale-continue branch in the partition loop).
func TestRecoverWith_PartitionsStaleAndActive(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Stale: heartbeat 10 minutes ago, non-terminal.
	seedAgent(t, ctx, log, "01HSTALE", "stale", agent.StateRunning, now.Add(-10*time.Minute))
	// Fresh: heartbeat just now, non-terminal.
	seedAgent(t, ctx, log, "01HFRESH", "fresh", agent.StateRunning, now.Add(-1*time.Second))

	rep, err := agent.RecoverWith(ctx, log, now, time.Minute)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(rep.Orphaned) != 1 || rep.Orphaned[0] != "01HSTALE" {
		t.Errorf("orphaned = %v, want [01HSTALE]", rep.Orphaned)
	}
	if len(rep.StillActive) != 1 || rep.StillActive[0] != "01HFRESH" {
		t.Errorf("stillActive = %v, want [01HFRESH]", rep.StillActive)
	}
	// The stale row's projection state should now be orphaned.
	row, ok, err := log.GetAgent(ctx, "01HSTALE")
	if err != nil || !ok {
		t.Fatalf("get stale: ok=%v err=%v", ok, err)
	}
	if row.State != agent.StateOrphaned {
		t.Errorf("stale agent state = %v, want orphaned", row.State)
	}
}

// --- OpenStateDB / verifySchema error branches ---

// TestOpenStateDB_EmptyPathErrors hits the empty-path guard.
func TestOpenStateDB_EmptyPathErrors(t *testing.T) {
	if _, err := agent.OpenStateDB(""); err == nil {
		t.Fatal("OpenStateDB(\"\") should error")
	}
}

// TestOpenStateDB_MkdirFailsWhenParentIsFile points the parent dir at an
// existing regular file so MkdirAll fails (a file in the path component).
func TestOpenStateDB_MkdirFailsWhenParentIsFile(t *testing.T) {
	dir := t.TempDir()
	fileAsDir := filepath.Join(dir, "iamafile")
	if err := os.WriteFile(fileAsDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	// state.db's parent dir would be fileAsDir/sub, but fileAsDir is a
	// regular file → MkdirAll fails.
	dbPath := filepath.Join(fileAsDir, "sub", "state.db")
	if _, err := agent.OpenStateDB(dbPath); err == nil {
		t.Fatal("OpenStateDB should fail when a path component is a file")
	}
}

// TestCloseStateDB_NilIsNoOp pins the nil-guard.
func TestCloseStateDB_NilIsNoOp(t *testing.T) {
	if err := agent.CloseStateDB(nil); err != nil {
		t.Fatalf("CloseStateDB(nil) = %v, want nil", err)
	}
}

// --- Replay / ReplayAll error propagation ---

func TestReplay_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if _, err := agent.Replay(context.Background(), log, "x"); err == nil {
		t.Fatal("Replay on closed DB should error")
	}
}

func TestReplayAll_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if _, err := agent.ReplayAll(context.Background(), log); err == nil {
		t.Fatal("ReplayAll on closed DB should error")
	}
}

// TestReplay_MalformedEventErrors appends an event whose type is unknown
// so Projection.Apply returns an error during replay, exercising
// Replay's apply-failure wrap.
func TestReplay_MalformedEventErrors(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	if _, err := log.Append(ctx, agent.Event{
		AgentID: "01HMAL", TS: time.Now().UTC(), Type: agent.EventType("totally-unknown-type"), Payload: []byte("{}"),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := agent.Replay(ctx, log, "01HMAL"); err == nil {
		t.Fatal("Replay over an unknown event type should error")
	}
	// ReplayAll funnels through Replay, so it surfaces the same error.
	if _, err := agent.ReplayAll(ctx, log); err == nil {
		t.Fatal("ReplayAll over an unknown event type should error")
	}
}

// TestSnapshot_SortsByID seeds two agents out of ID order and confirms
// Snapshot returns them sorted (exercising the comparator's swap path).
func TestSnapshot_SortsByID(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	// Append a created event for each so Replay populates two rows.
	for _, id := range []string{"01HZZZ", "01HAAA"} {
		created, err := agent.NewStateChangeCreated(agent.AgentCreated{ID: id, RootID: id, Title: id, Model: "fake"})
		if err != nil {
			t.Fatalf("marshal created: %v", err)
		}
		if _, err := log.Append(ctx, agent.Event{AgentID: id, TS: time.Now().UTC(), Type: agent.EvtStateChange, Payload: created}); err != nil {
			t.Fatalf("append created: %v", err)
		}
	}
	p, err := agent.ReplayAll(ctx, log)
	if err != nil {
		t.Fatalf("replayAll: %v", err)
	}
	snap := p.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("want 2 rows, got %d", len(snap))
	}
	if snap[0].ID != "01HAAA" || snap[1].ID != "01HZZZ" {
		t.Errorf("snapshot not sorted by id: %s, %s", snap[0].ID, snap[1].ID)
	}
}

func TestRecoverWith_NoStaleStillActiveOnly(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedAgent(t, ctx, log, "01HACTIVE", "active", agent.StateRunning, now)
	rep, err := agent.RecoverWith(ctx, log, now, time.Hour)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(rep.Orphaned) != 0 {
		t.Errorf("orphaned = %v, want none", rep.Orphaned)
	}
	if len(rep.StillActive) != 1 || rep.StillActive[0] != "01HACTIVE" {
		t.Errorf("stillActive = %v", rep.StillActive)
	}
}
