package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// AgentRow is the in-memory shape of one row of the `agents` projection
// table. Kept structurally identical to the design schema; the bit-
// identical check in the SoT proof compares serialized rows.
type AgentRow struct {
	ID              string
	ParentID        string // empty string for root
	RootID          string
	State           State
	Attempt         int
	Title           string
	Model           string
	TokensIn        int64
	TokensOut       int64
	CostCents       int64
	ToolCalls       int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastHeartbeatAt time.Time
}

// Projection is the in-memory roster - the read model the TUI binds to.
// Fully reconstructable by replaying the events table.
type Projection struct {
	rows map[string]*AgentRow
}

func NewProjection() *Projection {
	return &Projection{rows: map[string]*AgentRow{}}
}

// Get returns a copy of the row (so callers can't mutate projection state).
func (p *Projection) Get(id string) (AgentRow, bool) {
	r, ok := p.rows[id]
	if !ok {
		return AgentRow{}, false
	}
	return *r, true
}

// Snapshot returns all rows in deterministic order (sorted by id) - the
// shape used by the bit-identical comparison in the SoT proof.
func (p *Projection) Snapshot() []AgentRow {
	out := make([]AgentRow, 0, len(p.rows))
	for _, r := range p.rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CanonicalJSON serializes the projection in a stable form for byte-level
// equality checks. This is the "bit-identical" oracle used by the SoT proof.
func (p *Projection) CanonicalJSON() ([]byte, error) {
	snap := p.Snapshot()
	return json.Marshal(snap)
}

// AgentCreated carries the immutable fields a freshly-spawned agent
// needs to land in the projection. It is embedded in the `created` variant
// of a StateChangePayload, never sent as a standalone payload.
type AgentCreated struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	RootID   string `json:"root_id"`
	Title    string `json:"title"`
	Model    string `json:"model"`
}

// StateChangeKind discriminates the two shapes a state_change event can
// carry: the initial spawn ("created") that lands an agent in the
// projection, and a subsequent transition ("transition") that updates the
// row's state field. Encoded as a string in JSON so adding a new variant
// later (e.g. "retry" for a fresh attempt of a failed agent) is a
// non-breaking schema extension.
type StateChangeKind string

const (
	StateChangeCreated    StateChangeKind = "created"
	StateChangeTransition StateChangeKind = "transition"
)

// StateChangePayload is the formal wire shape for EvtStateChange events.
// Replaces the ad-hoc {Created?, To?} probe the preflight used. The Kind
// field is required at the unmarshal boundary; readers reject payloads
// missing it as schema drift (preflight AMEND for slice 1c).
type StateChangePayload struct {
	Kind    StateChangeKind `json:"kind"`
	Created *AgentCreated   `json:"created,omitempty"`
	To      *State          `json:"to,omitempty"`
}

// NewStateChangeCreated returns a JSON-marshalled state_change payload for
// the initial spawn event. Callers append this as the first event for an
// agent_id; subsequent events use NewStateChangeTransition.
func NewStateChangeCreated(c AgentCreated) ([]byte, error) {
	return json.Marshal(StateChangePayload{
		Kind:    StateChangeCreated,
		Created: &c,
	})
}

// NewStateChangeTransition returns a JSON-marshalled state_change payload
// for a transition. Caller has already validated the transition via
// Transition(); this just records the destination state.
func NewStateChangeTransition(to State) ([]byte, error) {
	return json.Marshal(StateChangePayload{
		Kind: StateChangeTransition,
		To:   &to,
	})
}

type TokenUsage struct {
	DeltaIn   int64 `json:"delta_in"`
	DeltaOut  int64 `json:"delta_out"`
	DeltaCost int64 `json:"delta_cost_cents"`
}

type ToolCall struct {
	Name string `json:"name"`
	// Input is the raw JSON tool input the model emitted. Stored so
	// chat transcripts can render WHAT the model asked for (e.g. the
	// bash command, the file path being read), not just the tool name.
	// Optional - older events written before this field existed
	// unmarshal with an empty Input and the renderer falls back to
	// name-only.
	Input []byte `json:"input,omitempty"`
}

// ToolResultPreviewCap is the max payload size persisted for a single
// tool_result event - by chatglue for the chat thread's own tools and
// by runChild for a sub-agent's tools. Larger outputs reach the model
// in full inside agent.Run; the persisted event just carries a preview
// the transcript surfaces render without bloating the log.
// chatglue.ToolResultPreviewCap aliases this value so the two write
// paths can never drift.
const ToolResultPreviewCap = 2048

// ToolResult is the on-the-wire payload for EvtToolResult events. Body
// caps at ToolResultPreviewCap so a multi-MiB grep result doesn't
// bloat the log; the model still saw the full output inside the
// agent.Run loop. IsError is true when the tool surfaced an error
// (tool returned err OR was rejected by the approver).
type ToolResult struct {
	Name    string `json:"name"`
	Output  []byte `json:"output,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

type Heartbeat struct{}

// Apply mutates the projection in response to a single event. Errors are
// returned for unknown event types, malformed payloads, or schema drift
// (e.g. a state_change with no Kind discriminator), so the replay
// validator catches divergence early.
//
// `ev.TS` is normalized to UTC at the read boundary in
// SQLiteEventLog.Read; callers feeding events from elsewhere should
// normalize before Apply or projection rows will drift across TZ changes.
func (p *Projection) Apply(ev Event) error {
	switch ev.Type {
	case EvtStateChange:
		var pl StateChangePayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return fmt.Errorf("projection: state_change unmarshal: %w", err)
		}
		switch pl.Kind {
		case StateChangeCreated:
			if pl.Created == nil {
				return fmt.Errorf("projection: state_change kind=created without created payload")
			}
			if _, ok := p.rows[ev.AgentID]; ok {
				return fmt.Errorf("projection: state_change kind=created for already-known agent %q", ev.AgentID)
			}
			c := pl.Created
			p.rows[ev.AgentID] = &AgentRow{
				ID:              c.ID,
				ParentID:        c.ParentID,
				RootID:          c.RootID,
				Title:           c.Title,
				Model:           c.Model,
				State:           StateSpawning,
				Attempt:         1,
				CreatedAt:       ev.TS,
				UpdatedAt:       ev.TS,
				LastHeartbeatAt: ev.TS,
			}
		case StateChangeTransition:
			row, ok := p.rows[ev.AgentID]
			if !ok {
				return fmt.Errorf("projection: state_change kind=transition for unknown agent %q", ev.AgentID)
			}
			if pl.To == nil {
				return fmt.Errorf("projection: state_change kind=transition without `to` field")
			}
			row.State = *pl.To
			row.UpdatedAt = ev.TS
		case "":
			return fmt.Errorf("projection: state_change missing required `kind` discriminator (schema drift?)")
		default:
			return fmt.Errorf("projection: state_change unknown kind %q", pl.Kind)
		}
	case EvtTokenUsage:
		row, ok := p.rows[ev.AgentID]
		if !ok {
			return fmt.Errorf("projection: token_usage for unknown agent %q", ev.AgentID)
		}
		var t TokenUsage
		if err := json.Unmarshal(ev.Payload, &t); err != nil {
			return fmt.Errorf("projection: token_usage unmarshal: %w", err)
		}
		row.TokensIn += t.DeltaIn
		row.TokensOut += t.DeltaOut
		row.CostCents += t.DeltaCost
		row.UpdatedAt = ev.TS
	case EvtToolCall:
		row, ok := p.rows[ev.AgentID]
		if !ok {
			return fmt.Errorf("projection: tool_call for unknown agent %q", ev.AgentID)
		}
		row.ToolCalls++
		row.UpdatedAt = ev.TS
	case EvtToolResult:
		// no projection impact in preflight; production will record duration
		row, ok := p.rows[ev.AgentID]
		if ok {
			row.UpdatedAt = ev.TS
		}
	case EvtProviderCall, EvtUserMessage, EvtAssistantMessage, EvtSteering, EvtArtifactRef, EvtSessionReset, EvtResearchPhase,
		EvtUserShellStart, EvtUserShellEnd,
		EvtGatewayInbound, EvtGatewayOutbound,
		EvtApprovalProposed, EvtApprovalAccepted, EvtApprovalRejected,
		EvtCommandUsed:
		row, ok := p.rows[ev.AgentID]
		if ok {
			row.UpdatedAt = ev.TS
		}
	case EvtHeartbeat:
		row, ok := p.rows[ev.AgentID]
		if !ok {
			return fmt.Errorf("projection: heartbeat for unknown agent %q", ev.AgentID)
		}
		row.LastHeartbeatAt = ev.TS
		row.UpdatedAt = ev.TS
	default:
		return fmt.Errorf("projection: unknown event type %q", ev.Type)
	}
	return nil
}

// Replay rebuilds a fresh Projection by streaming all events for agentID
// from the log in seq order. This is the recovery path used at startup.
func Replay(ctx context.Context, log *SQLiteEventLog, agentID string) (*Projection, error) {
	p := NewProjection()
	evs, err := log.Read(ctx, agentID, 0)
	if err != nil {
		return nil, err
	}
	for _, ev := range evs {
		if err := p.Apply(ev); err != nil {
			return nil, fmt.Errorf("replay: apply seq=%d: %w", ev.Seq, err)
		}
	}
	return p, nil
}

// ReplayAll rebuilds a Projection covering every agent_id present in the log.
// Used by the SoT proof to verify a multi-agent scenario.
func ReplayAll(ctx context.Context, log *SQLiteEventLog) (*Projection, error) {
	rows, err := log.DB().QueryContext(ctx, `SELECT DISTINCT agent_id FROM events`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	merged := NewProjection()
	for _, id := range ids {
		p, err := Replay(ctx, log, id)
		if err != nil {
			return nil, err
		}
		for k, v := range p.rows {
			merged.rows[k] = v
		}
	}
	return merged, nil
}

// CountEvents returns the total number of events in the log; used by the
// bench to report final-row tallies after a kill-and-recover cycle.
func CountEvents(ctx context.Context, log *SQLiteEventLog) (int64, error) {
	row := log.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM events`)
	var n int64
	return n, row.Scan(&n)
}

// MaxSeq returns the highest seq committed; used by recovery to confirm
// nothing committed was lost.
func MaxSeq(ctx context.Context, db *sql.DB) (int64, error) {
	row := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`)
	var n int64
	return n, row.Scan(&n)
}
