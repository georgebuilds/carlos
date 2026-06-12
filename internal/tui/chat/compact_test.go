package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/memory"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tui/slash"
)

// fakeSummarizer is a controllable memory.Summarizer for testing the
// /compact pipeline without firing real provider calls.
type fakeSummarizer struct {
	mu     sync.Mutex
	calls  [][]providers.Message
	output string
	err    error
}

func (f *fakeSummarizer) Summarize(_ context.Context, msgs []providers.Message) (string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Copy the slice so a later mutation by the caller can't poison
	// our recording.
	cp := make([]providers.Message, len(msgs))
	copy(cp, msgs)
	f.calls = append(f.calls, cp)
	if f.err != nil {
		return "", 0, f.err
	}
	return f.output, len(f.output) / 4, nil
}

func (f *fakeSummarizer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// Compile-time check: the fake satisfies the interface.
var _ memory.Summarizer = (*fakeSummarizer)(nil)

// TestWithSummarizer_SetsField proves the option wires through.
func TestWithSummarizer_SetsField(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000CMP001"
	seedAgent(t, log, agentID, "summarizer wires", "fake")

	fs := &fakeSummarizer{output: "ok"}
	m := New(log, agentID, NewMemTextSource(), WithSummarizer(fs))
	if m.summarizer == nil {
		t.Fatal("WithSummarizer didn't set the field")
	}
	if m.summarizer != fs {
		t.Error("WithSummarizer stored a different value")
	}
}

// TestCompactSlash_NilSummarizerEchoesNotConfigured covers the
// degenerate path: dev-aid chat surface without a summarizer should
// echo a friendly hint, not crash.
func TestCompactSlash_NilSummarizerEchoesNotConfigured(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000CMP002"
	seedAgent(t, log, agentID, "no summarizer", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	c, _ := slash.Parse("/compact")
	cmd := m.dispatchSlash(c)
	if cmd == nil {
		t.Fatal("dispatchSlash(/compact) returned nil")
	}
	st, ok := cmd().(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", cmd())
	}
	if !strings.Contains(st.text, "not configured") {
		t.Errorf("expected 'not configured' hint: %q", st.text)
	}
	if st.kind != statusWarn {
		t.Errorf("kind = %d, want statusWarn", st.kind)
	}
}

// TestCompactSlash_EmptyTranscriptShortCircuits asserts the "nothing
// to compact" short-circuit: with no user/assistant rows the
// summarizer should not be called.
func TestCompactSlash_EmptyTranscriptShortCircuits(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000CMP003"
	seedAgent(t, log, agentID, "empty transcript", "fake")
	fs := &fakeSummarizer{output: "should not be invoked"}
	m := New(log, agentID, NewMemTextSource(), WithSummarizer(fs))
	m = drive(t, m, 120, 30)

	c, _ := slash.Parse("/compact")
	cmd := m.dispatchSlash(c)
	if cmd == nil {
		t.Fatal("dispatchSlash returned nil")
	}
	st, ok := cmd().(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", cmd())
	}
	if !strings.Contains(st.text, "nothing to compact") {
		t.Errorf("expected 'nothing to compact': %q", st.text)
	}
	if fs.callCount() != 0 {
		t.Errorf("summarizer called %d times for empty transcript", fs.callCount())
	}
}

// TestCompactSlash_HappyPath verifies the full pipeline:
//
//	transcript → summarize → EvtSessionReset + synthetic EvtUserMessage.
//
// The chatglue.buildHistory contract is the load-bearing invariant —
// we assert the event sequence so a future regression on the reset
// marker would surface here.
func TestCompactSlash_HappyPath(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000CMP004"
	seedAgent(t, log, agentID, "happy compact", "fake")
	fs := &fakeSummarizer{output: "User asked about Go. Assistant explained types. Open thread: generics."}

	m := New(log, agentID, NewMemTextSource(), WithSummarizer(fs))
	m = drive(t, m, 120, 30)

	// Seed a realistic transcript directly via applyEvent so the
	// in-memory state matches what a real conversation produces.
	apply := func(kind entryKind, text string) {
		m.transcript = append(m.transcript, transcriptEntry{
			kind: kind,
			ts:   time.Now().UTC(),
			text: text,
		})
	}
	apply(entryUserMessage, "what is Go?")
	apply(entryAssistantMessage, "Go is a statically-typed language.")
	apply(entryToolCall, "ignored")    // skipped — non-conversational
	apply(entryStateChange, "ignored") // skipped — non-conversational
	apply(entryUserMessage, "tell me about generics")
	apply(entryAssistantMessage, "Generics landed in 1.18.")

	c, _ := slash.Parse("/compact")
	cmd := m.dispatchSlash(c)
	if cmd == nil {
		t.Fatal("dispatchSlash returned nil")
	}
	st, ok := cmd().(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", cmd())
	}
	if !strings.Contains(st.text, "conversation compacted") {
		t.Errorf("expected 'conversation compacted' confirmation: %q", st.text)
	}
	if st.kind != statusInfo {
		t.Errorf("kind = %d, want statusInfo", st.kind)
	}

	// Summarizer was called exactly once with the projected history.
	if fs.callCount() != 1 {
		t.Fatalf("summarizer called %d times, want 1", fs.callCount())
	}
	gotMsgs := fs.calls[0]
	if len(gotMsgs) != 4 {
		t.Fatalf("projected history = %d rows, want 4 (user/asst pairs only)", len(gotMsgs))
	}
	// First user message + first assistant + second user + second
	// assistant in chronological order.
	want := []struct{ role, text string }{
		{"user", "what is Go?"},
		{"assistant", "Go is a statically-typed language."},
		{"user", "tell me about generics"},
		{"assistant", "Generics landed in 1.18."},
	}
	for i, w := range want {
		if gotMsgs[i].Role != w.role {
			t.Errorf("row %d role = %q, want %q", i, gotMsgs[i].Role, w.role)
		}
		if len(gotMsgs[i].Content) != 1 || gotMsgs[i].Content[0].Text != w.text {
			t.Errorf("row %d text mismatch: got %+v, want %q", i, gotMsgs[i].Content, w.text)
		}
	}

	// Event log: a fresh chat reload should see EvtSessionReset followed
	// by a synthetic EvtUserMessage carrying the summary prefix.
	evs, err := log.Read(context.Background(), agentID, 0)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	// The seeded created event is first; we need the LAST reset event
	// and the user_message that comes AFTER it.
	var resetSeq int64 = -1
	var sawUserAfter bool
	var seedText string
	for _, ev := range evs {
		if ev.Type == agent.EvtSessionReset {
			resetSeq = ev.Seq
		}
		if ev.Type == agent.EvtUserMessage && resetSeq != -1 && ev.Seq > resetSeq {
			sawUserAfter = true
			var p agent.MessagePayload
			_ = json.Unmarshal(ev.Payload, &p)
			seedText = p.Text
		}
	}
	if resetSeq == -1 {
		t.Fatal("expected EvtSessionReset in log")
	}
	if !sawUserAfter {
		t.Fatal("expected EvtUserMessage AFTER the reset")
	}
	if !strings.HasPrefix(seedText, "[compacted summary]") {
		t.Errorf("synthetic user message missing prefix: %q", seedText)
	}
	if !strings.Contains(seedText, "generics") {
		t.Errorf("synthetic user message missing summary body: %q", seedText)
	}
}

// TestCompactSlash_SummarizerErrorSurfaces makes sure a failed
// summarize call lands as a warn statusMsg rather than crashing or
// silently leaving the user wondering why nothing happened.
func TestCompactSlash_SummarizerErrorSurfaces(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000CMP005"
	seedAgent(t, log, agentID, "summarizer fails", "fake")
	fs := &fakeSummarizer{err: errors.New("rate limited")}

	m := New(log, agentID, NewMemTextSource(), WithSummarizer(fs))
	m = drive(t, m, 120, 30)

	m.transcript = append(m.transcript,
		transcriptEntry{kind: entryUserMessage, text: "hi"},
		transcriptEntry{kind: entryAssistantMessage, text: "hello"})

	c, _ := slash.Parse("/compact")
	cmd := m.dispatchSlash(c)
	if cmd == nil {
		t.Fatal("dispatchSlash returned nil")
	}
	st, ok := cmd().(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", cmd())
	}
	if !strings.Contains(st.text, "compact failed") || !strings.Contains(st.text, "rate limited") {
		t.Errorf("expected 'compact failed: rate limited' shape: %q", st.text)
	}
	if st.kind != statusWarn {
		t.Errorf("kind = %d, want statusWarn", st.kind)
	}

	// No reset / synthetic user should land — a failed compact is
	// non-destructive.
	evs, err := log.Read(context.Background(), agentID, 0)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, ev := range evs {
		if ev.Type == agent.EvtSessionReset {
			t.Error("EvtSessionReset written even though summarize failed")
		}
	}
}

// TestCompactSlash_EmptySummaryRejected covers the "summarizer
// returned blank" defensive guard: the chat should refuse to write
// a reset + empty user_message pair.
func TestCompactSlash_EmptySummaryRejected(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000CMP006"
	seedAgent(t, log, agentID, "empty summary", "fake")
	fs := &fakeSummarizer{output: "   \n\n  "}

	m := New(log, agentID, NewMemTextSource(), WithSummarizer(fs))
	m = drive(t, m, 120, 30)
	m.transcript = append(m.transcript,
		transcriptEntry{kind: entryUserMessage, text: "hi"})

	c, _ := slash.Parse("/compact")
	cmd := m.dispatchSlash(c)
	if cmd == nil {
		t.Fatal("dispatchSlash returned nil")
	}
	st, ok := cmd().(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", cmd())
	}
	if !strings.Contains(st.text, "compact failed") {
		t.Errorf("expected 'compact failed' on empty summary: %q", st.text)
	}
	if st.kind != statusWarn {
		t.Errorf("kind = %d, want statusWarn", st.kind)
	}
}

// TestTranscriptToMessages_FiltersNonConversational pins the
// projection logic: only user + assistant rows make it through; every
// other kind drops.
func TestTranscriptToMessages_FiltersNonConversational(t *testing.T) {
	in := []transcriptEntry{
		{kind: entryUserMessage, text: "u1"},
		{kind: entryToolCall, text: "tc"},
		{kind: entryAssistantMessage, text: "a1"},
		{kind: entryToolResult, text: "tr"},
		{kind: entrySteering, text: "steer"},
		{kind: entryStateChange, text: "sc"},
		{kind: entrySystemNote, text: "sys"},
		{kind: entryResearchProgress, text: "🔬"},
		{kind: entryUserMessage, text: ""},        // empty drops
		{kind: entryAssistantMessage, text: "a2"}, // keeper
	}
	got := transcriptToMessages(in)
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3 (u1 / a1 / a2)", len(got))
	}
	wantRoles := []string{"user", "assistant", "assistant"}
	wantTexts := []string{"u1", "a1", "a2"}
	for i := range got {
		if got[i].Role != wantRoles[i] {
			t.Errorf("row %d role = %q, want %q", i, got[i].Role, wantRoles[i])
		}
		if got[i].Content[0].Text != wantTexts[i] {
			t.Errorf("row %d text = %q, want %q", i, got[i].Content[0].Text, wantTexts[i])
		}
	}
}

// TestCompactBuiltin_RegisteredInSlash guards the /help discovery
// path: /compact must surface in the help overlay.
func TestCompactBuiltin_RegisteredInSlash(t *testing.T) {
	spec, ok := slash.Lookup("compact")
	if !ok {
		t.Fatal("/compact not registered in slash.Builtins")
	}
	if !strings.Contains(strings.ToLower(spec.Description), "compact") &&
		!strings.Contains(strings.ToLower(spec.Description), "summar") {
		t.Errorf("compact description should describe summarization: %q", spec.Description)
	}
}

// Sanity-print helper for failed assertions; kept exported-package-
// private so it's available to other tests in the chat package if
// needed without leaking through the public API. Returns a
// human-readable dump of the event stream.
func dumpEvents(t *testing.T, log *agent.SQLiteEventLog, agentID string) string {
	t.Helper()
	evs, err := log.Read(context.Background(), agentID, 0)
	if err != nil {
		return "read err: " + err.Error()
	}
	var b strings.Builder
	for _, ev := range evs {
		fmt.Fprintf(&b, "seq=%d type=%s ts=%s\n", ev.Seq, ev.Type, ev.TS.Format(time.RFC3339Nano))
	}
	return b.String()
}

// reference dumpEvents so go vet doesn't complain in stripped builds.
var _ = dumpEvents
