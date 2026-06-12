package agent

// ListChildSnapshots is the durable (projection-table) children read the
// web crew column relies on: unlike Supervisor.SnapshotChildrenOf it must
// keep answering after the children terminate. White-box so fixtures can
// write rows in arbitrary shapes (states, spend, corrupt state strings).

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// mkChildRow inserts a sub-agent row under parentID with explicit state
// and spend columns.
func mkChildRow(t *testing.T, log *SQLiteEventLog, id, parentID string, state State, tokensIn, tokensOut, costCents int64, agedBy time.Duration) {
	t.Helper()
	now := time.Now().UTC().Add(-agedBy).Truncate(time.Millisecond)
	if err := log.InsertAgent(context.Background(), AgentRow{
		ID: id, ParentID: parentID, RootID: parentID, State: state, Attempt: 1,
		Title: "sub " + id, Model: "test-model",
		CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("insert child %s: %v", id, err)
	}
	// InsertAgent doesn't carry spend columns (they accrue via updates);
	// set them directly so the snapshot read has real numbers to project.
	if _, err := log.DB().Exec(
		`UPDATE agents SET tokens_in = ?, tokens_out = ?, cost_cents = ? WHERE id = ?`,
		tokensIn, tokensOut, costCents, id,
	); err != nil {
		t.Fatalf("set spend for %s: %v", id, err)
	}
}

func TestListChildSnapshots_FinishedChildrenSurvive(t *testing.T) {
	log := newSessionLog(t)
	mkSession(t, log, "01HPARENT", "the thread", "", time.Hour)
	mkChildRow(t, log, "01HKID1", "01HPARENT", StateDone, 1000, 200, 3, 30*time.Minute)
	mkChildRow(t, log, "01HKID2", "01HPARENT", StateFailed, 500, 50, 1, 20*time.Minute)

	got, err := ListChildSnapshots(context.Background(), log, "01HPARENT")
	if err != nil {
		t.Fatalf("ListChildSnapshots: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 finished children, got %d: %+v", len(got), got)
	}
	// Oldest-first (spawn order).
	if got[0].AgentID != "01HKID1" || got[1].AgentID != "01HKID2" {
		t.Errorf("order = [%s %s], want [01HKID1 01HKID2]", got[0].AgentID, got[1].AgentID)
	}
	if got[0].State != StateDone || got[1].State != StateFailed {
		t.Errorf("states = [%s %s], want [done failed]", got[0].State, got[1].State)
	}
	if got[0].Tokens != 1200 {
		t.Errorf("tokens = %d, want 1200 (in+out)", got[0].Tokens)
	}
	if got[0].CostCents != 3 {
		t.Errorf("cost = %d, want 3", got[0].CostCents)
	}
	if got[0].Title != "sub 01HKID1" {
		t.Errorf("title = %q", got[0].Title)
	}
	if got[0].StartedAt.IsZero() {
		t.Error("started_at zero")
	}
}

func TestListChildSnapshots_LastToolEnrichment(t *testing.T) {
	log := newSessionLog(t)
	mkSession(t, log, "01HPAR", "thread", "", time.Hour)
	mkChildRow(t, log, "01HKID", "01HPAR", StateDone, 0, 0, 0, time.Minute)
	payload, _ := json.Marshal(ToolCall{Name: "read_file", Input: []byte(`{}`)})
	if _, err := log.Append(context.Background(), Event{
		AgentID: "01HKID", TS: time.Now().UTC(), Type: EvtToolCall, Payload: payload,
	}); err != nil {
		t.Fatalf("append tool call: %v", err)
	}

	got, err := ListChildSnapshots(context.Background(), log, "01HPAR")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].LastTool != "read_file" {
		t.Errorf("last_tool = %+v, want read_file", got)
	}
}

func TestListChildSnapshots_NoChildrenIsEmpty(t *testing.T) {
	log := newSessionLog(t)
	mkSession(t, log, "01HLONE", "childless", "", time.Minute)
	got, err := ListChildSnapshots(context.Background(), log, "01HLONE")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("childless thread returned %d children", len(got))
	}
}

func TestListChildSnapshots_DoesNotLeakSiblings(t *testing.T) {
	// Children of thread A must never surface under thread B.
	log := newSessionLog(t)
	mkSession(t, log, "01HA", "thread a", "", time.Hour)
	mkSession(t, log, "01HB", "thread b", "", time.Hour)
	mkChildRow(t, log, "01HAKID", "01HA", StateDone, 0, 0, 0, time.Minute)

	got, err := ListChildSnapshots(context.Background(), log, "01HB")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("thread B sees thread A's children: %+v", got)
	}
}

func TestListChildSnapshots_BadInputs(t *testing.T) {
	log := newSessionLog(t)
	// Empty parent id: no query, no error - "" is the legacy top-level
	// bucket, not a thread.
	if got, err := ListChildSnapshots(context.Background(), log, ""); err != nil || got != nil {
		t.Errorf("empty parent: got=%v err=%v, want nil/nil", got, err)
	}
	// Nil log: defensive nil/nil (mirrors SnapshotChildrenOf).
	if got, err := ListChildSnapshots(context.Background(), nil, "x"); err != nil || got != nil {
		t.Errorf("nil log: got=%v err=%v, want nil/nil", got, err)
	}
}

func TestListChildSnapshots_SkipsUnparseableState(t *testing.T) {
	log := newSessionLog(t)
	mkSession(t, log, "01HP", "thread", "", time.Hour)
	mkChildRow(t, log, "01HOK", "01HP", StateDone, 0, 0, 0, 2*time.Minute)
	// Corrupt the second child's state string directly.
	mkChildRow(t, log, "01HBAD", "01HP", StateDone, 0, 0, 0, time.Minute)
	if _, err := log.DB().Exec(`UPDATE agents SET state = 'nonsense' WHERE id = '01HBAD'`); err != nil {
		t.Fatalf("corrupt state: %v", err)
	}

	got, err := ListChildSnapshots(context.Background(), log, "01HP")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AgentID != "01HOK" {
		t.Errorf("corrupt-state row should be skipped, got %+v", got)
	}
}

func TestListChildSnapshots_ClosedDBErrors(t *testing.T) {
	log := newSessionLog(t)
	_ = log.Close()
	if _, err := ListChildSnapshots(context.Background(), log, "x"); err == nil {
		t.Error("closed DB should error")
	}
}
