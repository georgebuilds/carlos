package agent_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
)

// Slice 5c verifier tests. See internal/agent/verifier.go header for
// the empirical context (MAST, Too Consistent to Detect, etc).

// scriptedJudge is a fake.Provider whose Stream emits the given JSON
// verdict as text deltas. Lets us drive the verifier without a real
// LLM round-trip.
func scriptedJudge(name, jsonBody string) providers.Provider {
	return fake.New(name, fake.Script{
		{Kind: providers.EventTextDelta, Text: jsonBody},
		{Kind: providers.EventStopReason, Stop: "end_turn"},
	})
}

func TestVerifier_VerifyAcceptParses(t *testing.T) {
	judge := scriptedJudge("anthropic", `{"score": 9, "concerns": [], "decision": "accept"}`)
	v := &agent.Verifier{Judge: judge, JudgeModel: "claude-4"}
	r, err := v.Verify(context.Background(), agent.ArtifactRef{ID: "a1", AgentID: "child", Kind: agent.ArtifactKindAgentFinal}, []byte("body"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Score != 9 {
		t.Errorf("Score = %d", r.Score)
	}
	if r.Decision != agent.VerificationAccept {
		t.Errorf("Decision = %q", r.Decision)
	}
	if r.JudgeModel != "anthropic:claude-4" {
		t.Errorf("JudgeModel = %q", r.JudgeModel)
	}
}

func TestVerifier_VerifyNeedsRevisionParses(t *testing.T) {
	judge := scriptedJudge("openai", `{"score": 5, "concerns": ["missing tests", "unsupported claim"], "decision": "needs_revision"}`)
	v := &agent.Verifier{Judge: judge}
	r, err := v.Verify(context.Background(), agent.ArtifactRef{ID: "a1"}, []byte("body"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Decision != agent.VerificationNeedsRevision {
		t.Errorf("Decision = %q", r.Decision)
	}
	if len(r.Concerns) != 2 {
		t.Errorf("Concerns = %v", r.Concerns)
	}
}

func TestVerifier_VerifyRejectParses(t *testing.T) {
	judge := scriptedJudge("ollama", `{"score": 2, "concerns": ["fabricated"], "decision": "reject"}`)
	v := &agent.Verifier{Judge: judge}
	r, err := v.Verify(context.Background(), agent.ArtifactRef{ID: "a1"}, []byte("body"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Decision != agent.VerificationReject {
		t.Errorf("Decision = %q", r.Decision)
	}
}

func TestVerifier_VerifyToleratesFencedJSON(t *testing.T) {
	// Some judges wrap their JSON in a ```json fence. extractJSON
	// should find the object anyway.
	body := "```json\n{\"score\": 7, \"concerns\": [], \"decision\": \"accept\"}\n```"
	judge := scriptedJudge("a", body)
	v := &agent.Verifier{Judge: judge}
	r, err := v.Verify(context.Background(), agent.ArtifactRef{ID: "x"}, []byte("body"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Score != 7 {
		t.Errorf("Score = %d", r.Score)
	}
}

func TestVerifier_VerifyMalformedJSONErrors(t *testing.T) {
	judge := scriptedJudge("a", "the model just wrote prose and forgot json")
	v := &agent.Verifier{Judge: judge}
	r, err := v.Verify(context.Background(), agent.ArtifactRef{ID: "x"}, []byte("body"))
	if !errors.Is(err, agent.ErrMalformedJudgeResponse) {
		t.Fatalf("want ErrMalformedJudgeResponse, got %v", err)
	}
	// Raw body should still be populated so post-mortem can see it.
	if r.Raw == "" {
		t.Errorf("Raw should carry the malformed body for inspection")
	}
}

func TestVerifier_VerifyScoreOutOfRangeErrors(t *testing.T) {
	judge := scriptedJudge("a", `{"score": 99, "concerns": [], "decision": "accept"}`)
	v := &agent.Verifier{Judge: judge}
	_, err := v.Verify(context.Background(), agent.ArtifactRef{ID: "x"}, []byte("body"))
	if !errors.Is(err, agent.ErrMalformedJudgeResponse) {
		t.Fatalf("want ErrMalformedJudgeResponse, got %v", err)
	}
}

func TestVerifier_VerifyUnknownDecisionErrors(t *testing.T) {
	judge := scriptedJudge("a", `{"score": 5, "concerns": [], "decision": "maybe"}`)
	v := &agent.Verifier{Judge: judge}
	_, err := v.Verify(context.Background(), agent.ArtifactRef{ID: "x"}, []byte("body"))
	if !errors.Is(err, agent.ErrMalformedJudgeResponse) {
		t.Fatalf("want ErrMalformedJudgeResponse, got %v", err)
	}
}

func TestVerifier_VerifyNilJudgeErrors(t *testing.T) {
	v := &agent.Verifier{}
	_, err := v.Verify(context.Background(), agent.ArtifactRef{ID: "x"}, []byte{})
	if err == nil {
		t.Fatalf("expected error on nil judge")
	}
}

func TestVerifier_VerifyTruncatesLargeBody(t *testing.T) {
	// Body bigger than cap; verifier should still call Judge successfully.
	body := strings.Repeat("a", 256*1024)
	judge := scriptedJudge("a", `{"score": 8, "concerns": [], "decision": "accept"}`)
	v := &agent.Verifier{Judge: judge, MaxArtifactBytes: 128 * 1024}
	r, err := v.Verify(context.Background(), agent.ArtifactRef{ID: "x"}, []byte(body))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Decision != agent.VerificationAccept {
		t.Errorf("Decision = %q", r.Decision)
	}
}

// ---- SelectJudgeProvider ----

func TestSelectJudgeProvider_PickFirstDifferent(t *testing.T) {
	a := fake.New("anthropic", nil)
	o := fake.New("openai", nil)
	ol := fake.New("ollama", nil)
	got, err := agent.SelectJudgeProvider("anthropic", []providers.Provider{a, o, ol})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got.Name() != "openai" {
		t.Errorf("want openai, got %s", got.Name())
	}
}

func TestSelectJudgeProvider_OnlyInducerErrors(t *testing.T) {
	a := fake.New("anthropic", nil)
	_, err := agent.SelectJudgeProvider("anthropic", []providers.Provider{a})
	if !errors.Is(err, agent.ErrNoJudgeAvailable) {
		t.Fatalf("want ErrNoJudgeAvailable, got %v", err)
	}
}

func TestSelectJudgeProvider_EmptyListErrors(t *testing.T) {
	_, err := agent.SelectJudgeProvider("anthropic", nil)
	if !errors.Is(err, agent.ErrNoJudgeAvailable) {
		t.Fatalf("want ErrNoJudgeAvailable, got %v", err)
	}
}

func TestSelectJudgeProvider_SkipsNils(t *testing.T) {
	o := fake.New("openai", nil)
	got, err := agent.SelectJudgeProvider("anthropic", []providers.Provider{nil, nil, o})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got.Name() != "openai" {
		t.Errorf("want openai, got %s", got.Name())
	}
}

func TestSelectJudgeProvider_ThreeProvidersStillPicksFirst(t *testing.T) {
	a := fake.New("anthropic", nil)
	o := fake.New("openai", nil)
	ol := fake.New("ollama", nil)
	// Inducer = openai; should pick anthropic (first different).
	got, err := agent.SelectJudgeProvider("openai", []providers.Provider{a, o, ol})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got.Name() != "anthropic" {
		t.Errorf("want anthropic, got %s", got.Name())
	}
}

// ---- VerifyAndQueue ----

func openTestLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { agent.CloseStateDB(log) })
	return log
}

func TestVerifyAndQueue_CleanAcceptDoesNotQueue(t *testing.T) {
	log := openTestLog(t)
	judge := scriptedJudge("a", `{"score": 10, "concerns": [], "decision": "accept"}`)
	v := &agent.Verifier{Judge: judge}
	ref := agent.ArtifactRef{ID: "ref1", AgentID: "child", Kind: agent.ArtifactKindAgentFinal}
	r, err := agent.VerifyAndQueue(context.Background(), log, v, ref, []byte("body"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Decision != agent.VerificationAccept {
		t.Errorf("Decision = %q", r.Decision)
	}
	pend, err := agent.ListPendingApprovals(context.Background(), log)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pend) != 0 {
		t.Errorf("clean accept should not queue: got %d pending", len(pend))
	}
}

func TestVerifyAndQueue_LowScoreQueues(t *testing.T) {
	log := openTestLog(t)
	judge := scriptedJudge("a", `{"score": 5, "concerns": ["weak evidence"], "decision": "needs_revision"}`)
	v := &agent.Verifier{Judge: judge}
	ref := agent.ArtifactRef{ID: "ref2", AgentID: "child", Kind: agent.ArtifactKindAgentFinal}
	_, err := agent.VerifyAndQueue(context.Background(), log, v, ref, []byte("body"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	pend, _ := agent.ListPendingApprovals(context.Background(), log)
	if len(pend) != 1 {
		t.Fatalf("want 1 pending, got %d", len(pend))
	}
	if !strings.Contains(pend[0].Title, "needs_revision") {
		t.Errorf("title missing decision: %q", pend[0].Title)
	}
	if !strings.Contains(pend[0].Title, "weak evidence") {
		t.Errorf("title missing concern: %q", pend[0].Title)
	}
}

func TestVerifyAndQueue_VerifierFailedQueuesLoudly(t *testing.T) {
	log := openTestLog(t)
	judge := scriptedJudge("a", "definitely not json")
	v := &agent.Verifier{Judge: judge}
	ref := agent.ArtifactRef{ID: "ref3", AgentID: "child", Kind: agent.ArtifactKindAgentFinal}
	_, err := agent.VerifyAndQueue(context.Background(), log, v, ref, []byte("body"))
	if !errors.Is(err, agent.ErrMalformedJudgeResponse) {
		t.Fatalf("want ErrMalformedJudgeResponse, got %v", err)
	}
	pend, _ := agent.ListPendingApprovals(context.Background(), log)
	if len(pend) != 1 {
		t.Fatalf("verifier-failed should still queue; got %d pending", len(pend))
	}
	if !strings.Contains(pend[0].Title, "verifier-failed") {
		t.Errorf("title should flag verifier failure: %q", pend[0].Title)
	}
}

func TestVerifyAndQueue_NilVerifierQueuesUnverified(t *testing.T) {
	log := openTestLog(t)
	ref := agent.ArtifactRef{ID: "ref4", AgentID: "child", Kind: agent.ArtifactKindAgentFinal}
	_, err := agent.VerifyAndQueue(context.Background(), log, nil, ref, []byte("body"))
	if err != nil {
		t.Fatalf("nil verifier should not error: %v", err)
	}
	pend, _ := agent.ListPendingApprovals(context.Background(), log)
	if len(pend) != 1 {
		t.Fatalf("nil verifier should queue unverified; got %d pending", len(pend))
	}
	if !strings.Contains(pend[0].Title, "unverified") {
		t.Errorf("title should flag unverified: %q", pend[0].Title)
	}
}

func TestVerifyAndQueue_BorderlineScoreQueuesEvenOnAccept(t *testing.T) {
	// Decision=accept but score < 8 → still queues for human eyes.
	log := openTestLog(t)
	judge := scriptedJudge("a", `{"score": 7, "concerns": ["minor"], "decision": "accept"}`)
	v := &agent.Verifier{Judge: judge}
	ref := agent.ArtifactRef{ID: "ref5", AgentID: "child", Kind: agent.ArtifactKindAgentFinal}
	_, err := agent.VerifyAndQueue(context.Background(), log, v, ref, []byte("body"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	pend, _ := agent.ListPendingApprovals(context.Background(), log)
	if len(pend) != 1 {
		t.Errorf("borderline accept should still queue; got %d", len(pend))
	}
}
