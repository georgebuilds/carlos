package skills_test

import (
	"context"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
	"github.com/georgebuilds/carlos/internal/skills"
)

// fakeProviderEmitting builds a fake.Provider whose stream emits one
// text delta with the given body, then a stop event. Used to drive the
// inducer / judge with canned JSON.
func fakeProviderEmitting(name, body string) providers.Provider {
	script := fake.Script{
		{Kind: providers.EventTextDelta, Text: body},
		{Kind: providers.EventStopReason, Stop: "end_turn"},
	}
	return fake.New(name, script)
}

// TestInducer_StreamError: an EventError mid-stream propagates out of
// Induce as a wrapped error (streamText's error branch).
func TestInducer_StreamError(t *testing.T) {
	script := fake.Script{
		{Kind: providers.EventTextDelta, Text: "partial"},
		{Kind: providers.EventError, Err: context.DeadlineExceeded},
	}
	ind := skills.NewInducer(fake.New("x", script))
	_, err := ind.Induce(context.Background(), "transcript", nil, skills.InducerOptions{})
	if err == nil {
		t.Error("want stream error")
	}
}

// TestInducer_EmptyResponse: a stream that yields no text is a parse
// error ("empty response").
func TestInducer_EmptyResponse(t *testing.T) {
	ind := skills.NewInducer(fakeProviderEmitting("x", ""))
	_, err := ind.Induce(context.Background(), "transcript", nil, skills.InducerOptions{})
	if err == nil {
		t.Error("want empty-response parse error")
	}
}

// TestInducer_UnclosedCodeFence: a fenced block with no closing ```
// still parses the inner JSON (stripCodeFence returns the remainder).
func TestInducer_UnclosedCodeFence(t *testing.T) {
	canned := "```json\n{\"skill\": {\"name\": \"unclosed\", \"description\": \"Use when unclosed\", \"body\": \"x\"}}\n"
	ind := skills.NewInducer(fakeProviderEmitting("x", canned))
	prop, err := ind.Induce(context.Background(), "transcript", nil, skills.InducerOptions{})
	if err != nil {
		t.Fatalf("Induce: %v", err)
	}
	if prop == nil || prop.Name != "unclosed" {
		t.Errorf("want 'unclosed' proposal, got %+v", prop)
	}
}

// TestInducer_FenceWithoutNewline: a string that starts with ``` but has
// no newline is returned verbatim by stripCodeFence, then fails to parse
// as JSON.
func TestInducer_FenceWithoutNewline(t *testing.T) {
	ind := skills.NewInducer(fakeProviderEmitting("x", "```not json no newline"))
	_, err := ind.Induce(context.Background(), "transcript", nil, skills.InducerOptions{})
	if err == nil {
		t.Error("want parse error for malformed fence")
	}
}

// TestInducer_LongRawTruncatedInError: a long malformed response is
// truncated in the surfaced parse error (truncateForError path). The
// error must still be non-nil and not echo the entire blob.
func TestInducer_LongRawTruncatedInError(t *testing.T) {
	long := "not json " + strings.Repeat("x", 500)
	ind := skills.NewInducer(fakeProviderEmitting("x", long))
	_, err := ind.Induce(context.Background(), "transcript", nil, skills.InducerOptions{})
	if err == nil {
		t.Fatal("want parse error")
	}
	if strings.Contains(err.Error(), strings.Repeat("x", 500)) {
		t.Error("error should truncate the raw response, not echo it whole")
	}
	if !strings.Contains(err.Error(), "...") {
		t.Error("truncated error should carry an ellipsis")
	}
}

// TestInducer_ProviderLabelDefaultModel: when Model is empty the
// inducer label is just the provider name (no trailing colon).
func TestInducer_ProviderLabelDefaultModel(t *testing.T) {
	canned := `{"skill": {"name": "n", "description": "Use when n", "body": "b"}}`
	ind := skills.NewInducer(fakeProviderEmitting("anthropic", canned))
	prop, err := ind.Induce(context.Background(), "transcript", nil, skills.InducerOptions{}) // no Model
	if err != nil {
		t.Fatal(err)
	}
	if prop.InducerName != "anthropic" {
		t.Errorf("inducer label with default model: want 'anthropic', got %q", prop.InducerName)
	}
}

// TestInducer_IntoSkillZeroCreatedDefaultsToNow: a Proposal with a zero
// Created gets a fresh timestamp at IntoSkill time.
func TestInducer_IntoSkillZeroCreatedDefaultsToNow(t *testing.T) {
	p := &skills.Proposal{Name: "n", Description: "Use when n"}
	s := p.IntoSkill("")
	if s.Created.IsZero() {
		t.Error("zero Created should default to now")
	}
	if s.JudgeModel != "" {
		t.Errorf("empty judge model should pass through, got %q", s.JudgeModel)
	}
}

// TestInducer_HappyPath: canned JSON parses into a Proposal with the
// right fields populated.
func TestInducer_HappyPath(t *testing.T) {
	canned := `{"skill": {"name": "kebab-case-name", "description": "Use when ...", "body": "1. step one\n2. step two\n"}}`
	p := fakeProviderEmitting("anthropic", canned)
	ind := skills.NewInducer(p)
	prop, err := ind.Induce(context.Background(), "transcript text", []string{"Use when existing skill"}, skills.InducerOptions{
		Model:       "claude-sonnet-4-5",
		InducedFrom: []string{"agent-xyz"},
	})
	if err != nil {
		t.Fatalf("Induce: %v", err)
	}
	if prop == nil {
		t.Fatal("Induce returned nil proposal but no error")
	}
	if prop.Name != "kebab-case-name" {
		t.Errorf("name: want kebab-case-name got %q", prop.Name)
	}
	if !strings.HasPrefix(prop.Description, "Use when") {
		t.Errorf("description should start with 'Use when', got %q", prop.Description)
	}
	if !strings.Contains(prop.Body, "step one") {
		t.Errorf("body lost: %q", prop.Body)
	}
	if prop.InducerName != "anthropic:claude-sonnet-4-5" {
		t.Errorf("inducer label: want anthropic:claude-sonnet-4-5 got %q", prop.InducerName)
	}
	if len(prop.InducedFrom) != 1 || prop.InducedFrom[0] != "agent-xyz" {
		t.Errorf("induced_from: %v", prop.InducedFrom)
	}
}

// TestInducer_NotReusable: `{"skill": null}` returns (nil, nil) — the
// expected common case for non-reusable conversations.
func TestInducer_NotReusable(t *testing.T) {
	canned := `{"skill": null, "reason": "single-source lookup"}`
	p := fakeProviderEmitting("openai", canned)
	ind := skills.NewInducer(p)
	prop, err := ind.Induce(context.Background(), "trivial chat", nil, skills.InducerOptions{})
	if err != nil {
		t.Fatalf("Induce: %v", err)
	}
	if prop != nil {
		t.Errorf("want nil proposal, got %+v", prop)
	}
}

// TestInducer_CodeFenceTolerated: model wraps output in ```json fence
// despite our instructions; the parser strips and parses.
func TestInducer_CodeFenceTolerated(t *testing.T) {
	canned := "```json\n{\"skill\": {\"name\": \"fenced\", \"description\": \"Use when fenced\", \"body\": \"x\"}}\n```"
	p := fakeProviderEmitting("ollama", canned)
	ind := skills.NewInducer(p)
	prop, err := ind.Induce(context.Background(), "transcript", nil, skills.InducerOptions{})
	if err != nil {
		t.Fatalf("Induce: %v", err)
	}
	if prop == nil || prop.Name != "fenced" {
		t.Errorf("want fenced proposal, got %+v", prop)
	}
}

// TestInducer_MalformedJSON: garbage response surfaces a parse error.
func TestInducer_MalformedJSON(t *testing.T) {
	p := fakeProviderEmitting("x", "this is not json")
	ind := skills.NewInducer(p)
	_, err := ind.Induce(context.Background(), "transcript", nil, skills.InducerOptions{})
	if err == nil {
		t.Error("want parse error")
	}
}

// TestInducer_MissingFields: skill object missing name/description is a
// parse error.
func TestInducer_MissingFields(t *testing.T) {
	canned := `{"skill": {"body": "only body"}}`
	p := fakeProviderEmitting("x", canned)
	ind := skills.NewInducer(p)
	_, err := ind.Induce(context.Background(), "transcript", nil, skills.InducerOptions{})
	if err == nil {
		t.Error("want error for missing name+description")
	}
}

// TestInducer_NilProvider rejected.
func TestInducer_NilProvider(t *testing.T) {
	ind := &skills.Inducer{}
	_, err := ind.Induce(context.Background(), "x", nil, skills.InducerOptions{})
	if err == nil {
		t.Error("want nil-provider error")
	}
}

// TestInducer_EmptyTranscript rejected.
func TestInducer_EmptyTranscript(t *testing.T) {
	p := fakeProviderEmitting("x", `{"skill": null}`)
	ind := skills.NewInducer(p)
	_, err := ind.Induce(context.Background(), "   ", nil, skills.InducerOptions{})
	if err == nil {
		t.Error("want empty-transcript error")
	}
}

// TestInducer_IntoSkill: Proposal → Skill carries provenance + models
// + body.
func TestInducer_IntoSkill(t *testing.T) {
	p := &skills.Proposal{
		Name:        "n",
		Description: "Use when ...",
		Body:        "b",
		InducerName: "anthropic:claude-sonnet-4-5",
		InducedFrom: []string{"a", "b"},
	}
	s := p.IntoSkill("openai:gpt-4o")
	if s.Provenance != skills.ProvInduced {
		t.Errorf("provenance: %q", s.Provenance)
	}
	if s.InducerModel != "anthropic:claude-sonnet-4-5" {
		t.Errorf("inducer_model: %q", s.InducerModel)
	}
	if s.JudgeModel != "openai:gpt-4o" {
		t.Errorf("judge_model: %q", s.JudgeModel)
	}
	if len(s.InducedFrom) != 2 {
		t.Errorf("induced_from: %v", s.InducedFrom)
	}
	if s.Created.IsZero() {
		t.Error("created should be populated")
	}
}
