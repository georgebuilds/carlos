package skillwire

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
	"github.com/georgebuilds/carlos/internal/skills"
)

// newReplayTestLog mirrors wire_test.go's newLog but in-package (we
// need access to unexported symbols for the picker tests, so the file
// can't sit in skillwire_test). Same artifact-base override + temp DB.
func newReplayTestLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(tmp, "artifacts"))
	dbPath := filepath.Join(tmp, "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// seedReplayAgent inserts the agents-row + state-change event the
// approval artifacts depend on. Same intent as wire_test.go's seedAgent.
func seedReplayAgent(t *testing.T, log *agent.SQLiteEventLog, id string) {
	t.Helper()
	ctx := context.Background()
	created, err := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: id, RootID: id, Title: id, Model: "fake",
	})
	if err != nil {
		t.Fatalf("seed: marshal: %v", err)
	}
	if _, err := log.Append(ctx, agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtStateChange, Payload: created,
	}); err != nil {
		t.Fatalf("seed: append: %v", err)
	}
	now := time.Now().UTC()
	row := agent.AgentRow{
		ID:              id,
		RootID:          id,
		State:           agent.StateRunning,
		Attempt:         1,
		Title:           id,
		Model:           "fake",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}
	if err := log.InsertAgent(ctx, row); err != nil {
		t.Fatalf("seed: insert agent: %v", err)
	}
}

// fakeDispatcher is a hand-scripted VerifierDispatcher. The Reports
// slice is consumed in order — each Verify call pops the next entry.
// Empty Reports + Err nil returns nil/nil (which the evaluator records
// as a tie). Use this to seed wins/losses/ties precisely.
type fakeDispatcher struct {
	queue []dispatcherResponse
	calls int
}

type dispatcherResponse struct {
	reports []agent.VerificationReport
	err     error
}

func (d *fakeDispatcher) Verify(_ context.Context, _, _ string, _ []byte) ([]agent.VerificationReport, error) {
	d.calls++
	if len(d.queue) == 0 {
		return nil, nil
	}
	r := d.queue[0]
	d.queue = d.queue[1:]
	return r.reports, r.err
}

// scriptedProvider returns a fake.Provider whose Stream emits a single
// text delta + end_turn — enough for agent.Run to terminate cleanly
// with `text` as the final assistant message body.
func scriptedProvider(text string) *fake.Provider {
	return fake.New("fake", fake.Script{
		{Kind: providers.EventTextDelta, Text: text},
		{Kind: providers.EventStopReason, Stop: "end_turn"},
	})
}

func sampleProposalDiff() *skills.Proposal {
	return &skills.Proposal{
		Name:        "go-test-debug",
		Description: "Use when go test is failing",
		Body:        "When tests fail, run `go test -v` and read the first error.",
		InducerName: "anthropic:claude",
	}
}

func sampleProposalResearch() *skills.Proposal {
	return &skills.Proposal{
		Name:        "refetch-changes",
		Description: "Use when you need to refetch a URL to detect changes",
		Body:        "1. http get the URL\n2. diff against the prior snapshot.",
		InducerName: "anthropic:claude",
	}
}

func sampleProposalUnscorable() *skills.Proposal {
	return &skills.Proposal{
		Name:        "greet-the-user",
		Description: "Use when greeting the user warmly",
		Body:        "Be warm and friendly.",
		InducerName: "anthropic:claude",
	}
}

func userMsg(text string) []providers.Message {
	return []providers.Message{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: text}}},
	}
}

// === Picker tests ===========================================================

func TestPickVerifierKind_TestInvocationRoutesToDiff(t *testing.T) {
	got := PickVerifierKind(sampleProposalDiff(), nil)
	if got.Kind != agent.ArtifactKindDiff {
		t.Errorf("kind = %q, want diff", got.Kind)
	}
	if !strings.Contains(got.Reason, "go test") {
		t.Errorf("reason missing go-test mention: %q", got.Reason)
	}
}

func TestPickVerifierKind_BuildInvocationRoutesToDiff(t *testing.T) {
	p := &skills.Proposal{
		Name: "build-cleanly", Description: "x",
		Body: "Run `cargo build --release` before shipping.",
	}
	got := PickVerifierKind(p, nil)
	if got.Kind != agent.ArtifactKindDiff {
		t.Errorf("kind = %q, want diff", got.Kind)
	}
}

func TestPickVerifierKind_TranscriptResearchArtifactRoutesToResearch(t *testing.T) {
	p := sampleProposalUnscorable()
	transcripts := [][]providers.Message{{
		{Role: "assistant", Content: []providers.Block{{
			Kind:       "tool_result",
			ToolResult: []byte(`{"kind":"research","payload":"..."}`),
		}}},
	}}
	got := PickVerifierKind(p, transcripts)
	if got.Kind != agent.ArtifactKindResearch {
		t.Errorf("kind = %q, want research", got.Kind)
	}
}

func TestPickVerifierKind_ResearchVocabularyRoutesToResearch(t *testing.T) {
	got := PickVerifierKind(sampleProposalResearch(), nil)
	if got.Kind != agent.ArtifactKindResearch {
		t.Errorf("kind = %q, want research", got.Kind)
	}
}

func TestPickVerifierKind_NothingMatchesSkips(t *testing.T) {
	got := PickVerifierKind(sampleProposalUnscorable(), nil)
	if got.Kind != "" {
		t.Errorf("kind = %q, want empty (no verifier fits)", got.Kind)
	}
	if got.Reason == "" {
		t.Error("expected a non-empty Reason on skip")
	}
}

func TestPickVerifierKind_NilProposalSkips(t *testing.T) {
	got := PickVerifierKind(nil, nil)
	if got.Kind != "" {
		t.Errorf("kind = %q, want empty on nil proposal", got.Kind)
	}
}

// === Evaluate tests =========================================================

func TestEvaluate_NilReceiverErrors(t *testing.T) {
	var r *ReplayEvaluator
	_, err := r.Evaluate(context.Background(), sampleProposalDiff(), nil)
	if err == nil {
		t.Fatal("nil evaluator: want error, got nil")
	}
}

func TestEvaluate_NilProposalErrors(t *testing.T) {
	r := &ReplayEvaluator{Provider: scriptedProvider("x"), Dispatcher: &fakeDispatcher{}}
	_, err := r.Evaluate(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("nil proposal: want error, got nil")
	}
}

func TestEvaluate_NilProviderErrors(t *testing.T) {
	r := &ReplayEvaluator{Dispatcher: &fakeDispatcher{}}
	_, err := r.Evaluate(context.Background(), sampleProposalDiff(), nil)
	if err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("nil provider: want error mentioning provider, got %v", err)
	}
}

func TestEvaluate_NilDispatcherErrors(t *testing.T) {
	r := &ReplayEvaluator{Provider: scriptedProvider("x")}
	_, err := r.Evaluate(context.Background(), sampleProposalDiff(), nil)
	if err == nil || !strings.Contains(err.Error(), "dispatcher") {
		t.Fatalf("nil dispatcher: want error mentioning dispatcher, got %v", err)
	}
}

func TestEvaluate_PickerSkipsLeaveReportSkipped(t *testing.T) {
	r := &ReplayEvaluator{
		Provider:   scriptedProvider("x"),
		Dispatcher: &fakeDispatcher{},
	}
	rep, err := r.Evaluate(context.Background(), sampleProposalUnscorable(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Skipped {
		t.Error("expected Skipped=true when no verifier fits")
	}
	if rep.Decision != ReplayDecisionInconclusive {
		t.Errorf("decision = %q, want inconclusive", rep.Decision)
	}
}

func TestEvaluate_NoTranscriptsIsInconclusive(t *testing.T) {
	r := &ReplayEvaluator{
		Provider:   scriptedProvider("x"),
		Dispatcher: &fakeDispatcher{},
	}
	rep, err := r.Evaluate(context.Background(), sampleProposalDiff(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped {
		t.Error("expected Skipped=false — picker DID find a kind, just no transcripts")
	}
	if rep.Decision != ReplayDecisionInconclusive {
		t.Errorf("decision = %q, want inconclusive", rep.Decision)
	}
	if len(rep.Concerns) == 0 {
		t.Error("expected a 'no transcripts' concern")
	}
}

func TestEvaluate_AllWinsAccepts(t *testing.T) {
	// Each pair: with-skill scores 9, without-skill scores 4 → Delta=+1.
	pairs := []dispatcherResponse{
		{reports: []agent.VerificationReport{{Score: 9, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 4, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 9, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 4, JudgeModel: "fake"}}},
	}
	disp := &fakeDispatcher{queue: pairs}
	r := &ReplayEvaluator{
		Provider:          scriptedProvider("with"),
		Dispatcher:        disp,
		MaxReplays:        2,
		MaxLoopIterations: 1,
	}
	transcripts := [][]providers.Message{userMsg("seed1"), userMsg("seed2")}
	rep, err := r.Evaluate(context.Background(), sampleProposalDiff(), transcripts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != ReplayDecisionAccept {
		t.Errorf("decision = %q, want accept (score=%.2f)", rep.Decision, rep.Score)
	}
	if rep.Score < 0.6 {
		t.Errorf("score = %.2f, want >= 0.6", rep.Score)
	}
	if len(rep.Replays) != 2 {
		t.Fatalf("replays = %d, want 2", len(rep.Replays))
	}
}

func TestEvaluate_AllLossesRejects(t *testing.T) {
	pairs := []dispatcherResponse{
		{reports: []agent.VerificationReport{{Score: 3, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 8, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 3, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 8, JudgeModel: "fake"}}},
	}
	disp := &fakeDispatcher{queue: pairs}
	r := &ReplayEvaluator{
		Provider:          scriptedProvider("x"),
		Dispatcher:        disp,
		MaxReplays:        2,
		MaxLoopIterations: 1,
	}
	transcripts := [][]providers.Message{userMsg("a"), userMsg("b")}
	rep, err := r.Evaluate(context.Background(), sampleProposalDiff(), transcripts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != ReplayDecisionReject {
		t.Errorf("decision = %q, want reject (score=%.2f)", rep.Decision, rep.Score)
	}
}

func TestEvaluate_MixedIsInconclusive(t *testing.T) {
	// Pair 1: win. Pair 2: loss. Win rate = 0.5 → inconclusive.
	pairs := []dispatcherResponse{
		{reports: []agent.VerificationReport{{Score: 9, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 4, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 4, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 9, JudgeModel: "fake"}}},
	}
	disp := &fakeDispatcher{queue: pairs}
	r := &ReplayEvaluator{
		Provider:          scriptedProvider("x"),
		Dispatcher:        disp,
		MaxReplays:        2,
		MaxLoopIterations: 1,
	}
	transcripts := [][]providers.Message{userMsg("a"), userMsg("b")}
	rep, err := r.Evaluate(context.Background(), sampleProposalDiff(), transcripts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != ReplayDecisionInconclusive {
		t.Errorf("decision = %q, want inconclusive (score=%.2f)", rep.Decision, rep.Score)
	}
}

func TestEvaluate_DispatcherErrorRecordedAsConcern(t *testing.T) {
	disp := &fakeDispatcher{queue: []dispatcherResponse{
		{err: errors.New("verifier blew up")},
		{reports: []agent.VerificationReport{{Score: 5, JudgeModel: "fake"}}},
	}}
	r := &ReplayEvaluator{
		Provider:          scriptedProvider("x"),
		Dispatcher:        disp,
		MaxReplays:        1,
		MaxLoopIterations: 1,
	}
	rep, err := r.Evaluate(context.Background(), sampleProposalDiff(), [][]providers.Message{userMsg("a")})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Concerns) == 0 {
		t.Error("expected a verifier-error concern")
	}
}

func TestEvaluate_ContextCancellationPreservesPartialResults(t *testing.T) {
	pairs := []dispatcherResponse{
		{reports: []agent.VerificationReport{{Score: 9, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 4, JudgeModel: "fake"}}},
	}
	disp := &fakeDispatcher{queue: pairs}
	r := &ReplayEvaluator{
		Provider:          scriptedProvider("x"),
		Dispatcher:        disp,
		MaxReplays:        5, // ask for 5
		MaxLoopIterations: 1,
	}
	ctx, cancel := context.WithCancel(context.Background())
	// 3 transcripts but we cancel after the first.
	t3 := [][]providers.Message{userMsg("a"), userMsg("b"), userMsg("c")}

	// Wrap: run the first pair, then cancel and run again.
	rep1, err := r.Evaluate(ctx, sampleProposalDiff(), t3[:1])
	if err != nil {
		t.Fatal(err)
	}
	if len(rep1.Replays) != 1 {
		t.Fatalf("rep1 replays = %d, want 1", len(rep1.Replays))
	}
	cancel()
	// After cancellation: a new Evaluate against the same cancelled ctx
	// should still produce a non-nil report with the cancel concern.
	rep2, err := r.Evaluate(ctx, sampleProposalDiff(), t3)
	if err != nil {
		t.Fatal(err)
	}
	if rep2 == nil {
		t.Fatal("rep2 nil after cancellation")
	}
	if len(rep2.Concerns) == 0 {
		t.Error("expected a context-cancelled concern on rep2")
	}
}

// === wire.go integration =====================================================

func TestProposeSkillWithOptions_AutoRejectSkipsQueue(t *testing.T) {
	log := newReplayTestLog(t)
	const id = "agent-replay-1"
	seedReplayAgent(t, log, id)

	// Force a reject decision by stacking lopsided scores.
	disp := &fakeDispatcher{queue: []dispatcherResponse{
		{reports: []agent.VerificationReport{{Score: 2, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 9, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 2, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 9, JudgeModel: "fake"}}},
	}}
	ev := &ReplayEvaluator{
		Provider:          scriptedProvider("x"),
		Dispatcher:        disp,
		MaxReplays:        2,
		MaxLoopIterations: 1,
	}
	res, err := ProposeSkillWithOptions(context.Background(), log, id, sampleProposalDiff(), ProposeOptions{
		Replay:      ev,
		Transcripts: [][]providers.Message{userMsg("a"), userMsg("b")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.AutoRejected {
		t.Errorf("AutoRejected = false, want true (reason=%q)", res.AutoRejectReason)
	}
	pending, err := agent.ListPendingApprovals(context.Background(), log)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("pending = %d, want 0 (auto-rejected should not queue)", len(pending))
	}
}

func TestProposeSkillWithOptions_AcceptDecorationOnTitle(t *testing.T) {
	log := newReplayTestLog(t)
	const id = "agent-replay-2"
	seedReplayAgent(t, log, id)

	disp := &fakeDispatcher{queue: []dispatcherResponse{
		{reports: []agent.VerificationReport{{Score: 9, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 3, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 9, JudgeModel: "fake"}}},
		{reports: []agent.VerificationReport{{Score: 3, JudgeModel: "fake"}}},
	}}
	ev := &ReplayEvaluator{
		Provider:          scriptedProvider("x"),
		Dispatcher:        disp,
		MaxReplays:        2,
		MaxLoopIterations: 1,
	}
	res, err := ProposeSkillWithOptions(context.Background(), log, id, sampleProposalDiff(), ProposeOptions{
		Replay:      ev,
		Transcripts: [][]providers.Message{userMsg("a"), userMsg("b")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.AutoRejected {
		t.Fatal("AutoRejected unexpectedly true")
	}
	pending, err := agent.ListPendingApprovals(context.Background(), log)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	if !strings.Contains(pending[0].Title, "verifier: accept") {
		t.Errorf("title missing verifier verdict: %q", pending[0].Title)
	}
}

func TestProposeSkillWithOptions_ZeroOptionsBehavesLikeProposeSkill(t *testing.T) {
	log := newReplayTestLog(t)
	const id = "agent-replay-3"
	seedReplayAgent(t, log, id)

	res, err := ProposeSkillWithOptions(context.Background(), log, id, sampleProposalDiff(), ProposeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.AutoRejected {
		t.Error("AutoRejected true with no Replay configured")
	}
	if res.ReplayReport != nil {
		t.Error("ReplayReport non-nil with no Replay configured")
	}
	pending, err := agent.ListPendingApprovals(context.Background(), log)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
}
