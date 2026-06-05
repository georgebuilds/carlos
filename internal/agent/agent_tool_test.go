package agent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
	"github.com/georgebuilds/carlos/internal/tools"
)

// newTestSupervisor builds a Supervisor backed by a temp SQLite log,
// a FakeProvider that emits the given script on the first Stream
// call, and an empty tool registry. Used by Agent-tool tests that
// want a real spawn round-trip — the child does one Stream call
// (text + end_turn), so a single-script fake is sufficient here.
func newTestSupervisor(t *testing.T, script fake.Script) (*agent.Supervisor, func()) {
	t.Helper()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("open eventlog: %v", err)
	}
	p := fake.New("test", script)
	sup := agent.NewSupervisor(log, p, tools.NewRegistry())
	return sup, func() {
		sup.Shutdown()
		_ = log.Close()
	}
}

// TestAgentTool_HappyPath spawns a sub-agent, waits for its end_turn,
// and asserts the tool returns the assistant text + an artifact ref.
func TestAgentTool_HappyPath(t *testing.T) {
	script := fake.Script{
		{Kind: providers.EventTextDelta, Text: "Here's what I found: "},
		{Kind: providers.EventTextDelta, Text: "three things."},
		{Kind: providers.EventStopReason, Stop: "end_turn"},
	}
	sup, cleanup := newTestSupervisor(t, script)
	defer cleanup()

	tool := agent.NewAgentTool(sup)
	in, _ := json.Marshal(map[string]any{
		"objective":      "find three things",
		"output_format":  "a short list",
		"tool_allowlist": []string{},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := tool.Execute(ctx, in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result struct {
		AgentID     string             `json:"agent_id"`
		FinalText   string             `json:"final_text"`
		ArtifactRef *agent.ArtifactRef `json:"artifact_ref"`
		Error       string             `json:"error"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, out)
	}
	if result.Error != "" {
		t.Errorf("unexpected error: %s", result.Error)
	}
	if !strings.Contains(result.FinalText, "three things") {
		t.Errorf("final_text missing payload: %q", result.FinalText)
	}
	if result.AgentID == "" {
		t.Error("agent_id empty")
	}
	if result.ArtifactRef == nil || result.ArtifactRef.SHA256 == "" {
		t.Error("artifact_ref missing — spawn.go didn't write the final-turn artifact")
	}
}

// TestAgentTool_MissingObjectiveReturnsInfraError asserts Execute
// returns an infra error (not a tool_result) when required fields are
// missing — the parent model gets a clear "your call was malformed"
// signal rather than a silent failure.
func TestAgentTool_MissingObjectiveReturnsInfraError(t *testing.T) {
	sup, cleanup := newTestSupervisor(t, fake.Script{})
	defer cleanup()
	tool := agent.NewAgentTool(sup)
	cases := []map[string]any{
		{"output_format": "x", "tool_allowlist": []string{}},                  // no objective
		{"objective": "x", "tool_allowlist": []string{}},                      // no output_format
	}
	for i, c := range cases {
		in, _ := json.Marshal(c)
		if _, err := tool.Execute(context.Background(), in); err == nil {
			t.Errorf("case %d: expected validation error, got nil", i)
		}
	}
}

// TestAgentTool_SchemaWellFormed makes sure the schema parses and
// includes the required fields the description references.
func TestAgentTool_SchemaWellFormed(t *testing.T) {
	tool := agent.NewAgentTool(nil) // schema doesn't need supervisor
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema missing properties")
	}
	for _, field := range []string{"objective", "output_format", "tool_allowlist", "max_turns", "success_criteria"} {
		if _, ok := props[field]; !ok {
			t.Errorf("schema missing property %q", field)
		}
	}
	req, ok := schema["required"].([]any)
	if !ok || len(req) < 3 {
		t.Errorf("schema required: %v", schema["required"])
	}
}

// TestAgentTool_DescriptionStressesSingleAgentDefault ensures the
// description carries the load-bearing "single-agent by default"
// guidance the SPEC commits to. If a future edit drops it, this test
// flags the regression.
func TestAgentTool_DescriptionStressesSingleAgentDefault(t *testing.T) {
	d := (&agent.AgentTool{}).Description()
	for _, must := range []string{
		"ONLY",       // gating language
		"read-heavy", // when-to-delegate criterion
		"NETS NEGATIVE", // the empirical anti-recommendation
		"Default to doing it yourself",
	} {
		if !strings.Contains(d, must) {
			t.Errorf("description missing required guidance phrase: %q", must)
		}
	}
}

// TestAgentTool_NilSupervisorReturnsInfraError guards against the
// embarrassing case of a caller wiring the tool without wiring the
// supervisor first.
func TestAgentTool_NilSupervisorReturnsInfraError(t *testing.T) {
	tool := agent.NewAgentTool(nil)
	in, _ := json.Marshal(map[string]any{
		"objective": "x", "output_format": "y", "tool_allowlist": []string{},
	})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Error("expected error on nil supervisor")
	}
}
