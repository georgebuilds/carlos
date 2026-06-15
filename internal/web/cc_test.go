package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A small but representative CC session: a plain user turn, an assistant
// turn carrying text + a tool_use, the matching tool_result, plus a
// sidechain record that MUST be skipped and metadata lines that map to
// nothing. This is the §12 conformance fixture (JSONL in, []WireEvent out).
const ccFixture = `{"type":"mode","mode":"normal","sessionId":"s1"}
{"type":"user","timestamp":"2026-06-13T10:00:00.000Z","cwd":"/Users/george/Code/anneal","message":{"role":"user","content":"hello claude"}}
{"type":"assistant","timestamp":"2026-06-13T10:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"on it"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}
{"type":"user","timestamp":"2026-06-13T10:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":false,"content":"file1\nfile2"}]}}
{"type":"assistant","isSidechain":true,"timestamp":"2026-06-13T10:00:03.000Z","message":{"role":"assistant","content":[{"type":"text","text":"subagent noise"}]}}
`

func dataField(t *testing.T, we WireEvent, key string) any {
	t.Helper()
	m, ok := we.Data.(map[string]any)
	if !ok {
		t.Fatalf("event %d data is %T, want map", we.Seq, we.Data)
	}
	return m[key]
}

func TestCCMap_GoldenTranscript(t *testing.T) {
	evs := ccRecordsToWire("cc:s1", []byte(ccFixture))

	if len(evs) != 4 {
		t.Fatalf("got %d events, want 4 (sidechain + metadata dropped): %+v", len(evs), evs)
	}
	wantKinds := []string{"user_message", "assistant_message", "tool_call", "tool_result"}
	for i, we := range evs {
		if we.Kind != wantKinds[i] {
			t.Errorf("event %d kind = %q, want %q", i, we.Kind, wantKinds[i])
		}
		if we.Seq != int64(i+1) {
			t.Errorf("event %d seq = %d, want %d (emission ordinal)", i, we.Seq, i+1)
		}
		if we.Thread != "cc:s1" {
			t.Errorf("event %d thread = %q, want cc:s1", i, we.Thread)
		}
	}
	if got := dataField(t, evs[0], "text"); got != "hello claude" {
		t.Errorf("user text = %v, want 'hello claude'", got)
	}
	if got := dataField(t, evs[1], "text"); got != "on it" {
		t.Errorf("assistant text = %v, want 'on it'", got)
	}
	if got := dataField(t, evs[2], "name"); got != "Bash" {
		t.Errorf("tool_call name = %v, want Bash", got)
	}
	// tool_result name is correlated from the earlier tool_use id.
	if got := dataField(t, evs[3], "name"); got != "Bash" {
		t.Errorf("tool_result name = %v, want Bash (correlated by tool_use_id)", got)
	}
	if got := dataField(t, evs[3], "output_preview"); got != "file1\nfile2" {
		t.Errorf("tool_result preview = %q, want 'file1\\nfile2'", got)
	}
	if got := dataField(t, evs[3], "is_error"); got != false {
		t.Errorf("tool_result is_error = %v, want false", got)
	}
	// timestamps normalized to wire millisecond RFC3339.
	if evs[0].TS != "2026-06-13T10:00:00.000Z" {
		t.Errorf("ts = %q, want normalized millis", evs[0].TS)
	}
}

// writeCCSession drops a session file under a fresh project dir and returns
// the root + the wire id.
func writeCCSession(t *testing.T, name, body string) (root, id string) {
	t.Helper()
	root = t.TempDir()
	proj := filepath.Join(root, "-Users-george-Code-anneal")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(proj, name+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, "cc:" + name
}

func TestCCBackend_ListAndRead(t *testing.T) {
	root, id := writeCCSession(t, "sess-uuid-1", ccFixture)
	b := newCCBackendAt(root)
	// Freeze the clock an hour ahead so the file reads as recent (within
	// the 14d window) but not "running" (older than the 30s live window).
	b.now = func() time.Time { return time.Now().Add(time.Hour) }
	ctx := context.Background()

	list, err := b.ListThreads(ctx)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("listed %d threads, want 1: %+v", len(list), list)
	}
	s := list[0]
	if s.ID != id || s.Backend != "cc" {
		t.Errorf("summary id/backend = %q/%q, want %q/cc", s.ID, s.Backend, id)
	}
	if s.Title != "hello claude" || s.UserMsgs != 1 {
		t.Errorf("summary title/msgs = %q/%d, want 'hello claude'/1", s.Title, s.UserMsgs)
	}
	if s.Frame != "anneal" {
		t.Errorf("summary frame = %q, want 'anneal' (project basename)", s.Frame)
	}
	if s.State != "done" {
		t.Errorf("summary state = %q, want 'done' (file older than live window)", s.State)
	}
	if s.Model != "claude-opus-4-8" {
		t.Errorf("summary model = %q, want claude-opus-4-8", s.Model)
	}
	if s.Capabilities["send"] {
		t.Error("observe backend must not advertise send")
	}

	evs, err := b.ReadEvents(ctx, id, 0, 0)
	if err != nil || len(evs) != 4 {
		t.Fatalf("ReadEvents = %d events, %v; want 4", len(evs), err)
	}
	// from cursor excludes seq<=from.
	tail, _ := b.ReadEvents(ctx, id, 2, 0)
	for _, we := range tail {
		if we.Seq <= 2 {
			t.Errorf("from=2 returned seq %d", we.Seq)
		}
	}

	// Interactive ops are unsupported in observe-only mode.
	if _, err := b.Send(ctx, id, "hi"); err != ErrUnsupported {
		t.Errorf("Send err = %v, want ErrUnsupported", err)
	}
}

func TestCCBackend_RecencyFilterButOpenableById(t *testing.T) {
	root, id := writeCCSession(t, "old-uuid", ccFixture)
	// Age the file well past the recency window.
	old := time.Now().Add(-30 * 24 * time.Hour)
	path := filepath.Join(root, "-Users-george-Code-anneal", "old-uuid.jsonl")
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	b := newCCBackendAt(root)
	ctx := context.Background()

	list, _ := b.ListThreads(ctx)
	if len(list) != 0 {
		t.Errorf("old session should be filtered from the roster, got %d", len(list))
	}
	// ...but still openable by id (direct URL): GetThread + ReadEvents fall
	// back to a uuid search across project dirs.
	if _, ok, _ := b.GetThread(ctx, id); !ok {
		t.Error("GetThread should resolve an out-of-window session by id")
	}
	if evs, _ := b.ReadEvents(ctx, id, 0, 0); len(evs) != 4 {
		t.Errorf("ReadEvents on out-of-window session = %d, want 4", len(evs))
	}
}

func TestCCBackend_SubscribeTailsAppends(t *testing.T) {
	root, id := writeCCSession(t, "live-uuid", ccFixture)
	b := newCCBackendAt(root)
	ch, unsub, err := b.Subscribe(id)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsub()

	// Append a new user turn; the tail should deliver it as a wire event
	// past the initial cursor (4 events already on disk).
	path := filepath.Join(root, "-Users-george-Code-anneal", "live-uuid.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"user","timestamp":"2026-06-13T10:01:00.000Z","message":{"role":"user","content":"another turn"}}` + "\n")
	_ = f.Close()

	select {
	case we, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before delivering the appended turn")
		}
		if we.Kind != "user_message" || we.Seq != 5 {
			t.Fatalf("tailed event = {kind:%q seq:%d}, want {user_message 5}", we.Kind, we.Seq)
		}
		if got := dataField(t, we, "text"); got != "another turn" {
			t.Errorf("tailed text = %v, want 'another turn'", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the tailed append (poll interval ~1s)")
	}
}
