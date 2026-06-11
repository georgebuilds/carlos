package mcp

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestConnect_FailureSurfacesStderr spawns a real child that scribbles a
// marker to stderr and then exits non-zero. The MCP handshake can't possibly
// succeed (the child speaks no protocol), so Connect must fail - and because
// we now capture stderr instead of forwarding it, the failure error has to
// carry the marker so the operator can see why the server died.
func TestConnect_FailureSurfacesStderr(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available; skipping stderr-capture integration test")
	}
	cfg := ServerConfig{
		Name:    "x",
		Command: "sh",
		Args:    []string{"-c", "echo BOOM_MARKER >&2; exit 1"},
	}
	srv, err := Connect(context.Background(), cfg)
	if err == nil {
		if srv != nil {
			_ = srv.Close()
		}
		t.Fatal("Connect succeeded, want failure")
	}
	if !strings.Contains(err.Error(), "BOOM_MARKER") {
		t.Fatalf("Connect error %q does not contain captured stderr marker", err.Error())
	}
	if !strings.Contains(err.Error(), "stderr:") {
		t.Fatalf("Connect error %q missing stderr annotation", err.Error())
	}
}

// TestStderrTail_NilBufferReturnsEmpty covers the injected-Session path: a
// Server built without spawning a subprocess has no buffer, so StderrTail
// must return "" rather than panic.
func TestStderrTail_NilBufferReturnsEmpty(t *testing.T) {
	s := &Server{Name: "injected"}
	if got := s.StderrTail(); got != "" {
		t.Fatalf("StderrTail() with nil buffer = %q, want %q", got, "")
	}
}

// TestStderrTail_ReturnsCapturedTail confirms a populated buffer is read back
// through the exported accessor.
func TestStderrTail_ReturnsCapturedTail(t *testing.T) {
	buf := newBoundedBuffer(64)
	if _, err := buf.Write([]byte("diagnostic line")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	s := &Server{Name: "x", stderr: buf}
	if got := s.StderrTail(); got != "diagnostic line" {
		t.Fatalf("StderrTail() = %q, want %q", got, "diagnostic line")
	}
}
