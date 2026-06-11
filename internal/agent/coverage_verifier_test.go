package agent_test

// Coverage for Verifier.Verify stream/collect error branches and the
// empty-body parse path, driven by scripted fake judges that emit an
// error event or no text at all.

import (
	"context"
	"errors"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
)

// TestVerifier_StreamErrorSurfaces drives a judge whose stream emits an
// EventError, exercising collectJudgeText's error return and Verify's
// collect-error wrap.
func TestVerifier_StreamErrorSurfaces(t *testing.T) {
	judge := fake.New("a", fake.Script{
		{Kind: providers.EventTextDelta, Text: "partial"},
		{Kind: providers.EventError, Err: errors.New("transport reset")},
	})
	v := &agent.Verifier{Judge: judge}
	_, err := v.Verify(context.Background(), agent.ArtifactRef{ID: "r"}, []byte("body"))
	if err == nil {
		t.Fatal("expected collect error when the judge stream errors")
	}
}

// TestVerifier_EmptyBodyErrors drives a judge that emits only a stop
// reason (no text), so parseJudgeResponse hits the empty-body branch and
// Verify returns ErrMalformedJudgeResponse.
func TestVerifier_EmptyBodyErrors(t *testing.T) {
	judge := fake.New("a", fake.Script{
		{Kind: providers.EventStopReason, Stop: "end_turn"},
	})
	v := &agent.Verifier{Judge: judge}
	_, err := v.Verify(context.Background(), agent.ArtifactRef{ID: "r"}, []byte("body"))
	if !errors.Is(err, agent.ErrMalformedJudgeResponse) {
		t.Fatalf("expected ErrMalformedJudgeResponse on empty body, got %v", err)
	}
}
