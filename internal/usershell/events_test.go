package usershell

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

func newTestLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func TestAppendStart_Persists(t *testing.T) {
	log := newTestLog(t)
	ctx := context.Background()
	p := StartPayload{
		JobID:     "j-1",
		Command:   "ls -la",
		Cwd:       "/tmp",
		Mode:      "foreground",
		StartedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	seq, err := AppendStart(ctx, log, p)
	if err != nil {
		t.Fatal(err)
	}
	if seq <= 0 {
		t.Errorf("seq: want >0 got %d", seq)
	}
	events, _ := log.Read(ctx, EventAgentID, 0)
	if len(events) != 1 || events[0].Type != agent.EvtUserShellStart {
		t.Fatalf("expected one start event; got %+v", events)
	}
	decoded, err := DecodeStartPayload(events[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Command != "ls -la" || decoded.JobID != "j-1" {
		t.Errorf("decoded mismatch: %+v", decoded)
	}
}

func TestAppendEnd_Persists(t *testing.T) {
	log := newTestLog(t)
	ctx := context.Background()
	p := EndPayload{
		JobID:        "j-1",
		ExitCode:     0,
		Duration:     2500 * time.Millisecond,
		Cancelled:    false,
		Backgrounded: false,
		OutputInline: "hello\n",
		OutputPath:   "/tmp/usershell/j-1.log",
	}
	if _, err := AppendEnd(ctx, log, p); err != nil {
		t.Fatal(err)
	}
	events, _ := log.Read(ctx, EventAgentID, 0)
	if len(events) != 1 || events[0].Type != agent.EvtUserShellEnd {
		t.Fatalf("expected one end event; got %+v", events)
	}
	got, err := DecodeEndPayload(events[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExitCode != 0 || got.OutputInline != "hello\n" {
		t.Errorf("decoded: %+v", got)
	}
}

func TestAppendStart_NilLog(t *testing.T) {
	if _, err := AppendStart(context.Background(), nil, StartPayload{JobID: "x", Command: "x"}); err == nil {
		t.Error("expected error on nil log")
	}
}

func TestAppendStart_MissingJobID(t *testing.T) {
	log := newTestLog(t)
	if _, err := AppendStart(context.Background(), log, StartPayload{Command: "x"}); err == nil {
		t.Error("expected error on missing job id")
	}
}

func TestAppendStart_MissingCommand(t *testing.T) {
	log := newTestLog(t)
	if _, err := AppendStart(context.Background(), log, StartPayload{JobID: "x"}); err == nil {
		t.Error("expected error on missing command")
	}
}

func TestAppendEnd_NilLog(t *testing.T) {
	if _, err := AppendEnd(context.Background(), nil, EndPayload{JobID: "x"}); err == nil {
		t.Error("expected error on nil log")
	}
}

func TestAppendEnd_MissingJobID(t *testing.T) {
	log := newTestLog(t)
	if _, err := AppendEnd(context.Background(), log, EndPayload{}); err == nil {
		t.Error("expected error on missing job id")
	}
}

func TestDecodeStartPayload_Malformed(t *testing.T) {
	if _, err := DecodeStartPayload([]byte("not json")); err == nil {
		t.Error("expected decode error")
	}
}

func TestDecodeEndPayload_Malformed(t *testing.T) {
	if _, err := DecodeEndPayload([]byte("not json")); err == nil {
		t.Error("expected decode error")
	}
}

func TestTruncateForInline_NoTruncation(t *testing.T) {
	output := "short output"
	got, dropped := TruncateForInline(output)
	if got != output {
		t.Errorf("short output should pass through unchanged; got %q", got)
	}
	if dropped != 0 {
		t.Errorf("dropped: want 0 got %d", dropped)
	}
}

func TestTruncateForInline_TakesTail(t *testing.T) {
	long := strings.Repeat("a", MaxInlineOutput) + "TAIL"
	got, dropped := TruncateForInline(long)
	if dropped != 4 {
		t.Errorf("dropped: want 4 got %d", dropped)
	}
	if len(got) != MaxInlineOutput {
		t.Errorf("kept length: want %d got %d", MaxInlineOutput, len(got))
	}
	if !strings.HasSuffix(got, "TAIL") {
		t.Errorf("truncate should keep the tail; got tail-suffix %q", got[len(got)-4:])
	}
}

func TestTruncateForInline_ExactBoundary(t *testing.T) {
	exact := strings.Repeat("x", MaxInlineOutput)
	got, dropped := TruncateForInline(exact)
	if dropped != 0 || got != exact {
		t.Errorf("exact-fit should not truncate; dropped=%d, len=%d", dropped, len(got))
	}
}

func TestPayloadJSONRoundTrip(t *testing.T) {
	in := EndPayload{
		JobID:          "j-rt",
		ExitCode:       137,
		Duration:       3 * time.Second,
		Cancelled:      true,
		OutputInline:   "boom",
		TruncatedBytes: 9000,
		OutputPath:     "/tmp/usershell/j-rt.log",
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeEndPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Errorf("roundtrip mismatch: got %+v want %+v", got, in)
	}
}
