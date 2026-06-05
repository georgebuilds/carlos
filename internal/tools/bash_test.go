package tools

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestBash_SimpleEcho(t *testing.T) {
	tool := NewBashTool()
	out, err := tool.Execute(context.Background(), []byte(`{"cmd":"echo hello world"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !bytes.Contains(out, []byte("hello world")) {
		t.Errorf("output missing payload: %q", out)
	}
	if !bytes.Contains(out, []byte("[exit 0]")) {
		t.Errorf("output missing [exit 0] tag: %q", out)
	}
}

func TestBash_ExitCodeNonZero(t *testing.T) {
	tool := NewBashTool()
	out, err := tool.Execute(context.Background(), []byte(`{"cmd":"exit 7"}`))
	if err != nil {
		t.Fatalf("Execute: %v (non-zero exit must NOT be an infra error)", err)
	}
	if !bytes.Contains(out, []byte("[exit 7]")) {
		t.Errorf("output missing [exit 7] tag: %q", out)
	}
}

func TestBash_StderrCaptured(t *testing.T) {
	tool := NewBashTool()
	out, _ := tool.Execute(context.Background(), []byte(`{"cmd":"echo oops 1>&2"}`))
	if !bytes.Contains(out, []byte("oops")) {
		t.Errorf("stderr not captured in combined output: %q", out)
	}
}

func TestBash_OutputTruncation(t *testing.T) {
	tool := &BashTool{MaxOutputLen: 256, Timeout: 5 * time.Second}
	// Produce more than 256 bytes.
	out, err := tool.Execute(context.Background(), []byte(`{"cmd":"yes x | head -c 4096"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !bytes.Contains(out, []byte("[truncated,")) {
		t.Errorf("expected truncation marker, got: %q", out)
	}
}

func TestBash_Timeout(t *testing.T) {
	tool := &BashTool{Timeout: 100 * time.Millisecond, MaxOutputLen: 1024}
	start := time.Now()
	out, err := tool.Execute(context.Background(), []byte(`{"cmd":"sleep 5"}`))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute should not surface timeout as infra error: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Execute did not honor timeout: took %s", elapsed)
	}
	if !bytes.Contains(out, []byte("[killed after")) {
		t.Errorf("output missing [killed after ...] tag: %q", out)
	}
}

func TestBash_BadInputReturnsError(t *testing.T) {
	tool := NewBashTool()
	if _, err := tool.Execute(context.Background(), []byte(`not json`)); err == nil {
		t.Error("expected parse error on bad JSON")
	}
	if _, err := tool.Execute(context.Background(), []byte(`{}`)); err == nil {
		t.Error("expected empty-cmd error")
	}
}

func TestBash_ContextCancel(t *testing.T) {
	tool := &BashTool{Timeout: 5 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel quickly while a long sleep is in flight.
	done := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
		close(done)
	}()
	start := time.Now()
	_, _ = tool.Execute(ctx, []byte(`{"cmd":"sleep 5"}`))
	elapsed := time.Since(start)
	<-done
	if elapsed > 2*time.Second {
		t.Errorf("ctx cancellation didn't kill the process: took %s", elapsed)
	}
}

func TestBash_SchemaIsValidJSON(t *testing.T) {
	tool := NewBashTool()
	s := string(tool.Schema())
	if !strings.Contains(s, `"cmd"`) {
		t.Error("schema missing cmd field")
	}
	if !strings.Contains(s, `"required"`) {
		t.Error("schema missing required array")
	}
}

func TestBash_Description(t *testing.T) {
	tool := NewBashTool()
	if tool.Description() == "" {
		t.Error("Description must not be empty — Anthropic surfaces this to the model")
	}
}

// TestBash_PTYEcho verifies the PTY codepath produces real output. We
// skip if PTY isn't available on the test machine (rare on Darwin/Linux
// but possible in heavily-restricted CI containers).
func TestBash_PTYEcho(t *testing.T) {
	tool := &BashTool{PTY: true, Timeout: 5 * time.Second}
	out, err := tool.Execute(context.Background(), []byte(`{"cmd":"echo hello"}`))
	if err != nil {
		t.Skipf("PTY not available in this environment: %v", err)
	}
	if !bytes.Contains(out, []byte("hello")) {
		t.Errorf("PTY output missing payload: %q", out)
	}
	if !bytes.Contains(out, []byte("[exit 0]")) {
		t.Errorf("PTY output missing [exit 0] tag: %q", out)
	}
}

// TestBash_PTYTimeout confirms the PTY path also honours the timeout
// and kills the child process tree.
func TestBash_PTYTimeout(t *testing.T) {
	tool := &BashTool{PTY: true, Timeout: 200 * time.Millisecond}
	start := time.Now()
	out, err := tool.Execute(context.Background(), []byte(`{"cmd":"sleep 5"}`))
	elapsed := time.Since(start)
	if err != nil {
		t.Skipf("PTY not available in this environment: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("PTY timeout not honored: took %s", elapsed)
	}
	if !bytes.Contains(out, []byte("[killed after")) {
		t.Errorf("PTY output missing kill marker: %q", out)
	}
}
