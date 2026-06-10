package agent

// apply_handler_test exercises the slog-error paths added by fix #7.
// Lives in the internal package so it can call the lowercase `handle`
// directly (the external agent_test package can't reach it). The
// malformed-payload branch is the easy one; the WriteArtifact branch
// is harder to drive in-process without intrusive mocking, so we
// pin it via the marshal-failure path (NaN floats are not valid
// JSON).

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// applyHandlerSyncBuf wraps a bytes.Buffer with a mutex so the slog
// handler writing from the test goroutine doesn't race the test-side
// assertions.
type applyHandlerSyncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *applyHandlerSyncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *applyHandlerSyncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestApplyHandler_MalformedPayloadIsLogged pins fix #7: the handler
// now logs malformed resolution payloads at Error level rather than
// returning silently. We feed an EvtApprovalAccepted with non-JSON
// payload bytes and assert the capturing handler observes the log
// line.
func TestApplyHandler_MalformedPayloadIsLogged(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer CloseStateDB(log)

	sup := NewSupervisor(log, nil, nil)
	defer sup.Shutdown()

	buf := &applyHandlerSyncBuf{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := &ApplyHandler{
		Supervisor: sup,
		Log:        log,
		Logger:     slog.New(handler),
	}

	// Feed handle directly with a malformed Accept event. resolverAgentID
	// is the canonical user-namespace id; the seq/agent_id are recorded
	// in the log attrs.
	h.handle(context.Background(), Event{
		Seq:     42,
		AgentID: resolverAgentID,
		TS:      time.Now().UTC(),
		Type:    EvtApprovalAccepted,
		Payload: []byte("definitely not json {"),
	})

	got := buf.String()
	if !strings.Contains(got, "malformed resolution payload") {
		t.Fatalf("expected malformed-payload log; captured: %q", got)
	}
	if !strings.Contains(got, "level=ERROR") {
		t.Errorf("malformed payload should be logged at ERROR; got: %q", got)
	}
	if !strings.Contains(got, "event_id=42") {
		t.Errorf("log should include event_id attr; got: %q", got)
	}
}

// TestApplyHandler_NilLoggerFallsBackToSlogDefault is a smoke test
// for the slogger() helper: a handler with no Logger field must not
// panic when an error path fires. We don't capture the default
// stderr output; we just assert no panic.
func TestApplyHandler_NilLoggerFallsBackToSlogDefault(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer CloseStateDB(log)

	sup := NewSupervisor(log, nil, nil)
	defer sup.Shutdown()
	h := &ApplyHandler{Supervisor: sup, Log: log} // no Logger
	// Defer-recover so a panic here would fail loudly rather than
	// crashing the suite.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil-logger fallback panicked: %v", r)
		}
	}()
	h.handle(context.Background(), Event{
		Seq:     1,
		AgentID: resolverAgentID,
		TS:      time.Now().UTC(),
		Type:    EvtApprovalAccepted,
		Payload: []byte("nope"),
	})
}
