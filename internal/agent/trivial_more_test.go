package agent_test

import (
	"context"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
)

// Covers tiny declarative methods that only return a static value/error:
// Name() on tools and verifier adapters, the New constructor, the legacy
// OpenEventLog stub, the no-op Roster, the StartHeartbeat surface, and the
// budget Elapsed accessor. These are tiny but contribute to coverage in
// internal/agent; together they erase 8 of the 0% functions.

func TestAgent_New(t *testing.T) {
	a, err := agent.New(agent.Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a == nil {
		t.Fatal("New returned nil agent")
	}
}

func TestAgentTool_Name(t *testing.T) {
	tool := agent.NewAgentTool(nil)
	if tool.Name() != "agent" {
		t.Errorf("Name = %q want agent", tool.Name())
	}
}

func TestPlanTool_NameDescriptionSchema(t *testing.T) {
	tool := agent.NewPlanTool("a", nil, nil)
	if tool.Name() != "plan" {
		t.Errorf("Name = %q want plan", tool.Name())
	}
	if tool.Description() == "" {
		t.Errorf("Description should be non-empty")
	}
	if len(tool.Schema()) == 0 {
		t.Errorf("Schema should be non-empty")
	}
}

func TestCompilerVerifier_Name(t *testing.T) {
	v := agent.NewCompilerVerifier()
	if v.Name() != "compiler" {
		t.Errorf("Name = %q want compiler", v.Name())
	}
}

func TestTestRunnerVerifier_Name(t *testing.T) {
	v := agent.NewTestRunnerVerifier()
	if v.Name() != "tests" {
		t.Errorf("Name = %q want tests", v.Name())
	}
}

func TestURLRefetcherVerifier_Name(t *testing.T) {
	v := agent.NewURLRefetcherVerifier()
	if v.Name() != "urls" {
		t.Errorf("Name = %q want urls", v.Name())
	}
}

func TestOpenEventLog_StubReturnsError(t *testing.T) {
	// The legacy OpenEventLog is a not-implemented stub; it should
	// always return an error. Exercising it nails the 0% coverage line.
	if _, err := agent.OpenEventLog("/tmp/whatever"); err == nil {
		t.Fatalf("OpenEventLog should return an error")
	}
}

func TestRealClock_Now(t *testing.T) {
	// RealClock.Now should return a non-zero time.
	clk := agent.RealClock{}
	if clk.Now().IsZero() {
		t.Errorf("RealClock.Now should be non-zero")
	}
}

func TestSupervisor_RosterNilByContract(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	if got := sup.Roster(); got != nil {
		t.Errorf("Roster should be nil per contract, got %v", got)
	}
}

func TestSupervisor_RunIdempotentAndNilSafe(t *testing.T) {
	// Run is safe to call on a fresh supervisor (no log).
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Run(ctx) // no-op since sweeper is nil
	sup.Run(ctx) // double-run still safe

	// And with a real log, Run starts the sweeper but Shutdown drains it
	// cleanly.
	dir := t.TempDir()
	log, err := agent.OpenStateDB(dir + "/state.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	sup2 := agent.NewSupervisor(log, nil, nil)
	sup2.Run(ctx)
	sup2.Shutdown()
}

func TestSupervisor_StartHeartbeat_NilSafeAndIdempotent(t *testing.T) {
	// nil supervisor: no panic.
	var sup *agent.Supervisor
	sup.StartHeartbeat(context.Background(), "x")

	// Real supervisor without a log = nil heartbeat: still no panic.
	s := agent.NewSupervisor(nil, nil, nil)
	defer s.Shutdown()
	s.StartHeartbeat(context.Background(), "y")

	// Real supervisor with a log: empty agentID is a no-op.
	dir := t.TempDir()
	log, err := agent.OpenStateDB(dir + "/state.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	s2 := agent.NewSupervisor(log, nil, nil)
	defer s2.Shutdown()
	s2.StartHeartbeat(context.Background(), "")
	// Calling with a real ID starts the per-agent heartbeat; calling
	// twice is the documented idempotent no-op.
	s2.StartHeartbeat(context.Background(), "alpha")
	s2.StartHeartbeat(context.Background(), "alpha")
}

func TestTracker_Elapsed_NonNegative(t *testing.T) {
	tr := agent.NewTracker(nil)
	if e := tr.Elapsed(); e < 0 {
		t.Errorf("Elapsed should be non-negative, got %v", e)
	}
}

// Ensure compile-time: providers.Provider is the contract; we don't
// actually need a Provider here, but importing it forces the package
// graph to compile.
var _ providers.Provider = nil
