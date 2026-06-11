package agent_test

// Coverage for PlanTool.Execute validation + storage-error branches not
// reached by plan_tool_test.go:
//
//   - malformed JSON input → parse error.
//   - missing summary → validation error.
//   - ChangedFiles failure bubbles.
//   - a closed event log makes the diff-artifact write fail.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

func TestPlanTool_MalformedInputErrors(t *testing.T) {
	log := openPlanLog(t)
	const id = "agent-plan-malformed"
	seedPlanAgent(t, log, id)
	tool := agent.NewPlanTool(id, &fakeWorktree{diff: []byte("x")}, log)
	if _, err := tool.Execute(context.Background(), []byte("not json {")); err == nil {
		t.Fatal("malformed input should error")
	}
}

func TestPlanTool_MissingSummaryErrors(t *testing.T) {
	log := openPlanLog(t)
	const id = "agent-plan-nosummary"
	seedPlanAgent(t, log, id)
	tool := agent.NewPlanTool(id, &fakeWorktree{diff: []byte("x")}, log)
	in, _ := json.Marshal(map[string]any{"title": "has title"})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("missing summary should error")
	}
}

func TestPlanTool_ChangedFilesErrorBubbles(t *testing.T) {
	log := openPlanLog(t)
	const id = "agent-plan-cferr"
	seedPlanAgent(t, log, id)
	tool := agent.NewPlanTool(id, &fakeWorktree{
		diff:    []byte("diff --git a b\n"),
		fileErr: errors.New("git status wedged"),
	}, log)
	in, _ := json.Marshal(map[string]any{"title": "x", "summary": "y"})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("ChangedFiles failure should bubble")
	}
}

// TestPlanTool_WriteArtifactFailsOnClosedLog closes the log so the
// WriteArtifact insert for the diff blob fails, covering the
// write-diff-artifact error branch.
func TestPlanTool_WriteArtifactFailsOnClosedLog(t *testing.T) {
	log := openPlanLog(t)
	const id = "agent-plan-writefail"
	seedPlanAgent(t, log, id)
	tool := agent.NewPlanTool(id, &fakeWorktree{
		diff:  []byte("diff --git a b\n"),
		files: []string{"a"},
	}, log)
	if err := log.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	in, _ := json.Marshal(map[string]any{"title": "x", "summary": "y"})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("WriteArtifact on a closed log should error")
	}
}
