package chatglue

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/usershell"
)

// appendAssistantMessage mirrors appendUserMessage for the assistant role
// so the ordering tests can interleave both roles with shell jobs.
func appendAssistantMessage(t *testing.T, log *agent.SQLiteEventLog, id, text string) {
	t.Helper()
	payload, _ := json.Marshal(agent.MessagePayload{Text: text})
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtAssistantMessage, Payload: payload,
	}); err != nil {
		t.Fatalf("append assistant_message: %v", err)
	}
}

// appendShellJob writes a start+end pair under the synthetic user-shell
// agent_id, the way the Manager does at runtime.
func appendShellJob(t *testing.T, log *agent.SQLiteEventLog, start usershell.StartPayload, end usershell.EndPayload) {
	t.Helper()
	ctx := context.Background()
	if start.JobID != "" {
		if _, err := usershell.AppendStart(ctx, log, start); err != nil {
			t.Fatalf("append shell start: %v", err)
		}
	}
	if end.JobID != "" {
		if _, err := usershell.AppendEnd(ctx, log, end); err != nil {
			t.Fatalf("append shell end: %v", err)
		}
	}
}

func TestBuildHistory_ShellJobBecomesUserShellBlock(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-shell-proj"
	seedAgent(t, log, id)

	appendUserMessage(t, log, id, "run the tests")
	appendShellJob(t, log,
		usershell.StartPayload{JobID: "job1", Command: "go test ./...", Cwd: "/repo", FrameName: "work", StartedAt: time.Now()},
		usershell.EndPayload{JobID: "job1", ExitCode: 0, Duration: 1200 * time.Millisecond, OutputInline: "ok  ./...  1.2s\n"},
	)
	appendAssistantMessage(t, log, id, "all green")

	l := NewLoop(Config{}, log, newMemSource(), id)
	hist, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("history len = %d, want 3 (user, user-shell, assistant); got %+v", len(hist), hist)
	}
	if hist[0].Role != "user" || hist[0].Content[0].Text != "run the tests" {
		t.Errorf("entry 0 = %+v, want the user message", hist[0])
	}
	if hist[2].Role != "assistant" || hist[2].Content[0].Text != "all green" {
		t.Errorf("entry 2 = %+v, want the assistant message", hist[2])
	}
	block := hist[1]
	if block.Role != "user" {
		t.Errorf("shell block role = %q, want user", block.Role)
	}
	got := block.Content[0].Text
	for _, want := range []string{
		`<user-shell`, `frame="work"`, `cwd="/repo"`, `exit=0`, `duration="1.2s"`,
		"$ go test ./...", "ok  ./...  1.2s", "</user-shell>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("shell block missing %q\nfull block:\n%s", want, got)
		}
	}
}

func TestBuildHistory_ShellInterleavesBySeq(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-shell-order"
	seedAgent(t, log, id)

	// chat turn, then shell job, then another chat turn. Seq is global
	// + monotonic, so the merge must place the shell block between them.
	appendUserMessage(t, log, id, "first")
	appendShellJob(t, log,
		usershell.StartPayload{JobID: "j", Command: "echo hi", StartedAt: time.Now()},
		usershell.EndPayload{JobID: "j", OutputInline: "hi\n"},
	)
	appendUserMessage(t, log, id, "second")

	l := NewLoop(Config{}, log, newMemSource(), id)
	hist, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("want 3 entries, got %d", len(hist))
	}
	if hist[0].Content[0].Text != "first" {
		t.Errorf("entry 0 = %q", hist[0].Content[0].Text)
	}
	if !strings.Contains(hist[1].Content[0].Text, "<user-shell") {
		t.Errorf("entry 1 should be the shell block, got %q", hist[1].Content[0].Text)
	}
	if hist[2].Content[0].Text != "second" {
		t.Errorf("entry 2 = %q", hist[2].Content[0].Text)
	}
}

func TestBuildHistory_CancelledAndTruncatedAndError(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-shell-states"
	seedAgent(t, log, id)

	appendShellJob(t, log,
		usershell.StartPayload{JobID: "c", Command: "sleep 99", StartedAt: time.Now()},
		usershell.EndPayload{JobID: "c", Cancelled: true, TruncatedBytes: 4096, OutputInline: "tail\n", FailErrMsg: "killed"},
	)

	l := NewLoop(Config{}, log, newMemSource(), id)
	hist, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("want 1 entry, got %d", len(hist))
	}
	got := hist[0].Content[0].Text
	for _, want := range []string{`status="cancelled"`, "[earlier output truncated: 4096 bytes]", "[error: killed]"} {
		if !strings.Contains(got, want) {
			t.Errorf("block missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "exit=") {
		t.Errorf("a cancelled job should not render an exit code:\n%s", got)
	}
}

func TestBuildHistory_ShellNoFrameOmitsAttr(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-shell-noframe"
	seedAgent(t, log, id)

	// Legacy / test job: no FrameName. Block must still render, just
	// without a frame= attribute.
	appendShellJob(t, log,
		usershell.StartPayload{JobID: "nf", Command: "ls", StartedAt: time.Now()},
		usershell.EndPayload{JobID: "nf", OutputInline: "file\n"},
	)
	l := NewLoop(Config{}, log, newMemSource(), id)
	hist, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("want 1 entry, got %d", len(hist))
	}
	if strings.Contains(hist[0].Content[0].Text, "frame=") {
		t.Errorf("block should omit frame= when FrameName is empty:\n%s", hist[0].Content[0].Text)
	}
}

func TestBuildHistory_ShellEndWithoutStartIsDropped(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-shell-orphan"
	seedAgent(t, log, id)

	// An end with no recorded start can't be attributed → no block.
	appendShellJob(t, log,
		usershell.StartPayload{}, // no start
		usershell.EndPayload{JobID: "orphan", ExitCode: 0, OutputInline: "x\n"},
	)
	appendUserMessage(t, log, id, "still here")

	l := NewLoop(Config{}, log, newMemSource(), id)
	hist, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 || hist[0].Content[0].Text != "still here" {
		t.Fatalf("orphan end should be dropped, leaving only the user message; got %+v", hist)
	}
}

func TestBuildHistory_SessionResetClearsPendingShellStart(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-shell-reset"
	seedAgent(t, log, id)
	ctx := context.Background()

	// Start a shell job, then reset, then the job's end arrives. After a
	// reset the recorded start is cleared, so the post-reset end can't
	// correlate and emits nothing.
	if _, err := usershell.AppendStart(ctx, log, usershell.StartPayload{JobID: "r", Command: "make", StartedAt: time.Now()}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := log.Append(ctx, agent.Event{AgentID: id, TS: time.Now().UTC(), Type: agent.EvtSessionReset, Payload: []byte("{}")}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := usershell.AppendEnd(ctx, log, usershell.EndPayload{JobID: "r", OutputInline: "done\n"}); err != nil {
		t.Fatalf("end: %v", err)
	}
	appendUserMessage(t, log, id, "after reset")

	l := NewLoop(Config{}, log, newMemSource(), id)
	hist, err := l.buildHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 || hist[0].Content[0].Text != "after reset" {
		t.Fatalf("post-reset history should hold only the new user message; got %+v", hist)
	}
}

func TestMergeBySeq(t *testing.T) {
	mk := func(seqs ...int64) []agent.Event {
		out := make([]agent.Event, len(seqs))
		for i, s := range seqs {
			out[i] = agent.Event{Seq: s}
		}
		return out
	}
	seqsOf := func(evs []agent.Event) []int64 {
		out := make([]int64, len(evs))
		for i, e := range evs {
			out[i] = e.Seq
		}
		return out
	}
	eq := func(a, b []int64) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	if got := mergeBySeq(mk(1, 3, 5), nil); !eq(seqsOf(got), []int64{1, 3, 5}) {
		t.Errorf("empty b: %v", seqsOf(got))
	}
	if got := mergeBySeq(nil, mk(2, 4)); !eq(seqsOf(got), []int64{2, 4}) {
		t.Errorf("empty a: %v", seqsOf(got))
	}
	if got := mergeBySeq(mk(1, 4, 5), mk(2, 3, 6)); !eq(seqsOf(got), []int64{1, 2, 3, 4, 5, 6}) {
		t.Errorf("interleave: %v", seqsOf(got))
	}
	// Equal seqs: a wins the tie (<=), preserving chat-before-shell when
	// timestamps collide.
	if got := mergeBySeq(mk(2), mk(2)); !eq(seqsOf(got), []int64{2, 2}) {
		t.Errorf("equal seqs: %v", seqsOf(got))
	}
}
