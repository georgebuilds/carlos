package memory

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
)

// newMemStore is a local helper analogous to openTestStore in
// frame_test.go but shared with these tests.
func newMemStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestStore_DB returns the underlying handle.
func TestStore_DB(t *testing.T) {
	s := newMemStore(t)
	if s.DB() == nil {
		t.Fatal("DB() should return the open handle")
	}
	// Ping it as a sanity check.
	if err := s.DB().Ping(); err != nil {
		t.Errorf("ping: %v", err)
	}
}

// TestStore_WithTx_Commits exercises the happy path of the helper.
func TestStore_WithTx_Commits(t *testing.T) {
	s := newMemStore(t)
	ctx := context.Background()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO user_model(key, value, updated_at, source) VALUES (?, ?, ?, ?)`, "via-tx", "ok", time.Now().UnixMilli(), "user")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.GetFact(ctx, "via-tx")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || v != "ok" {
		t.Errorf("fact not committed: ok=%v val=%q", ok, v)
	}
}

// TestStore_WithTx_RollsBack on error.
func TestStore_WithTx_RollsBack(t *testing.T) {
	s := newMemStore(t)
	ctx := context.Background()
	sentinel := errors.New("rollback")
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		_, _ = tx.ExecContext(ctx, `INSERT INTO user_model(key, value, updated_at, source) VALUES (?, ?, ?, ?)`, "rolled-back", "ok", time.Now().UnixMilli(), "user")
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("got %v", err)
	}
	_, ok, err := s.GetFact(ctx, "rolled-back")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("rolled-back row should not be visible")
	}
}

// TestNewStore_NilDB rejects.
func TestNewStore_NilDB(t *testing.T) {
	if _, err := NewStore(nil); err == nil {
		t.Error("expected error on nil db")
	}
}

// TestNewStore_HappyPath wraps an externally-owned handle.
func TestNewStore_HappyPath(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "shared.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	// Close on a borrowed handle is a no-op (Store.owned=false).
	if err := s.Close(); err != nil {
		t.Errorf("close on shared: %v", err)
	}
	// DB is still open and usable.
	if err := db.Ping(); err != nil {
		t.Errorf("ping after Close on borrowed: %v", err)
	}
}

// TestRunSearch_NoEnv_FallsBackToHome exercises the home-dir branch.
// We point HOME at a temp dir + seed a DB there so RunSearch finds it.
func TestRunSearch_HomeFallback(t *testing.T) {
	dir := t.TempDir()
	// Sandbox HOME.
	t.Setenv("HOME", dir)
	// Make the env override empty so home is used.
	t.Setenv("CARLOS_STATE_DB", "")
	// Seed ~/.carlos/state.db with one row.
	dbPath := filepath.Join(dir, ".carlos", "state.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if _, err := store.AppendSummary(context.Background(), Summary{
		AgentID: "a", Text: "alphahomesignal",
	}); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	// Capture stdout via RunSearchTo using an empty dbPath so the
	// resolution rule kicks in.
	var buf bytes.Buffer
	if err := RunSearchTo(&buf, "alphahomesignal", 10, "", ""); err != nil {
		t.Fatalf("RunSearchTo: %v", err)
	}
	if !strings.Contains(buf.String(), "alphahomesignal") {
		t.Errorf("expected hit in output: %q", buf.String())
	}
}

// TestRunSearch_EnvOverride uses CARLOS_STATE_DB.
func TestRunSearch_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "via-env.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendSummary(context.Background(), Summary{
		AgentID: "a", Text: "envsignal token",
	}); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	t.Setenv("CARLOS_STATE_DB", dbPath)
	var buf bytes.Buffer
	if err := RunSearchTo(&buf, "envsignal", 10, "", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "envsignal") {
		t.Errorf("got %q", buf.String())
	}
}

// TestRunSearch_TopLevel exercises RunSearch (writes to os.Stdout) at
// least to drive the function-entry counter. We won't capture stdout
// directly; just confirm it returns successfully.
func TestRunSearch_TopLevel(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rs.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	t.Setenv("CARLOS_STATE_DB", dbPath)
	if err := RunSearch("nothingnothing", 5); err != nil {
		// `no matches.` to stdout is success.
		t.Errorf("RunSearch: %v", err)
	}
}

// TestRunSearchInFrame_TopLevel similarly drives the frame variant.
func TestRunSearchInFrame_TopLevel(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rsf.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	t.Setenv("CARLOS_STATE_DB", dbPath)
	if err := RunSearchInFrame("nothingnothing", "work", 5); err != nil {
		t.Errorf("RunSearchInFrame: %v", err)
	}
}

// TestResolveDBPath_Explicit returns the input verbatim.
func TestResolveDBPath_Explicit(t *testing.T) {
	got, err := resolveDBPath("/explicit/path.db")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/explicit/path.db" {
		t.Errorf("got %q", got)
	}
}

// TestResolveDBPath_EnvWins reads CARLOS_STATE_DB.
func TestResolveDBPath_Env(t *testing.T) {
	t.Setenv("CARLOS_STATE_DB", "/env/path.db")
	got, err := resolveDBPath("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/env/path.db" {
		t.Errorf("got %q", got)
	}
}

// TestResolveDBPath_HomeFallback returns ~/.carlos/state.db.
func TestResolveDBPath_Home(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CARLOS_STATE_DB", "")
	got, err := resolveDBPath("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".carlos", "state.db")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// TestFormatSearchHit_NoFrame omits the frame segment.
func TestFormatSearchHit_NoFrame(t *testing.T) {
	h := Summary{
		AgentID:  "abc12345xyz",
		ClosedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Text:     "hello world",
	}
	s := formatSearchHit(h)
	if strings.Count(s, "[") != 1 {
		t.Errorf("no-frame variant should have exactly one bracket pair; got %q", s)
	}
	if !strings.Contains(s, "[abc12345]") {
		t.Errorf("agent id truncation wrong: %q", s)
	}
}

// TestFormatSearchHit_WithFrame includes the frame.
func TestFormatSearchHit_WithFrame(t *testing.T) {
	h := Summary{
		AgentID: "ab",
		Frame:   "work",
		Text:    "hi",
	}
	s := formatSearchHit(h)
	if !strings.Contains(s, "[work]") {
		t.Errorf("frame missing: %q", s)
	}
	// AgentID shorter than 8 stays verbatim.
	if !strings.Contains(s, "[ab]") {
		t.Errorf("short agent id wrong: %q", s)
	}
}

// TestFormatSearchHit_TruncatesLongText caps the text at 200 runes.
func TestFormatSearchHit_LongText(t *testing.T) {
	long := strings.Repeat("a", 300)
	h := Summary{AgentID: "a", Text: long}
	s := formatSearchHit(h)
	if !strings.Contains(s, "…") {
		t.Errorf("expected truncation marker: %q", s[:80])
	}
}

// TestFormatSearchHit_CollapsesNewlines replaces them with spaces.
func TestFormatSearchHit_Newlines(t *testing.T) {
	h := Summary{AgentID: "a", Text: "line1\nline2"}
	s := formatSearchHit(h)
	if strings.Contains(s, "\n") {
		t.Errorf("newline should be stripped from text portion: %q", s)
	}
	if !strings.Contains(s, "line1 line2") {
		t.Errorf("newline should become space: %q", s)
	}
}

// TestApproxTokens computes the rough English approximation.
func TestApproxTokens(t *testing.T) {
	if approxTokens("") != 0 {
		t.Error("empty string should yield 0")
	}
	if approxTokens("abcd") != 1 {
		t.Errorf("4 chars should be 1 token, got %d", approxTokens("abcd"))
	}
	if approxTokens("ab") != 1 {
		t.Errorf("2 chars rounds up to 1, got %d", approxTokens("ab"))
	}
}

// TestFirstTextBlock returns the first text block payload.
func TestFirstTextBlock(t *testing.T) {
	m := providers.Message{
		Role: "user",
		Content: []providers.Block{
			{Kind: "tool_use", ToolName: "x"},
			{Kind: "text", Text: "hello"},
			{Kind: "text", Text: "later"},
		},
	}
	if got := firstTextBlock(m); got != "hello" {
		t.Errorf("got %q", got)
	}
}

// TestFirstTextBlock_NoText returns empty.
func TestFirstTextBlock_None(t *testing.T) {
	m := providers.Message{
		Role: "user",
		Content: []providers.Block{
			{Kind: "tool_use", ToolName: "x"},
		},
	}
	if got := firstTextBlock(m); got != "" {
		t.Errorf("got %q", got)
	}
}

// TestFlattenConversation joins messages with role prefixes + handles
// each Block kind.
func TestFlattenConversation_AllKinds(t *testing.T) {
	msgs := []providers.Message{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hi"}}},
		{Role: "assistant", Content: []providers.Block{
			{Kind: "tool_use", ToolName: "search"},
		}},
		{Role: "user", Content: []providers.Block{
			{Kind: "tool_result", ToolUseID: "t1"},
		}},
		{Role: "assistant", Content: []providers.Block{
			{Kind: "weird", Text: "fallback text"},
			{Kind: "weird"}, // no text either
		}},
	}
	got := flattenConversation(msgs)
	for _, want := range []string{"USER:", "ASSISTANT:", "[tool_use: search]", "[tool_result: t1]", "fallback text"} {
		if !strings.Contains(got, want) {
			t.Errorf("flattened conversation missing %q in %q", want, got)
		}
	}
}

// TestFlattenConversation_Empty returns empty.
func TestFlattenConversation_Empty(t *testing.T) {
	if got := flattenConversation(nil); got != "" {
		t.Errorf("got %q", got)
	}
}

// TestLLMSummarizer_NoMessages rejects empty input.
func TestLLMSummarizer_NoMessages(t *testing.T) {
	s := LLMSummarizer{Provider: &capturingProvider{}}
	_, _, err := s.Summarize(context.Background(), nil)
	if err == nil {
		t.Error("expected error")
	}
}

// TestLLMSummarizer_EmptyResponse errors when the provider returns no
// text.
func TestLLMSummarizer_EmptyResponse(t *testing.T) {
	p := &capturingProvider{response: "  "}
	s := LLMSummarizer{Provider: p}
	_, _, err := s.Summarize(context.Background(), []providers.Message{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: "x"}}},
	})
	if err == nil {
		t.Error("expected empty-response error")
	}
}

// TestLLMSummarizer_StreamOpenError propagates the provider's error.
func TestLLMSummarizer_OpenError(t *testing.T) {
	p := &errProvider{}
	s := LLMSummarizer{Provider: p}
	_, _, err := s.Summarize(context.Background(), []providers.Message{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: "x"}}},
	})
	if err == nil {
		t.Error("expected stream open error")
	}
}

// TestLLMSummarizer_EventErrorAborts surfaces an event-error to the
// caller.
func TestLLMSummarizer_EventError(t *testing.T) {
	p := &eventErrorProvider{}
	s := LLMSummarizer{Provider: p}
	_, _, err := s.Summarize(context.Background(), []providers.Message{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: "x"}}},
	})
	if err == nil {
		t.Error("expected event error to propagate")
	}
}

// TestNaiveSummarizer_NoUser falls back to the last block of any role.
func TestNaiveSummarizer_NoUser(t *testing.T) {
	msgs := []providers.Message{
		{Role: "assistant", Content: []providers.Block{{Kind: "text", Text: "only assistant"}}},
	}
	text, _, err := NaiveSummarizer{}.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "only assistant") {
		t.Errorf("fallback failed: %q", text)
	}
}

// TestStore_AppendSummary_NilStore guards the nil receiver.
func TestStore_NilReceivers(t *testing.T) {
	var s *Store
	ctx := context.Background()
	if _, err := s.AppendSummary(ctx, Summary{AgentID: "a", Text: "x"}); err == nil {
		t.Error("AppendSummary nil receiver should error")
	}
	if _, err := s.Search(ctx, "x", 10); err == nil {
		t.Error("Search nil receiver should error")
	}
	if _, err := s.RecentSummaries(ctx, 10); err == nil {
		t.Error("RecentSummaries nil receiver should error")
	}
	if _, _, err := s.GetFact(ctx, "k"); err == nil {
		t.Error("GetFact nil receiver should error")
	}
	if _, err := s.ListFacts(ctx); err == nil {
		t.Error("ListFacts nil receiver should error")
	}
	if err := s.ApplyFact(ctx, "k", "v", "u"); err == nil {
		t.Error("ApplyFact nil receiver should error")
	}
	if _, err := s.ProposeFactWrite(ctx, &fakeSink{}, "k", "v", ""); err == nil {
		t.Error("ProposeFactWrite nil receiver should error")
	}
}

// TestListFacts_LegacySourceNullCoalesces hits the COALESCE branch.
func TestListFacts_LegacySource(t *testing.T) {
	s := newMemStore(t)
	ctx := context.Background()
	// Direct insert with NULL source so COALESCE produces "".
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO user_model(key, value, updated_at, source) VALUES (?, ?, ?, NULL)`,
		"legacy", "value", time.Now().UnixMilli(),
	); err != nil {
		t.Fatal(err)
	}
	out, err := s.ListFacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	if out[0].Source != "" {
		t.Errorf("legacy source: want empty, got %q", out[0].Source)
	}
}

// TestStore_AppendSummary_ZeroTimeStampsNow checks the IsZero branch.
func TestAppendSummary_ZeroTimeStampsNow(t *testing.T) {
	s := newMemStore(t)
	ctx := context.Background()
	id, err := s.AppendSummary(ctx, Summary{
		AgentID: "x", Text: "first",
		// ClosedAt zero on purpose
	})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := s.RecentSummaries(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != id {
		t.Fatalf("want 1 hit with id %d", id)
	}
	if hits[0].ClosedAt.IsZero() {
		t.Error("ClosedAt should have been stamped")
	}
}

// TestSearch_FrameNotPresentReturnsEmpty exercises the frame-scoped
// search path for a frame with no matching rows.
func TestSearchInFrame_NoRows(t *testing.T) {
	s := newMemStore(t)
	ctx := context.Background()
	if _, err := s.AppendSummary(ctx, Summary{AgentID: "a", Text: "alpha", Frame: "work"}); err != nil {
		t.Fatal(err)
	}
	out, err := s.SearchInFrame(ctx, "alpha", "personal", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("want 0, got %d", len(out))
	}
}

// TestRecentInFrame_EmptyResults - frame with no rows yields empty.
func TestRecentInFrame_Empty(t *testing.T) {
	s := newMemStore(t)
	out, err := s.RecentInFrame(context.Background(), "empty-frame", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("want 0, got %d", len(out))
	}
}

// TestRecentInFrame_DefaultLimit covers the limit<=0 branch.
func TestRecentInFrame_DefaultLimit(t *testing.T) {
	s := newMemStore(t)
	out, err := s.RecentInFrame(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = out
}

// TestSearchInFrame_EmptyQueryRejected.
func TestSearchInFrame_EmptyQuery(t *testing.T) {
	s := newMemStore(t)
	if _, err := s.SearchInFrame(context.Background(), "", "work", 10); err == nil {
		t.Error("expected error")
	}
}

// TestApplyFact_NilStore was covered above but ensure direct nil
// covers ListFacts and GetFact short-circuits.
func TestNilStore_Direct(t *testing.T) {
	var s *Store
	if err := s.Close(); err != nil {
		t.Errorf("Close on nil should be no-op, got %v", err)
	}
}

// TestStore_OpenStore_MkdirError surfaces a mkdir failure path.
func TestOpenStore_MkdirError(t *testing.T) {
	// Create a file at the spot where parent dir would go.
	dir := t.TempDir()
	conflict := filepath.Join(dir, "file-not-a-dir")
	if err := os.WriteFile(conflict, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Try to open under that file. MkdirAll will fail because the
	// parent is a regular file.
	target := filepath.Join(conflict, "sub", "state.db")
	_, err := OpenStore(target)
	if err == nil {
		t.Error("expected mkdir error")
	}
}

// fakeSink minimally implements ProposalSink for nil-receiver tests.
type fakeSink struct{}

func (f *fakeSink) WriteProposalArtifact(_ context.Context, _, _ string, _ []byte) (ProposalRef, error) {
	return ProposalRef{}, nil
}
func (f *fakeSink) ProposeApproval(_ context.Context, _, _ string, _ ProposalRef) error {
	return nil
}

// errSink fails WriteProposalArtifact.
type errSink struct{ writeErr, approvalErr error }

func (e *errSink) WriteProposalArtifact(_ context.Context, _, _ string, _ []byte) (ProposalRef, error) {
	if e.writeErr != nil {
		return ProposalRef{}, e.writeErr
	}
	return ProposalRef{ID: "ref-1"}, nil
}
func (e *errSink) ProposeApproval(_ context.Context, _, _ string, _ ProposalRef) error {
	return e.approvalErr
}

// TestProposeFactWrite_ArtifactError surfaces a failing write.
func TestProposeFactWrite_ArtifactError(t *testing.T) {
	s := newMemStore(t)
	_, err := s.ProposeFactWrite(context.Background(), &errSink{writeErr: errors.New("boom")}, "k", "v", "r")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected write error wrapped, got %v", err)
	}
}

// TestProposeFactWrite_ApprovalError still returns the ref ID.
func TestProposeFactWrite_ApprovalError(t *testing.T) {
	s := newMemStore(t)
	id, err := s.ProposeFactWrite(context.Background(), &errSink{approvalErr: errors.New("queue full")}, "k", "v", "r")
	if err == nil {
		t.Fatal("expected approval error")
	}
	if id != "ref-1" {
		t.Errorf("expected ref id to be returned even on approval err; got %q", id)
	}
}

// capturingProvider records the request and emits one EventTextDelta
// with the configured response.
type capturingProvider struct {
	response string
	got      providers.Request
}

func (c *capturingProvider) Name() string                         { return "cap" }
func (c *capturingProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (c *capturingProvider) Stream(_ context.Context, req providers.Request) (<-chan providers.Event, error) {
	c.got = req
	ch := make(chan providers.Event, 1)
	ch <- providers.Event{Kind: providers.EventTextDelta, Text: c.response}
	close(ch)
	return ch, nil
}

// errProvider returns an error from Stream() before any events.
type errProvider struct{}

func (e *errProvider) Name() string                         { return "err" }
func (e *errProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (e *errProvider) Stream(_ context.Context, _ providers.Request) (<-chan providers.Event, error) {
	return nil, errors.New("stream open boom")
}

// eventErrorProvider emits an EventError event mid-stream.
type eventErrorProvider struct{}

func (e *eventErrorProvider) Name() string                         { return "eerr" }
func (e *eventErrorProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (e *eventErrorProvider) Stream(_ context.Context, _ providers.Request) (<-chan providers.Event, error) {
	ch := make(chan providers.Event, 2)
	ch <- providers.Event{Kind: providers.EventTextDelta, Text: "partial"}
	ch <- providers.Event{Kind: providers.EventError, Err: errors.New("mid-stream")}
	close(ch)
	return ch, nil
}
