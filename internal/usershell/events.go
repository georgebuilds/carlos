package usershell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// EventAgentID is the synthetic agent_id under which user-shell
// events are written. Distinct from any real agent id so projection
// scans can filter cleanly, and from "user" (the approval-queue
// resolver id) and "gateway" so the audit trail reads cleanly.
const EventAgentID = "user-shell"

// MaxInlineOutput caps how much output we embed in the End payload
// for the model context. Mirrors codex's MAX_METADATA_LENGTH and
// keeps the model's context window from being eaten by a chatty
// command. The full transcript lives in the artifact store; the End
// event's OutputRef points there for the TUI to load on demand.
const MaxInlineOutput = 30_000

// StartPayload is the JSON body of EvtUserShellStart. Written when
// a Job enters the running state - foreground OR background.
type StartPayload struct {
	JobID      string    `json:"job_id"`
	Command    string    `json:"command"`
	Cwd        string    `json:"cwd"`
	Mode       string    `json:"mode"`
	Background bool      `json:"background,omitempty"`
	StartedAt  time.Time `json:"started_at"`
}

// EndPayload is the JSON body of EvtUserShellEnd. Written when a Job
// leaves running for any terminal state. Carries enough for the
// model-context projection AND enough for the manage view to render
// the final state without re-reading the artifact.
type EndPayload struct {
	JobID        string        `json:"job_id"`
	ExitCode     int           `json:"exit_code"`
	Duration     time.Duration `json:"duration_ms"`
	Cancelled    bool          `json:"cancelled,omitempty"`
	Backgrounded bool          `json:"backgrounded,omitempty"`
	FailErrMsg   string        `json:"fail_err,omitempty"`

	// OutputInline is the captured output truncated to MaxInlineOutput
	// bytes. The model sees this directly in the projection.
	// TruncatedBytes is how many bytes were dropped (zero if the full
	// output fit).
	OutputInline   string `json:"output_inline,omitempty"`
	TruncatedBytes int    `json:"truncated_bytes,omitempty"`

	// OutputPath is the on-disk path to the full output log. Empty
	// when there was no output to persist (silent commands). Lives
	// under <OutputDir>/<job-id>.log - see Options.OutputDir. The TUI
	// reads this on-demand when the user expands a job's detail view.
	//
	// We persist to a file rather than the agent artifact store
	// because user-shell output is user-authored, not agent-authored
	// - the artifact store's FK is to the agents table, and inventing
	// a synthetic "user-shell" agent row would muddy that schema.
	OutputPath string `json:"output_path,omitempty"`
}

// AppendStart writes the start event under EventAgentID. Returns the
// committed event seq for callers that want to wait/observe.
func AppendStart(ctx context.Context, log *agent.SQLiteEventLog, p StartPayload) (int64, error) {
	if log == nil {
		return 0, errors.New("usershell events: nil log")
	}
	if p.JobID == "" {
		return 0, errors.New("usershell events: start payload missing job_id")
	}
	if p.Command == "" {
		return 0, errors.New("usershell events: start payload missing command")
	}
	body, err := json.Marshal(p)
	if err != nil {
		return 0, fmt.Errorf("usershell events: marshal start: %w", err)
	}
	return log.Append(ctx, agent.Event{
		AgentID: EventAgentID,
		TS:      time.Now().UTC().Truncate(time.Millisecond),
		Type:    agent.EvtUserShellStart,
		Payload: body,
	})
}

// AppendEnd writes the end event under EventAgentID.
func AppendEnd(ctx context.Context, log *agent.SQLiteEventLog, p EndPayload) (int64, error) {
	if log == nil {
		return 0, errors.New("usershell events: nil log")
	}
	if p.JobID == "" {
		return 0, errors.New("usershell events: end payload missing job_id")
	}
	body, err := json.Marshal(p)
	if err != nil {
		return 0, fmt.Errorf("usershell events: marshal end: %w", err)
	}
	return log.Append(ctx, agent.Event{
		AgentID: EventAgentID,
		TS:      time.Now().UTC().Truncate(time.Millisecond),
		Type:    agent.EvtUserShellEnd,
		Payload: body,
	})
}

// DecodeStartPayload is the symmetric helper for projection consumers
// (chatglue, manage view, audit tools). Returns a descriptive error
// on a corrupted row rather than panicking - matches the gateway
// pattern.
func DecodeStartPayload(raw []byte) (StartPayload, error) {
	var p StartPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return StartPayload{}, fmt.Errorf("usershell events: decode start: %w", err)
	}
	return p, nil
}

// DecodeEndPayload is the end-event analogue of DecodeStartPayload.
func DecodeEndPayload(raw []byte) (EndPayload, error) {
	var p EndPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return EndPayload{}, fmt.Errorf("usershell events: decode end: %w", err)
	}
	return p, nil
}

// TruncateForInline returns the output trimmed to MaxInlineOutput
// bytes from the END (so we keep the last MaxInlineOutput bytes -
// usually the most relevant for cargo test output, error messages,
// build failures), plus the number of bytes that were dropped from
// the front. A zero-byte drop means the input fit.
//
// Keeping the tail (not the head) is the right call for the model
// context: the head of a long log is usually "starting…", "compiling
// crate 1/200…" - boilerplate. The tail is the result.
func TruncateForInline(output string) (inline string, dropped int) {
	if len(output) <= MaxInlineOutput {
		return output, 0
	}
	return output[len(output)-MaxInlineOutput:], len(output) - MaxInlineOutput
}
