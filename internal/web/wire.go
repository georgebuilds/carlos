// Package web hosts the "carlos web" HTTP + SSE server: a localhost
// projection surface over the agent event log, mirroring what the chat
// TUI is to the same log. The browser appends EvtUserMessage events and
// streams everything else back out.
//
// The package depends only on internal/agent (stable types) plus a
// consumer-defined Backend seam (backend.go) that cmd/carlos implements
// for the interactive bits. Nothing in internal/agent imports web; there
// is no cycle. See the vault spec (web-spec.md) §8 for the frozen wire
// vocabulary this file encodes and web-implementation-plan.md for the
// build plan.
package web

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/tui/chatglue"
)

// WireEvent is the single JSON shape the front-end consumes (spec §8).
// carlos's event types are the superset source; this is the normalized
// projection. Seq is absent (omitempty) on ephemeral events (delta,
// approval_request, children) and present on replayable persisted ones.
type WireEvent struct {
	Seq    int64  `json:"seq,omitempty"`
	Thread string `json:"thread"`
	TS     string `json:"ts"`
	Kind   string `json:"kind"`
	Data   any    `json:"data"`
}

// ThreadSummary is the list/detail shape (spec §8.2). State describes the
// agent; Attached describes whether THIS process is interactively driving
// it - distinct concepts. GroupID is the web-only roster grouping overlay
// (additive, absent means ungrouped). Frame resolves at attach ("" when
// detached). Backend is always "carlos" in v1 (D5/D6 forward-compat).
type ThreadSummary struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Model        string          `json:"model"`
	State        string          `json:"state"`
	Attached     bool            `json:"attached"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	Preview      string          `json:"preview"`
	UserMsgs     int             `json:"user_msgs"`
	Frame        string          `json:"frame"`
	Backend      string          `json:"backend"`
	GroupID      *string         `json:"group_id,omitempty"`
	Capabilities map[string]bool `json:"capabilities"`
}

// wireState maps an agent.State to the underscore-form wire string the
// front-end expects (spec §8.1). This is deliberately NOT State.String()
// - that yields dash-form ("awaiting-input", "paused-by-user"), which the
// wire vocabulary does not use. Keep the two mappings in lockstep here.
func wireState(s agent.State) string {
	switch s {
	case agent.StateSpawning:
		return "spawning"
	case agent.StateQueued:
		return "queued"
	case agent.StateRunning:
		return "running"
	case agent.StateAwaitingInput:
		return "awaiting_input"
	case agent.StateBlocked:
		return "blocked"
	case agent.StatePausedByUser:
		return "paused"
	case agent.StateCompacting:
		return "compacting"
	case agent.StateCancelling:
		return "cancelling"
	case agent.StateDone:
		return "done"
	case agent.StateFailed:
		return "failed"
	case agent.StateOrphaned:
		return "orphaned"
	default:
		return "running"
	}
}

// rfc3339 renders an event timestamp in the millisecond-precision RFC3339
// form the wire uses (matching the spec §8 example).
func rfc3339(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// rawInput passes a tool's raw JSON input through to the browser as a
// nested object rather than a re-encoded string. Empty input renders as
// "" so the front-end can fall back to name-only.
func rawInput(b []byte) any {
	if len(b) == 0 {
		return ""
	}
	return json.RawMessage(b)
}

// passthrough forwards an opaque payload (usershell start/end) as nested
// JSON, defaulting to an empty object when absent.
func passthrough(b []byte) any {
	if len(b) == 0 {
		return map[string]any{}
	}
	return json.RawMessage(b)
}

// eventToWire converts a persisted agent.Event into its normalized
// WireEvent. The bool is false for event types deliberately NOT forwarded
// to the browser (provider_call, token_usage, heartbeat, gateway_*,
// steering, artifact_ref, and the artifact-approval queue events) - they
// are noise at the UI altitude (spec §8 notes). Callers skip those.
func eventToWire(ev agent.Event) (WireEvent, bool) {
	w := WireEvent{Seq: ev.Seq, Thread: ev.AgentID, TS: rfc3339(ev.TS)}
	switch ev.Type {
	case agent.EvtUserMessage:
		var p agent.MessagePayload
		_ = json.Unmarshal(ev.Payload, &p)
		w.Kind = "user_message"
		w.Data = map[string]any{"text": p.Text}

	case agent.EvtAssistantMessage:
		var p agent.MessagePayload
		_ = json.Unmarshal(ev.Payload, &p)
		text, isErr := p.Text, false
		if strings.HasPrefix(text, chatglue.ErrorEventPrefix) {
			isErr = true
			text = strings.TrimPrefix(text, chatglue.ErrorEventPrefix)
		}
		w.Kind = "assistant_message"
		w.Data = map[string]any{"text": text, "error": isErr}

	case agent.EvtToolCall:
		var p agent.ToolCall
		_ = json.Unmarshal(ev.Payload, &p)
		w.Kind = "tool_call"
		w.Data = map[string]any{"name": p.Name, "input": rawInput(p.Input)}

	case agent.EvtToolResult:
		var p agent.ToolResult
		_ = json.Unmarshal(ev.Payload, &p)
		w.Kind = "tool_result"
		w.Data = map[string]any{
			"name":           p.Name,
			"output_preview": string(p.Output),
			"is_error":       p.IsError,
			// ToolResult carries no truncated flag; derive it. The
			// persister caps at ToolResultPreviewCap, so a result at
			// the cap was (almost certainly) clipped. Honest match to
			// the TUI, which shows the same preview (spec F12, D-D).
			"truncated": len(p.Output) >= chatglue.ToolResultPreviewCap,
		}

	case agent.EvtStateChange:
		var p agent.StateChangePayload
		_ = json.Unmarshal(ev.Payload, &p)
		st := ""
		switch {
		case p.To != nil:
			st = wireState(*p.To)
		case p.Created != nil:
			st = "spawning"
		}
		if st == "" {
			return w, false
		}
		w.Kind = "state"
		w.Data = map[string]any{"state": st}

	case agent.EvtSessionReset:
		w.Kind = "session_reset"
		w.Data = map[string]any{}

	case agent.EvtResearchPhase:
		var p agent.ResearchPhasePayload
		_ = json.Unmarshal(ev.Payload, &p)
		w.Kind = "research_phase"
		w.Data = map[string]any{
			"phase":      p.Phase,
			"done":       p.Done,
			"elapsed_ms": p.Elapsed.Milliseconds(),
			"err":        p.Err,
		}

	case agent.EvtUserShellStart:
		w.Kind = "shell_start"
		w.Data = passthrough(ev.Payload)

	case agent.EvtUserShellEnd:
		w.Kind = "shell_end"
		w.Data = passthrough(ev.Payload)

	default:
		return w, false
	}
	return w, true
}
