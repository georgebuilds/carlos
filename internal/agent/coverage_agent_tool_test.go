package agent_test

// Coverage for AgentTool.Execute branches the happy-path test doesn't
// reach: malformed JSON input and context cancellation while waiting on
// the spawned child's result channel.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers/fake"
	"github.com/georgebuilds/carlos/internal/tools"
)

// newHangingSupervisor builds a Supervisor whose provider blocks forever,
// so a spawned child stays in `running` until its ctx is cancelled.
func newHangingSupervisor(t *testing.T) (*agent.Supervisor, func()) {
	t.Helper()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open eventlog: %v", err)
	}
	sup := agent.NewSupervisor(log, newHangingProvider(), tools.NewRegistry())
	return sup, func() {
		sup.Shutdown()
		_ = log.Close()
	}
}

func TestAgentTool_MalformedInputInfraError(t *testing.T) {
	sup, cleanup := newTestSupervisor(t, fake.Script{})
	defer cleanup()
	tool := agent.NewAgentTool(sup)
	if _, err := tool.Execute(context.Background(), []byte("not json {")); err == nil {
		t.Fatal("malformed input should return an infra error")
	}
}

// TestAgentTool_CtxCancelWhileWaiting spawns a child whose provider hangs
// forever, then cancels the context so Execute returns via the ctx.Done
// branch.
func TestAgentTool_CtxCancelWhileWaiting(t *testing.T) {
	// A hanging provider keeps the child in `running` so Execute blocks
	// on the result channel until we cancel.
	sup, cleanup := newHangingSupervisor(t)
	defer cleanup()
	tool := agent.NewAgentTool(sup)
	in, _ := json.Marshal(map[string]any{
		"objective": "hang", "output_format": "x", "tool_allowlist": []string{},
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, in)
		errCh <- err
	}()
	// Give the spawn a moment to start, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Execute should return ctx error on cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not return after ctx cancel")
	}
}
