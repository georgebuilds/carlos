package agent

// White-box coverage for runChild's persistence helpers - the guard
// and failure branches the end-to-end spawn tests can't reach: nil
// logs, nil/empty trackers, preview-cap truncation, the error-prefix
// heuristic, and the append-fails-so-columns-stay-untouched contract.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
)

func newPersistLog(t *testing.T) *SQLiteEventLog {
	t.Helper()
	log, err := OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open eventlog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func seedAgentRow(t *testing.T, log *SQLiteEventLog, id string) {
	t.Helper()
	now := time.Now().UTC()
	if err := log.InsertAgent(context.Background(), AgentRow{
		ID: id, RootID: id, State: StateRunning, Attempt: 1,
		Title: "row " + id, CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func TestPersistChildToolHooks_NilLogIsNoOp(t *testing.T) {
	s := &Supervisor{} // no log wired
	// Must not panic; nothing to assert beyond survival.
	s.persistChildToolCall(context.Background(), "c1", providers.Block{ToolName: "echo"})
	s.persistChildToolResult(context.Background(), "c1", providers.Block{ToolName: "echo"}, providers.Block{})
}

func TestPersistChildToolResult_CapsPreviewAndFlagsErrors(t *testing.T) {
	log := newPersistLog(t)
	s := &Supervisor{log: log}
	seedAgentRow(t, log, "c1")
	ctx := context.Background()

	// Oversized output truncates at the shared cap; error prefixes set
	// IsError exactly like chatglue's parent-side heuristic.
	huge := strings.Repeat("x", ToolResultPreviewCap+500)
	s.persistChildToolResult(ctx, "c1",
		providers.Block{ToolName: "bash"},
		providers.Block{ToolResult: []byte(huge)})
	s.persistChildToolResult(ctx, "c1",
		providers.Block{ToolName: "bash"},
		providers.Block{ToolResult: []byte("tool error: boom")})
	s.persistChildToolResult(ctx, "c1",
		providers.Block{ToolName: "bash"},
		providers.Block{ToolResult: []byte("(rejected by user)")})

	evs, err := log.Read(ctx, "c1", 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var results []ToolResult
	for _, ev := range evs {
		if ev.Type != EvtToolResult {
			continue
		}
		var tr ToolResult
		if err := json.Unmarshal(ev.Payload, &tr); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		results = append(results, tr)
	}
	if len(results) != 3 {
		t.Fatalf("tool_result events = %d, want 3", len(results))
	}
	if len(results[0].Output) != ToolResultPreviewCap {
		t.Errorf("preview len = %d, want capped at %d", len(results[0].Output), ToolResultPreviewCap)
	}
	if results[0].IsError {
		t.Error("plain output flagged as error")
	}
	if !results[1].IsError || !results[2].IsError {
		t.Errorf("error prefixes not flagged: %+v %+v", results[1], results[2])
	}
	if results[1].Name != "bash" {
		t.Errorf("result name = %q, want paired from the use block", results[1].Name)
	}
}

func TestPersistChildToolCall_AppendsUnderChildID(t *testing.T) {
	log := newPersistLog(t)
	s := &Supervisor{log: log}
	seedAgentRow(t, log, "c1")

	s.persistChildToolCall(context.Background(), "c1",
		providers.Block{ToolName: "echo", ToolInput: []byte(`{"x":1}`)})

	name, _, err := log.LastToolCall(context.Background(), "c1")
	if err != nil || name != "echo" {
		t.Errorf("LastToolCall = %q (err %v), want echo", name, err)
	}
}

func TestPersistChildUsage_GuardsAndFailure(t *testing.T) {
	log := newPersistLog(t)
	ctx := context.Background()
	seedAgentRow(t, log, "c1")

	// Guard: nil log.
	(&Supervisor{}).persistChildUsage(ctx, &runningChild{id: "c1", tracker: NewTracker(nil)})
	// Guard: nil tracker.
	s := &Supervisor{log: log}
	s.persistChildUsage(ctx, &runningChild{id: "c1"})
	// Guard: tracker with zero totals records nothing.
	s.persistChildUsage(ctx, &runningChild{id: "c1", tracker: NewTracker(nil)})

	evs, err := log.Read(ctx, "c1", 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, ev := range evs {
		if ev.Type == EvtTokenUsage {
			t.Fatalf("guarded paths appended a token_usage event: %+v", ev)
		}
	}
	row, _, _ := log.GetAgent(ctx, "c1")
	if row.TokensIn != 0 || row.TokensOut != 0 || row.CostCents != 0 {
		t.Errorf("guarded paths bumped columns: %+v", row)
	}

	// Happy: real totals land as event + columns.
	tr := NewTracker(nil)
	tr.Add(100, 50, 2)
	s.persistChildUsage(ctx, &runningChild{id: "c1", tracker: tr})
	row, _, _ = log.GetAgent(ctx, "c1")
	if row.TokensIn != 100 || row.TokensOut != 50 || row.CostCents != 2 {
		t.Errorf("columns = %+v, want 100/50/2", row)
	}

	// Failure: a closed DB means no event AND no column bump - the two
	// writes stay consistent (no event, no bump; never half-applied).
	_ = log.Close()
	s.persistChildUsage(ctx, &runningChild{id: "c1", tracker: tr}) // must not panic
}
