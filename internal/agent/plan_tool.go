// PlanTool — the model-facing handle on the propose-don't-publish gate.
//
// The model edits files inside a sandbox worktree using the usual
// read/write/edit/bash tools, then calls `plan` to package the
// accumulated diff into an approval-queue entry. The user reviews the
// diff out-of-band (`carlos approvals list` / accept / reject) and the
// foreground's apply-handler atomically merges (or discards) the
// worktree's branch when the user decides.
//
// What the model knows:
//
//   - The title + summary fields it provides.
//   - That after calling `plan`, the foreground will surface a
//     decision to the user.
//
// What the model does NOT know:
//
//   - Where the worktree lives (path, branch name, the parent repo's
//     identity). Those are foreground concerns and never appear in the
//     schema, the description, or the tool_result.
//   - Whether apply ultimately succeeds. The decision is async; the
//     model is told "queued for approval", not "applied".
//
// This split is load-bearing: it keeps the worktree-per-coding-task
// architecture invisible to the prompt surface, which means the same
// model can run with or without `--worktree` and the prompt doesn't
// have to change. Only the foreground's tool-registry construction
// differs.
//
// PlanTool lives in the `agent` package (not `tools`) because the
// approval-queue + artifact-store primitives it composes live here and
// `tools` cannot import `agent` without a cycle. Conceptually it's a
// peer of AgentTool — both are model-facing wrappers around supervisor
// machinery, both live next to the machinery they wrap.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/georgebuilds/carlos/internal/tools"
)

// PlanWorktree is the subset of *sandbox.Worktree PlanTool reads. The
// concrete type stays in internal/sandbox; we depend on the method set
// here so the tool stays fakeable in unit tests and the agent package
// avoids a transitive sandbox dependency it doesn't otherwise need.
type PlanWorktree interface {
	Diff() ([]byte, error)
	ChangedFiles() ([]string, error)
}

// PlanTool is the model-facing seam of the approval gate.
type PlanTool struct {
	// AgentID is the producing agent's id. The artifact rows and the
	// EvtApprovalProposed event are attributed to this id so the
	// approval-queue UI can group decisions by their authoring agent.
	AgentID string
	// Worktree is the sandbox the model is editing inside. PlanTool
	// calls Diff() + ChangedFiles() to package the artifact; it never
	// mutates the worktree.
	Worktree PlanWorktree
	// Log is the SQLite event log the artifact + approval events land
	// in. Same log the rest of the session writes to — there's no
	// separate "plan log".
	Log *SQLiteEventLog
}

// NewPlanTool wires a PlanTool with the given dependencies. All three
// fields are required at Execute time; Execute returns a clear infra
// error if any are missing so registration-time bugs surface as the
// model's first plan call rather than as a silent no-op.
func NewPlanTool(agentID string, w PlanWorktree, log *SQLiteEventLog) *PlanTool {
	return &PlanTool{AgentID: agentID, Worktree: w, Log: log}
}

// Name returns "plan".
func (*PlanTool) Name() string { return "plan" }

// Description is what the model sees in the tools array. It frames
// `plan` as the terminal step of an edit session — when the model
// is *done* and wants the user to review, not as a tool to call
// speculatively.
func (*PlanTool) Description() string {
	return "Queue the changes you've made in this session for the user's review. Call this AFTER you've made your edits (via write/edit/bash) and the work is ready for sign-off. The user sees the diff in the approval queue; on accept, the edits land atomically in their checkout; on reject, the work is discarded. Returns immediately with a plan id — the actual apply decision is asynchronous. Use sparingly: one call per logical unit of work, not one call per file."
}

// Schema is the JSON schema the model fills in. The diff itself is
// NOT in the schema — carlos computes it from the worktree's actual
// state so a hallucinated `diff` field can't bypass the gate.
func (*PlanTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"title": {
				"type": "string",
				"description": "One-line description of what this plan does, surfaces in the approval queue."
			},
			"summary": {
				"type": "string",
				"description": "A few sentences explaining the why + the scope. The user reads this before the diff."
			},
			"files_changed": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Paths the model claims to have modified. Carlos verifies against the worktree's actual diff."
			}
		},
		"required": ["title", "summary"]
	}`)
}

type planInput struct {
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`
	FilesChanged []string `json:"files_changed"`
}

// PlanArtifactMetaKind is the kind string the second artifact carries.
// Not promoted to ArtifactKind* in artifacts.go because that list is
// the SPEC-documented set; this is a satellite blob attached to a
// plan artifact, queryable by kind but not part of the headline
// taxonomy. Constant lives here so apply_handler can match on it.
const PlanArtifactMetaKind = "plan_meta"

// PlanMetadata is the second artifact PlanTool writes. The TUI's
// future approval pane renders these fields above the diff so a user
// can decide on context alone whether to crack open the patch. Kept
// as its own artifact (not merged into the diff blob) so a reviewer
// CLI can present the metadata without parsing the diff format.
type PlanMetadata struct {
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`
	FilesClaimed []string `json:"files_claimed"`
	FilesActual  []string `json:"files_actual"`
	PlanArtifact string   `json:"plan_artifact_id"`
}

// PlanResult is what the model sees as the tool_result body. The
// `await` field is a string the model can quote verbatim back to the
// user when explaining "your decision is needed" — keeps the
// instruction text in one place (here) so a future change to the CLI
// surface doesn't drift across prompts.
type PlanResult struct {
	Queued      bool     `json:"queued"`
	PlanID      string   `json:"plan_id"`
	MetadataID  string   `json:"metadata_id"`
	FilesActual []string `json:"files_actual"`
	Await       string   `json:"await"`
}

// Execute computes the worktree diff, packages it as an artifact,
// queues an approval, and returns a PlanResult JSON body. Errors
// returned are infrastructure errors (no diff, marshal failure,
// artifact write fail); a "model misunderstood the schema" surfaces
// as the parse error.
func (t *PlanTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	if t.Worktree == nil {
		return nil, errors.New("plan: worktree not wired (foreground forgot to inject)")
	}
	if t.Log == nil {
		return nil, errors.New("plan: event log not wired")
	}
	if t.AgentID == "" {
		return nil, errors.New("plan: agent_id not set")
	}

	var in planInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("plan: parse input: %w", err)
	}
	if in.Title == "" {
		return nil, errors.New("plan: title required")
	}
	if in.Summary == "" {
		return nil, errors.New("plan: summary required")
	}

	diff, err := t.Worktree.Diff()
	if err != nil {
		return nil, fmt.Errorf("plan: diff: %w", err)
	}
	if len(diff) == 0 {
		// The model called plan without having made any edits. Returning
		// this as a tool error (not a successful "queued: false") forces
		// the model to react and try again rather than silently telling
		// the user "I queued a plan" with nothing in it.
		return nil, errors.New("plan: no changes detected — make edits first")
	}

	actualFiles, err := t.Worktree.ChangedFiles()
	if err != nil {
		return nil, fmt.Errorf("plan: changed files: %w", err)
	}

	planRef, err := WriteArtifact(ctx, t.Log, t.AgentID, ArtifactKindPlan, diff)
	if err != nil {
		return nil, fmt.Errorf("plan: write diff artifact: %w", err)
	}

	meta := PlanMetadata{
		Title:        in.Title,
		Summary:      in.Summary,
		FilesClaimed: in.FilesChanged,
		FilesActual:  actualFiles,
		PlanArtifact: planRef.ID,
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("plan: marshal metadata: %w", err)
	}
	metaRef, err := WriteArtifact(ctx, t.Log, t.AgentID, PlanArtifactMetaKind, metaBytes)
	if err != nil {
		return nil, fmt.Errorf("plan: write metadata artifact: %w", err)
	}

	if _, err := ProposeApproval(ctx, t.Log, t.AgentID, in.Title, planRef); err != nil {
		return nil, fmt.Errorf("plan: propose approval: %w", err)
	}

	result := PlanResult{
		Queued:      true,
		PlanID:      planRef.ID,
		MetadataID:  metaRef.ID,
		FilesActual: actualFiles,
		Await:       fmt.Sprintf("carlos approvals accept %s to apply, or reject %s to discard", planRef.ID, planRef.ID),
	}
	out, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("plan: marshal result: %w", err)
	}
	return out, nil
}

// Compile-time check: PlanTool satisfies tools.Tool.
var _ tools.Tool = (*PlanTool)(nil)
