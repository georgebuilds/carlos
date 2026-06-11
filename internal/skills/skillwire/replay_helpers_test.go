package skillwire

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/skills"
)

// TestInitialUserMessage_NoUserMessage: a transcript with no user turn
// returns nil so runOnePair can short-circuit with "no initial user
// message".
func TestInitialUserMessage_NoUserMessage(t *testing.T) {
	transcript := []providers.Message{
		{Role: "assistant", Content: []providers.Block{{Kind: "text", Text: "hi"}}},
	}
	if got := initialUserMessage(transcript); got != nil {
		t.Errorf("want nil for user-less transcript, got %v", got)
	}
}

// TestInitialUserMessage_FirstUserWins: the FIRST user message is the
// seed, returned as a single-message slice.
func TestInitialUserMessage_FirstUserWins(t *testing.T) {
	transcript := []providers.Message{
		{Role: "assistant", Content: []providers.Block{{Kind: "text", Text: "preamble"}}},
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: "first"}}},
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: "second"}}},
	}
	got := initialUserMessage(transcript)
	if len(got) != 1 || got[0].Content[0].Text != "first" {
		t.Errorf("want single 'first' message, got %v", got)
	}
}

// TestFinalAssistantText_ConcatenatesTextBlocks: the LAST assistant
// message's text blocks are concatenated; tool-use blocks are skipped.
func TestFinalAssistantText_ConcatenatesTextBlocks(t *testing.T) {
	msgs := []providers.Message{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: "q"}}},
		{Role: "assistant", Content: []providers.Block{
			{Kind: "text", Text: "alpha "},
			{Kind: "tool_use", ToolName: "bash"},
			{Kind: "text", Text: "beta"},
		}},
	}
	got := finalAssistantText(msgs)
	if string(got) != "alpha beta" {
		t.Errorf("want 'alpha beta', got %q", string(got))
	}
}

// TestFinalAssistantText_NoAssistant: a transcript with no assistant
// turn returns nil.
func TestFinalAssistantText_NoAssistant(t *testing.T) {
	msgs := []providers.Message{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: "q"}}},
	}
	if got := finalAssistantText(msgs); got != nil {
		t.Errorf("want nil, got %q", string(got))
	}
}

// TestFinalAssistantText_AllToolUseFinalTurnIsNil: an assistant message
// composed solely of tool-use blocks yields nil (no scoreable text).
func TestFinalAssistantText_AllToolUseFinalTurnIsNil(t *testing.T) {
	msgs := []providers.Message{
		{Role: "assistant", Content: []providers.Block{
			{Kind: "tool_use", ToolName: "bash"},
		}},
	}
	if got := finalAssistantText(msgs); got != nil {
		t.Errorf("want nil for all-tool-use turn, got %q", string(got))
	}
}

// TestBestReport_Empty returns a zero-score reject sentinel.
func TestBestReport_Empty(t *testing.T) {
	got := bestReport(nil)
	if got.Score != 0 || got.Decision != agent.VerificationReject {
		t.Errorf("empty bestReport: want {0, reject}, got %+v", got)
	}
}

// TestBestReport_HighestWins picks the max-score report; ties keep the
// first.
func TestBestReport_HighestWins(t *testing.T) {
	reports := []agent.VerificationReport{
		{Score: 3, JudgeModel: "a"},
		{Score: 9, JudgeModel: "b"},
		{Score: 9, JudgeModel: "c"}, // tie with b; b (first) keeps
		{Score: 5, JudgeModel: "d"},
	}
	got := bestReport(reports)
	if got.Score != 9 || got.JudgeModel != "b" {
		t.Errorf("want first 9-score (b), got %+v", got)
	}
}

// TestCompareScores covers all three branches of the +1/-1/0 contract,
// including the error-forces-tie rule.
func TestCompareScores(t *testing.T) {
	hi := agent.VerificationReport{Score: 9}
	lo := agent.VerificationReport{Score: 4}
	if d := compareScores(hi, lo, "", ""); d != +1 {
		t.Errorf("with>without: want +1, got %d", d)
	}
	if d := compareScores(lo, hi, "", ""); d != -1 {
		t.Errorf("with<without: want -1, got %d", d)
	}
	if d := compareScores(hi, hi, "", ""); d != 0 {
		t.Errorf("equal: want 0, got %d", d)
	}
	// An error on either arm forces a tie regardless of scores.
	if d := compareScores(hi, lo, "boom", ""); d != 0 {
		t.Errorf("with-skill error: want 0, got %d", d)
	}
	if d := compareScores(hi, lo, "", "boom"); d != 0 {
		t.Errorf("without-skill error: want 0, got %d", d)
	}
}

// TestTranscriptsMentionResearch_ToolResultMatch: a research-kind
// artifact reference inside a tool_result body trips the sniff.
func TestTranscriptsMentionResearch_ToolResultMatch(t *testing.T) {
	transcripts := [][]providers.Message{{
		{Role: "user", Content: []providers.Block{
			{Kind: "tool_result", ToolResult: []byte(`{"id":"x","kind":"research"}`)},
		}},
	}}
	if !transcriptsMentionResearch(transcripts) {
		t.Error("expected research-kind tool_result to match")
	}
}

// TestTranscriptsMentionResearch_TextPhraseMatch: the "research
// artifact" text phrase also matches (case-insensitive).
func TestTranscriptsMentionResearch_TextPhraseMatch(t *testing.T) {
	transcripts := [][]providers.Message{{
		{Role: "assistant", Content: []providers.Block{
			{Kind: "text", Text: "I produced a Research Artifact for you"},
		}},
	}}
	if !transcriptsMentionResearch(transcripts) {
		t.Error("expected 'research artifact' phrase to match")
	}
}

// TestTranscriptsMentionResearch_NoMatch: ordinary transcripts don't
// trip the sniff.
func TestTranscriptsMentionResearch_NoMatch(t *testing.T) {
	transcripts := [][]providers.Message{{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hello"}}},
		{Role: "assistant", Content: []providers.Block{{Kind: "text", Text: "world"}}},
	}}
	if transcriptsMentionResearch(transcripts) {
		t.Error("did not expect a research match")
	}
}

// TestEvaluate_NoUserSeedRecordsArmErrors: a transcript with no user
// message means runOnePair can't build a seed; both arms record the
// "no initial user message" error and the pair is a tie. Drives the
// runOnePair early-return branch through the public Evaluate API.
func TestEvaluate_NoUserSeedRecordsArmErrors(t *testing.T) {
	disp := &fakeDispatcher{queue: []dispatcherResponse{
		{reports: []agent.VerificationReport{{Score: 5, JudgeModel: "fake"}}},
	}}
	r := &ReplayEvaluator{
		Provider:          scriptedProvider("x"),
		Dispatcher:        disp,
		MaxReplays:        1,
		MaxLoopIterations: 1,
	}
	// Transcript with an assistant turn only — no user seed.
	transcripts := [][]providers.Message{{
		{Role: "assistant", Content: []providers.Block{{Kind: "text", Text: "no user here"}}},
	}}
	rep, err := r.Evaluate(context.Background(), sampleProposalDiff(), transcripts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Replays) != 1 {
		t.Fatalf("want 1 replay pair, got %d", len(rep.Replays))
	}
	pair := rep.Replays[0]
	if pair.WithSkillErr == "" || pair.WithoutSkillErr == "" {
		t.Errorf("both arms should record a 'no initial user message' error, got %+v", pair)
	}
	if pair.Delta != 0 {
		t.Errorf("arm errors should force a tie (Delta=0), got %d", pair.Delta)
	}
	// The dispatcher should NOT have been called for a seedless pair.
	if disp.calls != 0 {
		t.Errorf("dispatcher should not run on a seedless pair, got %d calls", disp.calls)
	}
}

// TestReport_String renders valid indented JSON carrying the report
// fields.
func TestReport_String(t *testing.T) {
	r := Report{TotalProposals: 3, Accepted: 2, Rejected: 1, AcceptanceRate: 0.667}
	s := r.String()
	if !strings.Contains(s, `"total_proposals": 3`) {
		t.Errorf("String() missing total_proposals: %s", s)
	}
	if !strings.Contains(s, `"accepted": 2`) {
		t.Errorf("String() missing accepted: %s", s)
	}
}

// TestAcceptanceRate_NilLog: a nil log is an error, not a silent zero.
func TestAcceptanceRate_NilLog(t *testing.T) {
	m := NewMetrics()
	if _, err := m.AcceptanceRate(context.Background(), nil); err == nil {
		t.Error("want error for nil log")
	}
}

// TestAcceptanceRate_NoDecidedProposalsIsZero: with proposals pending
// but none decided, the rate is 0 (empty denominator guard).
func TestAcceptanceRate_NoDecidedProposalsIsZero(t *testing.T) {
	log := newReplayTestLog(t)
	const id = "agent-rate"
	seedReplayAgent(t, log, id)
	if _, err := ProposeSkill(context.Background(), log, id, &skills.Proposal{
		Name: "pending-only", Description: "Use when pending",
	}); err != nil {
		t.Fatal(err)
	}
	rate, err := NewMetrics().AcceptanceRate(context.Background(), log)
	if err != nil {
		t.Fatal(err)
	}
	if rate != 0 {
		t.Errorf("want 0 rate with no decided proposals, got %v", rate)
	}
}

// TestProposalCounts_NonSkillProposalIgnored: an approval proposal of a
// NON-skill artifact kind must not be counted by the skill metrics.
// Exercises the kind-filter `continue` in proposalCounts.
func TestProposalCounts_NonSkillProposalIgnored(t *testing.T) {
	log := newReplayTestLog(t)
	const id = "agent-mix"
	seedReplayAgent(t, log, id)
	ctx := context.Background()

	// A non-skill artifact proposed for approval (e.g. a plan).
	otherRef, err := agent.WriteArtifact(ctx, log, id, agent.ArtifactKindFile, []byte(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.ProposeApproval(ctx, log, id, "file: thing", otherRef); err != nil {
		t.Fatal(err)
	}
	// Accept the non-skill proposal — must not bump skill accepted count.
	if _, err := agent.AcceptApproval(ctx, log, otherRef.ID, ""); err != nil {
		t.Fatal(err)
	}

	rep, err := NewMetrics().Snapshot(ctx, log, nil, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if rep.TotalProposals != 0 {
		t.Errorf("non-skill proposal should not count; got TotalProposals=%d", rep.TotalProposals)
	}
	if rep.Accepted != 0 {
		t.Errorf("non-skill accept should not count; got Accepted=%d", rep.Accepted)
	}
}
