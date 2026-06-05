// Approval queue — the user-facing review gate for every artifact a
// sub-agent (or, eventually, the parent agent) produces that needs
// human sign-off before changing user state.
//
// Per spec § Manage mode § Skill proposals are artifacts in this same
// log: "an induced skill is just another on-disk artifact referenced
// from the event log, reviewable as a markdown diff in the same
// focus-pane/approval UX as any other deliverable." Plans, file diffs,
// skill proposals, research outputs — they all flow through this one
// queue.
//
// # Wire shape
//
// Three event types compose the contract:
//
//   - EvtApprovalProposed — an artifact is queued for review. Payload
//     carries the ArtifactRef (so consumers don't have to re-query)
//     and a free-text Title the UI surfaces in the queue list.
//   - EvtApprovalAccepted — the user (or a policy) approved. Payload
//     carries the artifact ID and an optional Note.
//   - EvtApprovalRejected — the user rejected; payload same as accept
//     plus the rejection Reason.
//
// The "pending queue" is derived: scan EvtApprovalProposed events,
// filter out any whose artifact ID has a subsequent Accept or Reject.
// Cheap at carlos scale (hundreds of approvals over a session).
//
// # Why event-sourced rather than an `approvals` table
//
// Carlos's invariant from design is "event log is the source of
// truth; everything else is a projection." A separate approvals
// table would duplicate that, drift in failure modes, and require a
// migration we don't yet need. Today the queue is cheap to project;
// when scale demands it, we add a materialized view as a projection
// (the same pattern as the agents table).
//
// # Phase wiring
//
// - Phase 4h (this file): API + a list-pending query.
// - Phase 4 manage TUI: a queue pane that ListPending → renders → on
//   keypress calls Accept / Reject.
// - Phase 6 skill induction: every PROPOSAL.md write follows up with
//   ProposeApproval. Acceptance promotes the skill to active.
// - Phase 7 plan/preview/apply: every plan write follows up with
//   ProposeApproval. Acceptance triggers the worktree Apply.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ApprovalProposalPayload is the JSON body of an EvtApprovalProposed
// event. Title is what the queue list renders; Ref is the on-disk
// artifact the user reviews.
type ApprovalProposalPayload struct {
	Title string      `json:"title"`
	Ref   ArtifactRef `json:"ref"`
}

// ApprovalResolutionPayload is the body of EvtApprovalAccepted or
// EvtApprovalRejected. ArtifactID matches the Ref.ID in the original
// Propose payload; Note is freeform context the user wrote at decide
// time (often empty for accepts, a sentence or two for rejects).
type ApprovalResolutionPayload struct {
	ArtifactID string `json:"artifact_id"`
	Note       string `json:"note,omitempty"`
}

// PendingApproval is the projection row the TUI consumes: one
// outstanding approval, with the artifact ref + the agent that
// produced it + when. agentID is the producer; the user resolves on
// behalf of "the workspace", so resolution events use a synthetic
// "user" agent ID (see Accept/Reject).
type PendingApproval struct {
	AgentID    string
	Title      string
	Ref        ArtifactRef
	ProposedAt time.Time
}

// resolverAgentID is the synthetic agent_id used for user-initiated
// resolution events. Distinct from any real agent so queries can
// filter on it without colliding with a child's events.
const resolverAgentID = "user"

// ProposeApproval queues artifactRef for human review. The producing
// agent (typically a sub-agent that just wrote a deliverable) calls
// this; the event lands in the producer's log namespace. Returns the
// committed event seq for callers that want to wait/observe.
//
// Title is what the queue UI shows; pick something short but
// meaningful ("refactor cmd/foo into pkg/bar", "skill: react-test-debug").
func ProposeApproval(ctx context.Context, log *SQLiteEventLog, agentID, title string, ref ArtifactRef) (int64, error) {
	if title == "" {
		return 0, errors.New("approval: title required")
	}
	if ref.ID == "" {
		return 0, errors.New("approval: artifact ref ID required")
	}
	payload, err := json.Marshal(ApprovalProposalPayload{Title: title, Ref: ref})
	if err != nil {
		return 0, fmt.Errorf("approval: marshal: %w", err)
	}
	return log.Append(ctx, Event{
		AgentID: agentID,
		TS:      time.Now().UTC().Truncate(time.Millisecond),
		Type:    EvtApprovalProposed,
		Payload: payload,
	})
}

// AcceptApproval closes a pending approval as accepted. Appends an
// EvtApprovalAccepted event under the synthetic "user" agent ID so
// queries can attribute the decision to the human and the producing
// agent's namespace stays purely about the agent's own activity.
func AcceptApproval(ctx context.Context, log *SQLiteEventLog, artifactID, note string) (int64, error) {
	return resolveApproval(ctx, log, EvtApprovalAccepted, artifactID, note)
}

// RejectApproval closes a pending approval as rejected. The Note
// should carry the user's reasoning — it's what the producing agent
// reads next time it considers a similar proposal (eventually wired
// into Phase 6 skill-induction calibration).
func RejectApproval(ctx context.Context, log *SQLiteEventLog, artifactID, reason string) (int64, error) {
	return resolveApproval(ctx, log, EvtApprovalRejected, artifactID, reason)
}

func resolveApproval(ctx context.Context, log *SQLiteEventLog, et EventType, artifactID, note string) (int64, error) {
	if artifactID == "" {
		return 0, errors.New("approval: artifact ID required")
	}
	payload, err := json.Marshal(ApprovalResolutionPayload{ArtifactID: artifactID, Note: note})
	if err != nil {
		return 0, fmt.Errorf("approval: marshal: %w", err)
	}
	return log.Append(ctx, Event{
		AgentID: resolverAgentID,
		TS:      time.Now().UTC().Truncate(time.Millisecond),
		Type:    et,
		Payload: payload,
	})
}

// ListPendingApprovals returns every artifact with a Proposed event
// that has no subsequent Accept or Reject. Sorted by ProposedAt asc
// so the oldest pending review is at the top of the queue.
//
// Implementation: one cross-namespace events scan; small N at v0
// scale (the queue clears at human cadence; thousands of pending is
// pathological). When scale demands, we materialize a projection
// table the same way agents is materialized today.
func ListPendingApprovals(ctx context.Context, log *SQLiteEventLog) ([]PendingApproval, error) {
	rows, err := log.DB().QueryContext(ctx, `
		SELECT seq, agent_id, ts, type, payload FROM events
		WHERE type IN (?, ?, ?)
		ORDER BY seq ASC
	`, string(EvtApprovalProposed), string(EvtApprovalAccepted), string(EvtApprovalRejected))
	if err != nil {
		return nil, fmt.Errorf("approval: query: %w", err)
	}
	defer rows.Close()

	pending := map[string]PendingApproval{} // artifact ID → row
	for rows.Next() {
		var (
			seq     int64
			agentID string
			tsMs    int64
			typeS   string
			payload []byte
		)
		if err := rows.Scan(&seq, &agentID, &tsMs, &typeS, &payload); err != nil {
			return nil, err
		}
		switch EventType(typeS) {
		case EvtApprovalProposed:
			var p ApprovalProposalPayload
			if err := json.Unmarshal(payload, &p); err != nil {
				continue // skip malformed; don't block the whole queue
			}
			pending[p.Ref.ID] = PendingApproval{
				AgentID:    agentID,
				Title:      p.Title,
				Ref:        p.Ref,
				ProposedAt: time.UnixMilli(tsMs).UTC(),
			}
		case EvtApprovalAccepted, EvtApprovalRejected:
			var r ApprovalResolutionPayload
			if err := json.Unmarshal(payload, &r); err != nil {
				continue
			}
			delete(pending, r.ArtifactID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]PendingApproval, 0, len(pending))
	for _, p := range pending {
		out = append(out, p)
	}
	// Stable order: oldest proposal first.
	sortByProposedAt(out)
	return out, nil
}

// sortByProposedAt orders pending approvals oldest-first. Ties on
// ProposedAt (common: two proposals land in the same millisecond — we
// truncate to ms before persisting) break deterministically on
// Ref.ID so list output is stable across runs. Insertion sort because
// N is small (human-cadence review queue, hundreds at most).
func sortByProposedAt(a []PendingApproval) {
	less := func(x, y PendingApproval) bool {
		if !x.ProposedAt.Equal(y.ProposedAt) {
			return x.ProposedAt.Before(y.ProposedAt)
		}
		return x.Ref.ID < y.Ref.ID
	}
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && less(a[j], a[j-1]); j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
