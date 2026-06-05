// Phase 7 slice 7e — apply handler.
//
// The apply handler is the foreground-side companion to PlanTool. When
// the user resolves a pending approval whose artifact is of kind
// `plan`, the handler dispatches to the sandbox.Worktree the model
// was editing inside:
//
//   - EvtApprovalAccepted → Worktree.Apply (ff-only merge into parent
//     branch; refuses if parent HEAD has moved — the user sees the
//     plan stay in the queue, can re-trigger after a rebase).
//   - EvtApprovalRejected → Worktree.Discard (drop the branch + dir).
//
// The handler is single-process and in-memory: the supervisor's
// `worktrees` map is the only registry. If carlos crashes between the
// model calling `plan` and the user resolving the approval, the entry
// is gone and the user has to re-run the session. The orphaned
// worktree on disk gets cleaned up by `git worktree prune`. v0
// limitation; a future slice could persist the supervisor's map
// alongside the agent row.
//
// Both verdicts produce an event on the event log so a post-mortem can
// always reconstruct what landed when:
//
//   - EvtArtifactWritten with kind `apply_outcome` carrying an
//     ApplyOutcome JSON payload (Status, Error, ResolvedAt).
//
// That kind doesn't graduate to ArtifactKind* — it's a satellite
// record like PlanArtifactMetaKind, not part of the SPEC-documented
// artifact taxonomy.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ApplyOutcomeKind is the artifact kind written for each apply / discard
// resolution. Stable string; the (eventual) post-mortem CLI greps for
// it. Not in artifacts.go's ArtifactKind* set — same reason as
// PlanArtifactMetaKind.
const ApplyOutcomeKind = "apply_outcome"

// ApplyOutcome is the structured record written to the event log after
// each plan resolution. The producing agent's id is the AgentID field
// on the wrapping Artifact row — not duplicated here.
type ApplyOutcome struct {
	// PlanArtifactID is the artifact the user accepted or rejected.
	PlanArtifactID string `json:"plan_artifact_id"`
	// Status is "applied" | "discarded" | "apply_failed" | "no_worktree".
	// "no_worktree" fires when the supervisor has no registered
	// worktree for the producing agent (crashed/restarted between
	// propose and accept).
	Status string `json:"status"`
	// Error is the underlying error string when Status implies one;
	// empty otherwise. Kept as a plain string so the record round-trips
	// through JSON cleanly.
	Error string `json:"error,omitempty"`
	// ResolvedAt is when the handler finished processing (apply or
	// discard returned). Distinct from the resolution event's TS —
	// that's when the user clicked accept.
	ResolvedAt time.Time `json:"resolved_at"`
}

// ApplyHandler is the long-running goroutine that subscribes to
// approval-resolution events and dispatches to the supervisor's
// registered worktrees. Construct one per session; cancel its ctx to
// stop it cleanly.
type ApplyHandler struct {
	// Supervisor owns the agentID → worktree map. Required.
	Supervisor *Supervisor
	// Log is the SQLite event log we subscribe to + write outcomes
	// into. Required.
	Log *SQLiteEventLog
}

// Run subscribes to EvtApprovalAccepted + EvtApprovalRejected events
// (under the synthetic resolverAgentID = "user") and dispatches each
// plan-kind resolution to the supervisor's registered worktree. Blocks
// until ctx is cancelled or the subscription channel closes.
//
// Returns nil on clean shutdown (ctx cancellation); returns an error
// only for setup failures. Per-event errors are recorded as
// ApplyOutcome artifacts, never bubbled out — the handler is the
// "always-run, never-die" supervisor of the gate.
func (h *ApplyHandler) Run(ctx context.Context) error {
	if h == nil || h.Supervisor == nil || h.Log == nil {
		return errors.New("apply handler: nil receiver, supervisor, or log")
	}
	ch, unsub, err := h.Log.Subscribe(resolverAgentID)
	if err != nil {
		return fmt.Errorf("apply handler: subscribe: %w", err)
	}
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			h.handle(ctx, ev)
		}
	}
}

// handle processes one resolution event. Routes accept→Apply and
// reject→Discard, writes an ApplyOutcome artifact for both paths.
// Skips events whose artifact isn't a plan — the resolver-namespace
// subscription catches every Accept/Reject regardless of kind.
func (h *ApplyHandler) handle(ctx context.Context, ev Event) {
	if ev.Type != EvtApprovalAccepted && ev.Type != EvtApprovalRejected {
		return
	}
	var p ApprovalResolutionPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return // malformed event; nothing to act on
	}
	producingAgent, kind, ok := h.lookupArtifact(ctx, p.ArtifactID)
	if !ok || kind != ArtifactKindPlan {
		return
	}

	worktree, ok := h.Supervisor.AgentWorktreeFor(producingAgent)
	if !ok {
		h.writeOutcome(ctx, producingAgent, ApplyOutcome{
			PlanArtifactID: p.ArtifactID,
			Status:         "no_worktree",
			Error:          "supervisor has no worktree for producing agent (process restart?)",
			ResolvedAt:     time.Now().UTC(),
		})
		return
	}

	outcome := ApplyOutcome{
		PlanArtifactID: p.ArtifactID,
		ResolvedAt:     time.Now().UTC(),
	}
	switch ev.Type {
	case EvtApprovalAccepted:
		if err := worktree.Apply(); err != nil {
			outcome.Status = "apply_failed"
			outcome.Error = err.Error()
		} else {
			outcome.Status = "applied"
			_ = worktree.Close()
			h.Supervisor.ClearAgentWorktree(producingAgent)
		}
	case EvtApprovalRejected:
		if err := worktree.Discard(); err != nil {
			outcome.Status = "apply_failed"
			outcome.Error = err.Error()
		} else {
			outcome.Status = "discarded"
			_ = worktree.Close()
			h.Supervisor.ClearAgentWorktree(producingAgent)
		}
	}
	outcome.ResolvedAt = time.Now().UTC()
	h.writeOutcome(ctx, producingAgent, outcome)
}

// lookupArtifact resolves an artifact ID to (producing agent ID, kind,
// found). Used by handle to know whether the resolved event refers to
// a plan artifact + which worktree to dispatch to. Queries the
// projection table directly because no helper exists in artifacts.go
// today; if more callers need this it should graduate.
func (h *ApplyHandler) lookupArtifact(ctx context.Context, id string) (string, string, bool) {
	row := h.Log.DB().QueryRowContext(ctx,
		`SELECT agent_id, kind FROM artifacts WHERE id = ?`, id)
	var agentID, kind string
	if err := row.Scan(&agentID, &kind); err != nil {
		return "", "", false
	}
	return agentID, kind, true
}

// writeOutcome records an ApplyOutcome artifact. Failure to write is
// not fatal — the apply / discard already happened on disk; we just
// lose one telemetry record. The handler MUST stay running so other
// resolutions still process.
func (h *ApplyHandler) writeOutcome(ctx context.Context, agentID string, o ApplyOutcome) {
	blob, err := json.Marshal(o)
	if err != nil {
		return
	}
	_, _ = WriteArtifact(ctx, h.Log, agentID, ApplyOutcomeKind, blob)
}
