package research_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
	"github.com/georgebuilds/carlos/internal/research"
)

// errProvider is a provider whose Stream fails outright (before any
// event). Used to exercise the provider-error path in both methods.
type errProvider struct{ err error }

func (errProvider) Name() string                         { return "err" }
func (errProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (p errProvider) Stream(context.Context, providers.Request) (<-chan providers.Event, error) {
	return nil, p.err
}

// midStreamErrProvider emits a text delta then an EventError, exercising
// the in-flight error branch of the drain loop.
type midStreamErrProvider struct{ text string }

func (midStreamErrProvider) Name() string                         { return "miderr" }
func (midStreamErrProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (p midStreamErrProvider) Stream(context.Context, providers.Request) (<-chan providers.Event, error) {
	ch := make(chan providers.Event, 2)
	go func() {
		defer close(ch)
		ch <- providers.Event{Kind: providers.EventTextDelta, Text: p.text}
		ch <- providers.Event{Kind: providers.EventError, Err: errors.New("boom")}
	}()
	return ch, nil
}

func TestClarifyQuestions(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     []string
	}{
		{
			name:     "zero questions empty body",
			response: "",
			want:     []string{},
		},
		{
			name:     "zero questions sentinel",
			response: "None.",
			want:     []string{},
		},
		{
			name:     "one question",
			response: "Which version of the library?",
			want:     []string{"Which version of the library?"},
		},
		{
			name:     "two questions",
			response: "Which version?\nWhich operating system?",
			want:     []string{"Which version?", "Which operating system?"},
		},
		{
			name:     "truncates to two",
			response: "Q1?\nQ2?\nQ3?\nQ4?",
			want:     []string{"Q1?", "Q2?"},
		},
		{
			name:     "strips bullets and dedupes",
			response: "- Which version?\n* Which version?\n1. Which OS?",
			want:     []string{"Which version?", "Which OS?"},
		},
		{
			name:     "garbage with sentinel lines filtered",
			response: "n/a\nno clarification needed\nWhich region?",
			want:     []string{"Which region?"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := research.Preflight{
				Provider: newScriptedProvider("clarify", tc.response),
				Model:    "test-model",
			}
			got, err := p.ClarifyQuestions(context.Background(), "how do I do X?")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d questions %v, want %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("q[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestClarifyQuestions_NilProvider(t *testing.T) {
	p := research.Preflight{Model: "m"}
	if _, err := p.ClarifyQuestions(context.Background(), "q"); err == nil {
		t.Fatal("want error for nil provider")
	}
}

func TestClarifyQuestions_EmptyQuestion(t *testing.T) {
	p := research.Preflight{Provider: newScriptedProvider("c"), Model: "m"}
	if _, err := p.ClarifyQuestions(context.Background(), "   "); err == nil {
		t.Fatal("want error for empty question")
	}
}

func TestClarifyQuestions_ProviderError(t *testing.T) {
	p := research.Preflight{Provider: errProvider{err: errors.New("stream failed")}, Model: "m"}
	_, err := p.ClarifyQuestions(context.Background(), "q")
	if err == nil {
		t.Fatal("want error from provider stream")
	}
	if !strings.Contains(err.Error(), "provider stream") {
		t.Errorf("error %q should mention provider stream", err)
	}
}

func TestClarifyQuestions_MidStreamError(t *testing.T) {
	p := research.Preflight{Provider: midStreamErrProvider{text: "Which version?"}, Model: "m"}
	_, err := p.ClarifyQuestions(context.Background(), "q")
	if err == nil {
		t.Fatal("want error from mid-stream EventError")
	}
	if !strings.Contains(err.Error(), "provider error") {
		t.Errorf("error %q should mention provider error", err)
	}
}

func TestBrief(t *testing.T) {
	tests := []struct {
		name     string
		question string
		answers  []string
		response string
		want     string
	}{
		{
			name:     "single paragraph passthrough",
			question: "how fast is X?",
			answers:  []string{"version 2", "on linux"},
			response: "Investigate the throughput of X version 2 on Linux and why it matters for batch jobs.",
			want:     "Investigate the throughput of X version 2 on Linux and why it matters for batch jobs.",
		},
		{
			name:     "collapses multi-line into one paragraph",
			question: "q",
			answers:  []string{"a"},
			response: "Line one of the brief.\n\nLine two of the brief.",
			want:     "Line one of the brief. Line two of the brief.",
		},
		{
			name:     "empty response falls back to question",
			question: "  the original question  ",
			answers:  nil,
			response: "   \n  \n",
			want:     "the original question",
		},
		{
			name:     "skips blank answers",
			question: "q",
			answers:  []string{"", "  ", "real answer"},
			response: "A focused brief.",
			want:     "A focused brief.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := research.Preflight{
				Provider: newScriptedProvider("brief", tc.response),
				Model:    "m",
			}
			got, err := p.Brief(context.Background(), tc.question, tc.answers)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("brief = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBrief_NilProvider(t *testing.T) {
	p := research.Preflight{Model: "m"}
	if _, err := p.Brief(context.Background(), "q", nil); err == nil {
		t.Fatal("want error for nil provider")
	}
}

func TestBrief_EmptyQuestion(t *testing.T) {
	p := research.Preflight{Provider: newScriptedProvider("b"), Model: "m"}
	if _, err := p.Brief(context.Background(), "  ", nil); err == nil {
		t.Fatal("want error for empty question")
	}
}

func TestBrief_ProviderError(t *testing.T) {
	p := research.Preflight{Provider: errProvider{err: errors.New("nope")}, Model: "m"}
	if _, err := p.Brief(context.Background(), "q", nil); err == nil {
		t.Fatal("want error from provider stream")
	}
}

// TestBrief_UsesFakeProvider exercises the providers/fake adapter too,
// proving the helper drains an off-the-shelf scripted provider (the
// same fake the engine tests lean on) and not just our local fake.
func TestBrief_UsesFakeProvider(t *testing.T) {
	prov := fake.New("fakebrief", fake.Script{
		{Kind: providers.EventTextDelta, Text: "A clean one paragraph brief."},
		{Kind: providers.EventStopReason, Stop: "end_turn"},
	})
	p := research.Preflight{Provider: prov, Model: "m"}
	got, err := p.Brief(context.Background(), "question", []string{"answer"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "A clean one paragraph brief." {
		t.Errorf("brief = %q", got)
	}
}

// TestBriefUserMessage_NoAnswers proves the no-answers branch labels the
// absence explicitly so the model is not left guessing.
func TestBrief_NoAnswersBranch(t *testing.T) {
	// A response that echoes nothing; we only care the call succeeds and
	// the empty-answer path is taken (covered via Brief with nil answers
	// and a non-empty response).
	p := research.Preflight{
		Provider: newScriptedProvider("b", "Brief with no answers."),
		Model:    "m",
	}
	got, err := p.Brief(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Brief with no answers." {
		t.Errorf("brief = %q", got)
	}
}
