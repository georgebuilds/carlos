package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ProposalRef is the lightweight pointer to a proposed-fact artifact
// that ProposeFactWrite hands off to the approval queue. It mirrors
// the shape of internal/agent.ArtifactRef without forcing a memory
// → agent import (which would close a cycle, since agent imports
// memory for the Store field on agent.Config).
//
// The supervisor wraps agent.ArtifactRef into this shape via a thin
// adapter at the call site (cmd/carlos foreground wiring); the
// memory package only cares about the ID for return-value
// correlation.
type ProposalRef struct {
	ID      string
	AgentID string
	Path    string
	Kind    string
	SHA256  string
	Size    int64
}

// ProposalSink is the minimal interface memory needs to file a
// user-model proposal for human review. The supervisor implements
// this in cmd/carlos by delegating to agent.WriteArtifact +
// agent.ProposeApproval against the shared SQLiteEventLog.
//
// Why an interface here instead of importing agent directly: the
// agent package already imports memory (for the Store field on
// Config). Importing back would form a cycle; the indirection costs
// us one tiny adapter at the supervisor and buys us decoupling that
// makes the memory subsystem trivially testable without standing up
// a real event log.
type ProposalSink interface {
	// WriteProposalArtifact persists body as an artifact attributed
	// to agentID with the given kind, returning a ProposalRef the
	// approval queue can quote.
	WriteProposalArtifact(ctx context.Context, agentID, kind string, body []byte) (ProposalRef, error)
	// ProposeApproval queues ref for human review with the given
	// title under agentID's namespace.
	ProposeApproval(ctx context.Context, agentID, title string, ref ProposalRef) error
}

// Fact is one row in the `user_model` table - a slow-moving fact
// about the user. Examples: name, pronouns, preferred_email,
// working_hours, tech_stack, current_project. The taxonomy is loose:
// the model writes whatever fits, the user accepts or rejects.
type Fact struct {
	Key       string
	Value     string
	UpdatedAt time.Time
	// Source is "user" for user-curated facts, or
	// "agent_proposed_accepted" for facts that flowed through
	// ProposeFactWrite + approval. Empty for legacy rows.
	Source string
}

// FactSourceUser marks a fact set directly by the user (via a future
// /user CLI verb or a config import).
const FactSourceUser = "user"

// FactSourceAgentAccepted marks a fact that the agent proposed and
// the user accepted through the approval queue. The audit trail is
// the EvtApprovalAccepted event + the proposal artifact.
const FactSourceAgentAccepted = "agent_proposed_accepted"

// userModelProposalAgentID is the synthetic agent ID under which
// user-model proposals are filed. Matches the synthetic ID
// internal/agent/approval.go uses for resolutions ("user"), so the
// proposal + accept/reject pair land in the same namespace.
//
// IMPORTANT: callers MUST ensure a row with id="user" exists in the
// `agents` projection cache before calling ProposeFactWrite - the
// artifacts table has an FK to agents. The main.go bootstrap does
// this once at startup (Phase 7 follow-up).
const userModelProposalAgentID = "user"

// FactProposal is the JSON body of a user_model_proposal artifact.
// The approval queue's accept handler unmarshals this to call
// ApplyFact.
type FactProposal struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Rationale string `json:"rationale,omitempty"`
}

// GetFact looks up a single fact by key. Returns (value, true, nil)
// on hit, ("", false, nil) on miss. DB errors propagate.
func (s *Store) GetFact(ctx context.Context, key string) (string, bool, error) {
	if s == nil {
		return "", false, errors.New("memory: nil store")
	}
	if key == "" {
		return "", false, errors.New("memory: GetFact: empty key")
	}
	var value string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM user_model WHERE key = ?`, key,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("memory: get fact %s: %w", key, err)
	}
	return value, true, nil
}

// ListFacts returns every row in user_model, ordered by key ASC so
// CLI / TUI output is deterministic.
func (s *Store) ListFacts(ctx context.Context) ([]Fact, error) {
	if s == nil {
		return nil, errors.New("memory: nil store")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value, updated_at, COALESCE(source, '') FROM user_model ORDER BY key ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("memory: list facts: %w", err)
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		var (
			f         Fact
			updatedMs int64
		)
		if err := rows.Scan(&f.Key, &f.Value, &updatedMs, &f.Source); err != nil {
			return nil, fmt.Errorf("memory: scan fact: %w", err)
		}
		f.UpdatedAt = time.UnixMilli(updatedMs).UTC()
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ProposalArtifactKind is the artifact kind written by
// ProposeFactWrite. Pinned as a constant so the future
// promote-on-accept event subscriber can filter on it without
// stringly-typing the value at two call sites.
const ProposalArtifactKind = "user_model_proposal"

// ProposeFactWrite is the confirm-before-write entry point. It does
// NOT write to user_model. Instead:
//
//  1. Marshals (key, value, rationale) as a FactProposal JSON blob.
//  2. Writes that blob as an artifact (kind=user_model_proposal) via
//     sink.WriteProposalArtifact under the synthetic "user" agent ID.
//  3. Calls sink.ProposeApproval so the artifact lands in the
//     manage-mode approval queue.
//
// The user accepts or rejects via the 4h queue. On accept, a future
// event subscriber (Phase 7 follow-up; see notes file) reads the
// artifact, unmarshals the FactProposal, and calls ApplyFact to do
// the actual UPSERT. This mirrors the propose-don't-publish rule
// from skill induction (artifacts.go § Phase 6).
//
// sink is supplied by cmd/carlos foreground wiring: it wraps
// agent.WriteArtifact + agent.ProposeApproval against the shared
// SQLiteEventLog. See the notes file for the adapter shape.
//
// Returns the proposed artifact's ID so callers can correlate the
// approval back to the proposal.
func (s *Store) ProposeFactWrite(ctx context.Context, sink ProposalSink, key, value, rationale string) (string, error) {
	if s == nil {
		return "", errors.New("memory: nil store")
	}
	if sink == nil {
		return "", errors.New("memory: ProposeFactWrite: nil sink")
	}
	if key == "" {
		return "", errors.New("memory: ProposeFactWrite: empty key")
	}
	if value == "" {
		return "", errors.New("memory: ProposeFactWrite: empty value")
	}
	body, err := json.Marshal(FactProposal{Key: key, Value: value, Rationale: rationale})
	if err != nil {
		return "", fmt.Errorf("memory: marshal fact proposal: %w", err)
	}
	ref, err := sink.WriteProposalArtifact(ctx, userModelProposalAgentID, ProposalArtifactKind, body)
	if err != nil {
		return "", fmt.Errorf("memory: write proposal artifact: %w", err)
	}
	title := "user-model update: " + key
	if err := sink.ProposeApproval(ctx, userModelProposalAgentID, title, ref); err != nil {
		return ref.ID, fmt.Errorf("memory: propose approval: %w", err)
	}
	return ref.ID, nil
}

// ApplyFact performs the actual UPSERT into user_model. This is what
// the event subscriber calls when an EvtApprovalAccepted lands for a
// user_model_proposal artifact (Phase 7 follow-up wires the
// subscriber; see notes file § Promote-on-accept seam).
//
// Source should be FactSourceUser for direct user edits or
// FactSourceAgentAccepted for promoted proposals. An empty source
// is allowed for legacy/back-compat callers but discouraged.
func (s *Store) ApplyFact(ctx context.Context, key, value, source string) error {
	if s == nil {
		return errors.New("memory: nil store")
	}
	if key == "" {
		return errors.New("memory: ApplyFact: empty key")
	}
	if value == "" {
		return errors.New("memory: ApplyFact: empty value")
	}
	now := time.Now().UTC().Truncate(time.Millisecond).UnixMilli()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_model(key, value, updated_at, source)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
		  value = excluded.value,
		  updated_at = excluded.updated_at,
		  source = excluded.source
	`, key, value, now, source)
	if err != nil {
		return fmt.Errorf("memory: apply fact %s: %w", key, err)
	}
	return nil
}
