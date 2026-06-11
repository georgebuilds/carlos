package agent_test

// Coverage for VerifyAndQueue / composeApprovalTitle guard + error
// branches not reached by the happy-path verifier_test.go cases:
//
//   - nil log and empty ref-ID input guards.
//   - ProposeApproval failure (closed DB) in the no-judge, verifier-
//     failed, and needs-revision queue paths.
//   - the >80-char concern truncation in the composed approval title.

import (
	"context"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

func TestVerifyAndQueue_NilLogErrors(t *testing.T) {
	ref := agent.ArtifactRef{ID: "r", AgentID: "child", Kind: agent.ArtifactKindAgentFinal}
	if _, err := agent.VerifyAndQueue(context.Background(), nil, nil, ref, []byte("x")); err == nil {
		t.Fatal("nil log should error")
	}
}

func TestVerifyAndQueue_EmptyRefIDErrors(t *testing.T) {
	log := openTestLog(t)
	ref := agent.ArtifactRef{ID: "", AgentID: "child", Kind: agent.ArtifactKindAgentFinal}
	if _, err := agent.VerifyAndQueue(context.Background(), log, nil, ref, []byte("x")); err == nil {
		t.Fatal("empty ref ID should error")
	}
}

// TestVerifyAndQueue_NoJudgeProposeFails closes the DB so the no-judge
// ProposeApproval write fails, hitting that error branch.
func TestVerifyAndQueue_NoJudgeProposeFails(t *testing.T) {
	log := closedLog(t, nil)
	ref := agent.ArtifactRef{ID: "r", AgentID: "child", Kind: agent.ArtifactKindAgentFinal}
	if _, err := agent.VerifyAndQueue(context.Background(), log, nil, ref, []byte("x")); err == nil {
		t.Fatal("propose on closed DB (no-judge path) should error")
	}
}

// TestVerifyAndQueue_VerifierFailedProposeFails drives the verifier-
// failed branch with a closed DB so its ProposeApproval also fails.
func TestVerifyAndQueue_VerifierFailedProposeFails(t *testing.T) {
	log := closedLog(t, nil)
	judge := scriptedJudge("a", "not json at all")
	v := &agent.Verifier{Judge: judge}
	ref := agent.ArtifactRef{ID: "r", AgentID: "child", Kind: agent.ArtifactKindAgentFinal}
	_, err := agent.VerifyAndQueue(context.Background(), log, v, ref, []byte("x"))
	if err == nil {
		t.Fatal("verifier-failed propose on closed DB should error")
	}
	if !strings.Contains(err.Error(), "propose after verifier err") {
		t.Errorf("error %q should mention the post-verifier propose failure", err)
	}
}

// TestVerifyAndQueue_QueuePathProposeFails drives the needs-revision
// queue path with a closed DB so the queue-path ProposeApproval fails.
func TestVerifyAndQueue_QueuePathProposeFails(t *testing.T) {
	log := closedLog(t, nil)
	judge := scriptedJudge("a", `{"score": 4, "concerns": ["weak"], "decision": "needs_revision"}`)
	v := &agent.Verifier{Judge: judge}
	ref := agent.ArtifactRef{ID: "r", AgentID: "child", Kind: agent.ArtifactKindAgentFinal}
	if _, err := agent.VerifyAndQueue(context.Background(), log, v, ref, []byte("x")); err == nil {
		t.Fatal("queue-path propose on closed DB should error")
	}
}

// TestVerifyAndQueue_LongConcernTruncatedInTitle uses a >80-char first
// concern so composeApprovalTitle's truncation branch fires.
func TestVerifyAndQueue_LongConcernTruncatedInTitle(t *testing.T) {
	log := openTestLog(t)
	longConcern := strings.Repeat("this is a very long concern line ", 5) // > 80 chars
	judge := scriptedJudge("a", `{"score": 3, "concerns": ["`+longConcern+`"], "decision": "reject"}`)
	v := &agent.Verifier{Judge: judge}
	ref := agent.ArtifactRef{ID: "r", AgentID: "child", Kind: agent.ArtifactKindAgentFinal}
	if _, err := agent.VerifyAndQueue(context.Background(), log, v, ref, []byte("x")); err != nil {
		t.Fatalf("verify: %v", err)
	}
	pend, _ := agent.ListPendingApprovals(context.Background(), log)
	if len(pend) != 1 {
		t.Fatalf("want 1 pending, got %d", len(pend))
	}
	if !strings.Contains(pend[0].Title, "...") {
		t.Errorf("long concern should be truncated with ellipsis: %q", pend[0].Title)
	}
}
