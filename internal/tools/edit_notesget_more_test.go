package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestEdit_NegativeExpectCount — a negative expect_match_count is rejected.
func TestEdit_NegativeExpectCount(t *testing.T) {
	tool := NewEditTool()
	in := `{"path":"x","search":"a","replace":"b","expect_match_count":-1}`
	if _, err := tool.Execute(context.Background(), []byte(in)); err == nil ||
		!strings.Contains(err.Error(), "non-negative") {
		t.Errorf("want non-negative error, got %v", err)
	}
}

// TestEdit_FileNotFound — editing a missing file surfaces an open error.
func TestEdit_FileNotFound(t *testing.T) {
	tool := NewEditTool()
	missing := filepath.Join(t.TempDir(), "nope.txt")
	in := `{"path":"` + missing + `","search":"a","replace":"b"}`
	if _, err := tool.Execute(context.Background(), []byte(in)); err == nil ||
		!strings.Contains(err.Error(), "open") {
		t.Errorf("want open error, got %v", err)
	}
}

// TestNotesGet_SectionNotFound — requesting a non-existent section yields a
// "section not found" envelope.
func TestNotesGet_SectionNotFound(t *testing.T) {
	tool := NewNotesGetTool(newTestEnv(t))
	out, err := tool.Execute(context.Background(),
		[]byte(`{"note":"carlos","section":"No Such Heading"}`))
	if err != nil {
		t.Fatal(err)
	}
	if msg := errMsg(t, out); !strings.Contains(msg, "section not found") {
		t.Errorf("want section-not-found envelope, got %q", msg)
	}
}
