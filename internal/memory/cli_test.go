package memory_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/memory"
)

// TestRunSearchTo_HappyPath proves the CLI surface end-to-end: seed
// one summary, search for a token in it, see the formatted line on
// the writer.
func TestRunSearchTo_HappyPath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	store, err := memory.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if _, err := store.AppendSummary(context.Background(), memory.Summary{
		AgentID: "agent-abc12345",
		Text:    "We discussed implementing the carlos memory subsystem.",
	}); err != nil {
		t.Fatalf("AppendSummary: %v", err)
	}
	_ = store.Close()

	var buf bytes.Buffer
	if err := memory.RunSearchTo(&buf, "memory", 10, dbPath); err != nil {
		t.Fatalf("RunSearchTo: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "[agent-ab]") {
		t.Errorf("expected agent id prefix in output, got %q", out)
	}
	if !strings.Contains(out, "carlos memory subsystem") {
		t.Errorf("expected summary text in output, got %q", out)
	}
}

// TestRunSearchTo_NoMatches verifies the "no matches." friendly
// output (so the CLI doesn't render an empty buffer).
func TestRunSearchTo_NoMatches(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	store, _ := memory.OpenStore(dbPath)
	_ = store.Close()

	var buf bytes.Buffer
	if err := memory.RunSearchTo(&buf, "nothingmatchesthis", 10, dbPath); err != nil {
		t.Fatalf("RunSearchTo: %v", err)
	}
	if !strings.Contains(buf.String(), "no matches.") {
		t.Errorf("expected friendly empty output, got %q", buf.String())
	}
}

// TestRunSearchTo_EmptyQueryRejected guards against an FTS5 crash on
// blank input.
func TestRunSearchTo_EmptyQueryRejected(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	store, _ := memory.OpenStore(dbPath)
	_ = store.Close()

	var buf bytes.Buffer
	if err := memory.RunSearchTo(&buf, "   ", 10, dbPath); err == nil {
		t.Error("expected error on empty query")
	}
}
