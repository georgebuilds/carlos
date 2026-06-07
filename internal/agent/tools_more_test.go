package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// AgentTool Execute error paths + ApplyHandler subtle branches.

func TestAgentTool_Execute_NilSupervisorErrors(t *testing.T) {
	tool := agent.NewAgentTool(nil)
	if _, err := tool.Execute(context.Background(), []byte(`{}`)); err == nil {
		t.Fatal("nil supervisor should error")
	}
}

func TestAgentTool_Execute_MalformedInputErrors(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	sup := agent.NewSupervisor(log, newHangingProvider(), tools.NewRegistry())
	defer sup.Shutdown()
	tool := agent.NewAgentTool(sup)
	if _, err := tool.Execute(context.Background(), []byte("not-json")); err == nil {
		t.Fatal("malformed input should error")
	}
}

func TestAgentTool_Execute_MissingObjectiveErrors(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	sup := agent.NewSupervisor(log, newHangingProvider(), tools.NewRegistry())
	defer sup.Shutdown()
	tool := agent.NewAgentTool(sup)
	in, _ := json.Marshal(map[string]any{"output_format": "x", "tool_allowlist": []string{}})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("missing objective should error")
	}
}

func TestAgentTool_Execute_MissingOutputFormatErrors(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	sup := agent.NewSupervisor(log, newHangingProvider(), tools.NewRegistry())
	defer sup.Shutdown()
	tool := agent.NewAgentTool(sup)
	in, _ := json.Marshal(map[string]any{"objective": "x", "tool_allowlist": []string{}})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("missing output_format should error")
	}
}

func TestAgentTool_Execute_CapRefusalSurfacesAsToolResult(t *testing.T) {
	// When the supervisor refuses (e.g. solo mode), the tool surfaces
	// the error in the JSON output rather than as a Go error.
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	sup := agent.NewSupervisor(log, newHangingProvider(), tools.NewRegistry())
	defer sup.Shutdown()
	sup.SetMaxConcurrentChildren(0) // every spawn refused
	tool := agent.NewAgentTool(sup)
	in, _ := json.Marshal(map[string]any{
		"objective":      "x",
		"output_format":  "y",
		"tool_allowlist": []string{},
	})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("expected no Go err: %v", err)
	}
	var res struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(out, &res)
	if res.Error == "" {
		t.Errorf("expected error in output JSON, got %s", out)
	}
}

// PlanTool: cover the agent-id-empty branch (existing tests cover the
// nil-worktree + missing-title paths).
func TestPlanTool_Execute_EmptyAgentIDErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(dir, "artifacts"))
	log, err := agent.OpenSQLiteEventLog(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer log.Close()
	tool := agent.NewPlanTool("", &fakeWorktree{diff: []byte("d")}, log)
	in, _ := json.Marshal(map[string]any{"title": "x", "summary": "y"})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("empty agent ID should error")
	}
}

func TestPlanTool_Execute_NilLogErrors(t *testing.T) {
	tool := agent.NewPlanTool("a", &fakeWorktree{diff: []byte("d")}, nil)
	in, _ := json.Marshal(map[string]any{"title": "x", "summary": "y"})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("nil log should error")
	}
}

func TestPlanTool_Execute_MissingSummaryErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(dir, "artifacts"))
	log, err := agent.OpenSQLiteEventLog(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer log.Close()
	tool := agent.NewPlanTool("a", &fakeWorktree{diff: []byte("d")}, log)
	in, _ := json.Marshal(map[string]any{"title": "x"})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("missing summary should error")
	}
}

func TestPlanTool_Execute_ChangedFilesErrorBubbles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(dir, "artifacts"))
	log := openPlanLog(t)
	const id = "agent-plan-cf"
	seedPlanAgent(t, log, id)
	tool := agent.NewPlanTool(id, &fakeWorktree{
		diff:    []byte("real diff"),
		fileErr: errors.New("git wedged on changed-files"),
	}, log)
	in, _ := json.Marshal(map[string]any{"title": "x", "summary": "y"})
	_, err := tool.Execute(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "changed files") {
		t.Fatalf("want changed-files err, got %v", err)
	}
}

func TestApplyHandler_NilReceiverErrors(t *testing.T) {
	var h *agent.ApplyHandler
	if err := h.Run(context.Background()); err == nil {
		t.Fatal("nil handler should error")
	}
}

func TestApplyHandler_NilSupervisorErrors(t *testing.T) {
	h := &agent.ApplyHandler{Supervisor: nil, Log: nil}
	if err := h.Run(context.Background()); err == nil {
		t.Fatal("nil supervisor/log should error")
	}
}

func TestApplyHandler_NonResolutionEventIgnored(t *testing.T) {
	// Inject a non-approval event under the resolver id; the handler
	// should not crash and should not write an apply_outcome.
	log := openPlanLog(t)
	sup := newSupervisorForHandler(t, log)
	h := &agent.ApplyHandler{Supervisor: sup, Log: log}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	// Append a non-approval event under "user" agent id.
	_, _ = log.Append(ctx, agent.Event{
		AgentID: "user", TS: time.Now().UTC().Truncate(time.Millisecond),
		Type: agent.EvtUserMessage, Payload: []byte(`{}`),
	})
	// Give the handler a moment.
	time.Sleep(80 * time.Millisecond)
	// No outcome should appear.
	rows, err := log.DB().Query(`SELECT COUNT(*) FROM artifacts WHERE kind = ?`, agent.ApplyOutcomeKind)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("count query no rows")
	}
	var n int
	if err := rows.Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected no outcomes, got %d", n)
	}
}

// SetMode + SetFrameSubtrees on the LayeredApprover branch we hit when
// active frame is itself in the subtrees map.
func TestLayeredApprover_SetFrameSubtreesNilClearsMap(t *testing.T) {
	la := agent.NewLayeredApprover(agent.AutoApprover{}, nil, nil)
	la.SetFrameSubtrees("active", map[string]string{"a": "/x", "b": "/y"})
	la.SetFrameSubtrees("", nil) // nil map = disable
	// After clearing, a write that would otherwise be in a foreign subtree
	// should fall through to the fallback (auto-approver returns true).
	in := []byte(`{"path":"/x/foo"}`)
	if !la.ApproveToolCall("write", in) {
		t.Error("post-clear write should be allowed via fallback")
	}
}

// Ensure compile-time: providers.Provider import is used somewhere.
var _ providers.Provider = nil
