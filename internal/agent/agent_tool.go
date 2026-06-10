package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// AgentTool is the "Agent" primitive the parent model calls to delegate
// work to a sub-agent. It is the user-facing wrapper around
// Supervisor.Spawn - when the parent emits a tool_use for this tool,
// the supervisor builds a SpawnContract from the typed input, runs the
// child loop, waits for SpawnResult, and returns the child's typed
// deliverable as the tool_result.
//
// # When the model should call this
//
// When-to-delegate is **mode-driven**, not baked into this tool's
// description. The active frame's mode is rendered into the system
// prompt's Frame block by internal/agent/sysprompt.go:
//
//   - solo: model defaults to doing the work itself; spawning is opt-in
//     to user request. SpawnCapFor returns 0, so the supervisor refuses
//     every Spawn anyway.
//   - tight: model can delegate, but the supervisor caps in-flight
//     children at 1. Use it for sequential delegation.
//   - orchestrator: model is told to delegate by default for anything
//     beyond a trivial single-line edit, in parallel where the work has
//     independent parts. SpawnCapFor returns 5. The user opted in to
//     this mode; no per-turn confirmation overlay.
//
// This tool's Description() therefore stays policy-neutral: it documents
// the tool's mechanics (inputs, output shape, atomic return) and points
// the model at the Frame block for the when-to-delegate decision.
//
// # Caps + safety
//
// Supervisor.Spawn enforces depth (default 1; leaves can't spawn),
// per-parent concurrency (mode-gated via SpawnCapFor: solo=0, tight=1,
// orchestrator=5), and restart intensity. Sub-agents auto-approve their
// own tool calls (AutoApprover; Phase 4 surfaces per-child tool prompts
// in the manage roster). All of these are transparent to the model -
// it just sees a tool that delegates.
//
// # Output shape
//
// The tool returns JSON the model can read directly:
//
//	{
//	  "agent_id": "<ULID>",
//	  "final_text": "<assistant final-turn concatenated text>",
//	  "artifact_ref": {
//	    "sha256": "...", "kind": "agent_final", "path": "...", "size": ...
//	  },
//	  "error": "..."        // only present on failure
//	}
//
// final_text is the cheap-to-read summary; artifact_ref points at the
// full final-turn JSON written by spawn.go's runChild - the parent can
// follow it with a read_artifact tool (Phase 4) to inspect the entire
// turn including any tool_use blocks.
type AgentTool struct {
	sup *Supervisor
}

// NewAgentTool wires the tool to a supervisor. The supervisor is the
// one constructed via NewSupervisor(log, parentProvider, baseRegistry)
// in the parent's loop.
func NewAgentTool(s *Supervisor) *AgentTool {
	return &AgentTool{sup: s}
}

func (*AgentTool) Name() string { return "agent" }

func (*AgentTool) Description() string {
	return `Delegate a focused sub-task to a child agent. The child runs in its own context, with its own restricted tool set, and returns a single typed deliverable.

When to call this is set by the active frame's mode (shown in the Frame block of the system prompt): solo discourages delegation, tight allows one child at a time, orchestrator encourages parallel delegation for anything beyond a trivial single-line edit. Follow the Frame block; do not override it from this tool description.

Inputs:
  - objective (required): one-paragraph description of what the child must accomplish.
  - output_format (required): exact shape the child should return - be specific about fields, not just "a summary".
  - tool_allowlist (required): subset of your tool names the child may call. EMPTY allowlist = pure reasoning child, no tools. Restrict aggressively - give the child only what it needs.
  - max_turns (optional, default 25): hard cap on child's agent-loop iterations.
  - success_criteria (optional): how the child knows it's done; surfaced in the child's initial prompt.

The child returns its final assistant turn as JSON, plus an artifact_ref to the persisted full turn. You'll get the deliverable atomically - there's no streaming or mid-flight communication.`
}

// Schema is the JSON schema for the Agent tool's input - passed to the
// provider in the tools array so the model can construct valid calls.
// Schema mirrors SpawnContract: every required field is required here,
// types and descriptions match.
func (*AgentTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"objective": {
				"type": "string",
				"description": "What the child must accomplish, in one paragraph."
			},
			"output_format": {
				"type": "string",
				"description": "The exact shape the child should return. Be specific about fields/structure."
			},
			"tool_allowlist": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Subset of parent's tool names the child may call. Empty = no tools (pure reasoning)."
			},
			"max_turns": {
				"type": "integer",
				"description": "Hard cap on child agent-loop iterations (default 25)."
			},
			"success_criteria": {
				"type": "string",
				"description": "How the child knows it's done; appears in the child's initial prompt."
			}
		},
		"required": ["objective", "output_format", "tool_allowlist"]
	}`)
}

type agentToolInput struct {
	Objective       string   `json:"objective"`
	OutputFormat    string   `json:"output_format"`
	ToolAllowlist   []string `json:"tool_allowlist"`
	MaxTurns        int      `json:"max_turns,omitempty"`
	SuccessCriteria string   `json:"success_criteria,omitempty"`
}

type agentToolOutput struct {
	AgentID      string       `json:"agent_id"`
	FinalText    string       `json:"final_text"`
	ArtifactRef  *ArtifactRef `json:"artifact_ref,omitempty"`
	Error        string       `json:"error,omitempty"`
}

// Execute parses the input, calls Supervisor.Spawn with parentID == ""
// (the top-level agent IS the parent of any sub-agent it spawns;
// nested spawning is gated by depth cap), reads the SpawnResult, and
// returns the typed output as JSON.
//
// The depth cap is the safety net for accidental fan-out: if a child
// somehow gets this tool too (it shouldn't - parent must explicitly
// allowlist "agent" for the child, which the description discourages),
// the supervisor's depth check refuses with ErrSpawnDepthExceeded and
// we return that as the tool_result error. Model sees the failure and
// adapts.
//
// Sub-agent errors (depth/concurrency/restart-intensity, provider
// error, MaxIterations exceeded) are returned as tool_result text -
// NOT as Tool.Execute errors. The parent loop sees a successful tool
// call with an "error" field in the output JSON; the model decides
// how to handle.
func (t *AgentTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	if t.sup == nil {
		return nil, errors.New("agent tool: supervisor not wired")
	}
	var in agentToolInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("agent tool: parse input: %w", err)
	}
	if in.Objective == "" {
		return nil, errors.New("agent tool: objective required")
	}
	if in.OutputFormat == "" {
		return nil, errors.New("agent tool: output_format required")
	}
	contract := SpawnContract{
		Objective:       in.Objective,
		OutputFormat:    in.OutputFormat,
		ToolAllowlist:   in.ToolAllowlist,
		MaxTurns:        in.MaxTurns,
		SuccessCriteria: in.SuccessCriteria,
	}
	// parentID == "" - the top-level agent in this process owns the
	// spawn. Future Phase 4 work threads a real parentID when the
	// caller is itself a sub-agent (deep chains gated by depth cap).
	sub, resultCh, err := t.sup.Spawn(ctx, "", contract)
	if err != nil {
		// Cap rejections (depth/concurrency/restart) surface to the
		// model as a tool_result, not as an infra error - model can
		// adapt (e.g., "I'll do this myself").
		out := agentToolOutput{Error: err.Error()}
		return mustMarshal(out), nil
	}
	// Wait on the result. ctx cancel cuts both this read and the
	// child's inner loop (Spawn passes a derived ctx).
	select {
	case res, ok := <-resultCh:
		if !ok {
			return nil, errors.New("agent tool: result channel closed without value")
		}
		out := agentToolOutput{
			AgentID:   sub.ID,
			FinalText: extractFinalText(res.FinalTurn),
		}
		if res.FinalArtifact.SHA256 != "" {
			ref := res.FinalArtifact
			out.ArtifactRef = &ref
		}
		if res.Err != nil {
			out.Error = res.Err.Error()
		}
		return mustMarshal(out), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// extractFinalText concatenates text blocks from the child's final
// assistant message. Tool_use blocks are skipped (they're internal to
// the child's loop, not its deliverable). Returns empty string if the
// turn had no text blocks - caller should also read the artifact ref
// for the full structured turn in that case.
func extractFinalText(m providers.Message) string {
	if len(m.Content) == 0 {
		return ""
	}
	var out []byte
	for _, b := range m.Content {
		if b.Kind == "text" || b.Kind == "" {
			if len(out) > 0 {
				out = append(out, '\n')
			}
			out = append(out, []byte(b.Text)...)
		}
	}
	return string(out)
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// Programmer error: every field above is JSON-serializable.
		// Panic is appropriate - this isn't a user-input failure.
		panic(fmt.Sprintf("agent tool: marshal output: %v", err))
	}
	return b
}

// Compile-time check.
var _ tools.Tool = (*AgentTool)(nil)
