package agent

import (
	"context"
	"errors"
	"time"
)

type EventType string

const (
	EvtStateChange  EventType = "state_change"
	EvtProviderCall EventType = "provider_call"
	EvtToolCall     EventType = "tool_call"
	EvtToolResult   EventType = "tool_result"
	EvtTokenUsage   EventType = "token_usage"
	EvtUserMessage EventType = "user_message"
	// MessagePayload — see below: declared after the EventType block.
	// EvtAssistantMessage is the sealed text of one assistant turn,
	// appended after the provider stream completes. Live tokens are
	// surfaced via chat.TextSource during the stream; this event is the
	// persistent record the transcript replays on reload. Payload shape
	// mirrors EvtUserMessage (a single Text field) — see chat package.
	EvtAssistantMessage EventType = "assistant_message"
	// EvtSessionReset marks a conversational fresh-start. Producers
	// (`/clear` in chat today; a future `/compact` later) append one
	// to signal "everything before this is no longer part of the
	// active conversation". Projections (chat transcript + chatglue
	// history) drop accumulated state when they encounter one. The
	// pre-reset events stay in the log for audit; they just don't
	// feed the model on the next turn.
	EvtSessionReset EventType = "session_reset"
	EvtSteering     EventType = "steering"
	EvtArtifactRef  EventType = "artifact_ref"
	EvtHeartbeat    EventType = "heartbeat"

	// Approval-queue events. ProposeApproval marks an artifact as
	// awaiting human decision; Accept / Reject close it. The "pending"
	// queue is the set of artifacts with a Propose event and no
	// subsequent Accept/Reject — derived at query time. See
	// internal/agent/approval.go.
	EvtApprovalProposed EventType = "approval_proposed"
	EvtApprovalAccepted EventType = "approval_accepted"
	EvtApprovalRejected EventType = "approval_rejected"

	// EvtResearchPhase signals a phase transition in a research sub-agent
	// (slice 11d). Payload shape: ResearchPhasePayload. Manage/chat
	// renderers consume these to surface live progress without polling.
	// The research engine emits one start + one done event per phase
	// (decompose, search, fetch, read, synthesize, verify); the spawn
	// helper wires the engine's OnPhaseStart/OnPhaseDone callbacks to
	// append these to the agent's event stream.
	EvtResearchPhase EventType = "research_phase"

	// Gateway events — the messaging-broker integration. The broker
	// owns the payload shapes (see internal/gateway/events.go); the
	// constants live here so the event log knows the type strings and
	// projections can filter for them without a circular import.
	//
	// EvtGatewayOutbound is written BEFORE the network call so a crash
	// mid-send produces a "we tried, status unknown" row; the broker
	// reconciles on restart.
	EvtGatewayOutbound EventType = "gateway_outbound"
	// EvtGatewayInbound is written after dedupe + validation, before
	// downstream processing. Matches the in-TUI approval click shape so
	// the approval-queue resolver doesn't care which surface produced
	// the decision.
	EvtGatewayInbound EventType = "gateway_inbound"

	// User-shell events — Phase U "!"-prefix feature. The usershell
	// Manager owns the payload shapes (internal/usershell/events.go);
	// the constants live here for the same reason the gateway pair
	// does — the event log + projections need to filter on them
	// without a circular import.
	//
	// EvtUserShellStart is written when a job enters the running state
	// (foreground OR background). Carries command, cwd, and start
	// timestamp so a crash mid-run leaves a recoverable "we tried" row.
	EvtUserShellStart EventType = "user_shell_start"
	// EvtUserShellEnd is written when a job leaves running for a
	// terminal state. Carries exit code, duration, cancelled/bg flags,
	// inline-capped output for the model context, AND an artifact ref
	// to the full output blob.
	EvtUserShellEnd EventType = "user_shell_end"
)

type Event struct {
	Seq     int64
	AgentID string
	TS      time.Time
	Type    EventType
	Payload []byte
}

// MessagePayload is the canonical on-the-wire shape for EvtUserMessage,
// EvtAssistantMessage, and EvtSteering events. Single Text field today;
// future fields (attachments, citations) can be added without breaking
// existing rows because json.Unmarshal ignores unknown fields.
type MessagePayload struct {
	Text string `json:"text"`
}

// ResearchPhasePayload is the EvtResearchPhase payload (slice 11d).
// Phase values match research package phase names ("decompose",
// "search", "fetch", "read", "synthesize", "verify"). Done=true
// signals the engine finished the phase (start events carry Done=false
// + Elapsed=0); Err carries any failure reason on the done event.
// SubQuery is reserved for future fine-grained progress inside phases
// that iterate (e.g. per-sub-query search/read) — the spawn helper
// leaves it empty for now, sticking to phase-boundary granularity.
type ResearchPhasePayload struct {
	Phase    string        `json:"phase"`
	SubQuery string        `json:"sub_query,omitempty"`
	Elapsed  time.Duration `json:"elapsed_ms"`
	Done     bool          `json:"done,omitempty"`
	Err      string        `json:"err,omitempty"`
}

type EventLog interface {
	Append(ctx context.Context, ev Event) (int64, error)
	Read(ctx context.Context, agentID string, fromSeq int64) ([]Event, error)
	Subscribe(agentID string) (<-chan Event, func(), error)
	Close() error
}

type Artifact struct {
	ID        string
	AgentID   string
	Path      string
	Kind      string
	SHA256    string
	CreatedAt time.Time
}

func OpenEventLog(path string) (EventLog, error) {
	return nil, errors.New("eventlog: not implemented (SQLite WAL setup pending)")
}
