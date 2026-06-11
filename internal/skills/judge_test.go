package skills_test

import (
	"context"
	"testing"

	"github.com/georgebuilds/carlos/internal/skills"
)

func TestJudge_HappyPath(t *testing.T) {
	canned := `{"quality": 8, "decision": "accept", "concerns": ["minor: body is verbose"]}`
	p := fakeProviderEmitting("openai", canned)
	j := skills.NewJudge(p)
	score, err := j.Score(context.Background(), &skills.Proposal{
		Name:        "n",
		Description: "Use when ...",
		Body:        "step 1\nstep 2",
	}, skills.JudgeOptions{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if score.Quality != 8 {
		t.Errorf("quality: want 8 got %d", score.Quality)
	}
	if score.Decision != skills.DecisionAccept {
		t.Errorf("decision: want accept got %q", score.Decision)
	}
	if score.JudgeModel != "openai:gpt-4o" {
		t.Errorf("judge_model: want openai:gpt-4o got %q", score.JudgeModel)
	}
	if len(score.Concerns) != 1 {
		t.Errorf("concerns: %v", score.Concerns)
	}
}

func TestJudge_RejectDecision(t *testing.T) {
	canned := `{"quality": 2, "decision": "reject", "concerns": ["not reusable"]}`
	p := fakeProviderEmitting("anthropic", canned)
	j := skills.NewJudge(p)
	score, err := j.Score(context.Background(), &skills.Proposal{
		Name:        "n",
		Description: "Use when ...",
	}, skills.JudgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if score.Decision != skills.DecisionReject {
		t.Errorf("want reject, got %q", score.Decision)
	}
}

func TestJudge_QualityOutOfRange(t *testing.T) {
	canned := `{"quality": 99, "decision": "accept"}`
	p := fakeProviderEmitting("x", canned)
	j := skills.NewJudge(p)
	_, err := j.Score(context.Background(), &skills.Proposal{Name: "n", Description: "d"}, skills.JudgeOptions{})
	if err == nil {
		t.Error("want quality-range error")
	}
}

func TestJudge_UnknownDecision(t *testing.T) {
	canned := `{"quality": 5, "decision": "maybe"}`
	p := fakeProviderEmitting("x", canned)
	j := skills.NewJudge(p)
	_, err := j.Score(context.Background(), &skills.Proposal{Name: "n", Description: "d"}, skills.JudgeOptions{})
	if err == nil {
		t.Error("want unknown-decision error")
	}
}

func TestJudge_NilProvider(t *testing.T) {
	j := &skills.Judge{}
	_, err := j.Score(context.Background(), &skills.Proposal{Name: "n", Description: "d"}, skills.JudgeOptions{})
	if err == nil {
		t.Error("want nil-provider error")
	}
}

func TestJudge_NilProposal(t *testing.T) {
	p := fakeProviderEmitting("x", `{"quality":5,"decision":"accept"}`)
	j := skills.NewJudge(p)
	_, err := j.Score(context.Background(), nil, skills.JudgeOptions{})
	if err == nil {
		t.Error("want nil-proposal error")
	}
}

// TestJudge_NeedsRevisionDecision exercises the third valid decision
// branch in parseJudgeOutput.
func TestJudge_NeedsRevisionDecision(t *testing.T) {
	canned := `{"quality": 6, "decision": "needs_revision", "concerns": ["tighten the trigger"]}`
	j := skills.NewJudge(fakeProviderEmitting("x", canned))
	score, err := j.Score(context.Background(), &skills.Proposal{Name: "n", Description: "d"}, skills.JudgeOptions{})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if score.Decision != skills.DecisionNeedsRevision {
		t.Errorf("want needs_revision, got %q", score.Decision)
	}
}

// TestJudge_EmptyResponse: a stream that yields nothing is a parse
// error.
func TestJudge_EmptyResponse(t *testing.T) {
	j := skills.NewJudge(fakeProviderEmitting("x", "   "))
	_, err := j.Score(context.Background(), &skills.Proposal{Name: "n", Description: "d"}, skills.JudgeOptions{})
	if err == nil {
		t.Error("want empty-response parse error")
	}
}

// TestJudge_MalformedJSON: garbage surfaces a parse error.
func TestJudge_MalformedJSON(t *testing.T) {
	j := skills.NewJudge(fakeProviderEmitting("x", "definitely not json"))
	_, err := j.Score(context.Background(), &skills.Proposal{Name: "n", Description: "d"}, skills.JudgeOptions{})
	if err == nil {
		t.Error("want malformed-JSON error")
	}
}

func TestJudge_SelectJudgeProviderHappy(t *testing.T) {
	got, err := skills.SelectJudgeProvider("anthropic", []string{"anthropic", "openai", "ollama"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "openai" {
		t.Errorf("want openai (first non-anthropic), got %q", got)
	}
}

func TestJudge_SelectJudgeProviderOnlyOne(t *testing.T) {
	_, err := skills.SelectJudgeProvider("anthropic", []string{"anthropic"})
	if err == nil {
		t.Error("want error when only inducer is configured")
	}
}

func TestJudge_SelectJudgeProviderEmptyInducer(t *testing.T) {
	_, err := skills.SelectJudgeProvider("", []string{"anthropic"})
	if err == nil {
		t.Error("want error for empty inducer")
	}
}
