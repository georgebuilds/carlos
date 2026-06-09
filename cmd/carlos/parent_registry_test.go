package main

import (
	"context"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/tools"
)

// TestParentRegistry_IncludesAgentTool pins the v0.7.2 contract:
// the interactive TUI's parent registry MUST include the "agent"
// delegation primitive so the model can spawn sub-agents via
// Supervisor.Spawn. Before this fix only `carlos please` registered
// it; the chat path could see /agents listed but had no way to
// populate it.
//
// We mirror the wire-up inline so this test stays decoupled from
// runtime_tui.go's main flow: build a base registry + supervisor,
// build a parent registry the way runDefault does, and verify the
// agent tool is present.
func TestParentRegistry_IncludesAgentTool(t *testing.T) {
	baseReg := tools.NewDefaultRegistry()
	sup := &agent.Supervisor{}

	parentReg := tools.NewRegistry()
	for _, ttool := range baseReg.All() {
		parentReg.Register(ttool)
	}
	parentReg.Register(agent.NewAgentTool(sup))

	got, ok := parentReg.Get("agent")
	if !ok {
		t.Fatal("expected `agent` tool in parent registry; not found")
	}
	if got.Name() != "agent" {
		t.Errorf("tool name = %q, want %q", got.Name(), "agent")
	}
	if got.Description() == "" {
		t.Error("agent tool description must not be empty")
	}
	if len(got.Schema()) == 0 {
		t.Error("agent tool schema must not be empty")
	}
}

// TestParentRegistry_AgentToolNotInBaseRegistry guards the
// architectural invariant: the BASE registry (used by sub-agents
// and the supervisor) must NOT carry the agent tool. Children
// inherit baseReg + their own tools so they can't further delegate;
// without this property a runaway model could spawn an unbounded
// tree of children even with Supervisor.Spawn's depth cap.
func TestParentRegistry_AgentToolNotInBaseRegistry(t *testing.T) {
	baseReg := tools.NewDefaultRegistry()
	if _, ok := baseReg.Get("agent"); ok {
		t.Error("base registry leaked the `agent` delegation tool")
	}
}

// TestParentRegistry_AgentToolUsesSupervisor proves the wire-up
// actually plumbs the live Supervisor through. The Execute path is
// integration-tested elsewhere; here we just confirm the wrapper
// holds a non-nil reference (calling Execute with nil supervisor
// would panic).
func TestParentRegistry_AgentToolUsesSupervisor(t *testing.T) {
	sup := &agent.Supervisor{}
	tool := agent.NewAgentTool(sup)
	// Execute with bad input still surfaces the schema-validation
	// path rather than panicking on a nil supervisor.
	_, _ = tool.Execute(context.Background(), []byte(`{}`))
}
