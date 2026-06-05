package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
)

// TestNotesRecentDefault — empty input is allowed; returns the
// default-sized list.
func TestNotesRecentDefault(t *testing.T) {
	tool := NewNotesRecentTool(newTestEnv(t))
	out, err := tool.Execute(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["vault"] == nil || m["vault"] == "" {
		t.Error("vault missing")
	}
	notes, _ := m["notes"].([]any)
	if len(notes) == 0 {
		t.Error("expected ≥1 recent note")
	}
}

// TestNotesRecentSince — `since: 24h` honors the cutoff. We can't
// easily assert which notes are dropped without touching modtimes
// (the testdata files share the worktree's clone timestamp), so this
// test pins the parsing path: a valid duration is accepted, an
// invalid one is rejected.
func TestNotesRecentSinceValid(t *testing.T) {
	tool := NewNotesRecentTool(newTestEnv(t))
	out, err := tool.Execute(context.Background(), []byte(`{"since": "168h"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); errMsg != "" {
		t.Errorf("valid since should not error; got %q", errMsg)
	}
}

func TestNotesRecentSinceDays(t *testing.T) {
	// `7d` is the Obsidian-style shorthand we extend Go's
	// ParseDuration with.
	tool := NewNotesRecentTool(newTestEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"since": "7d"}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); errMsg != "" {
		t.Errorf("`7d` should parse; got envelope %q", errMsg)
	}
}

func TestNotesRecentSinceInvalid(t *testing.T) {
	tool := NewNotesRecentTool(newTestEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"since": "bogus"}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "since") {
		t.Errorf("expected since-parse envelope; got %+v", m)
	}
}

// TestNotesRecentNoVault — empty config + no override → envelope.
func TestNotesRecentNoVault(t *testing.T) {
	tool := NewNotesRecentTool(newNotesEnv(config.VaultConfig{}))
	out, _ := tool.Execute(context.Background(), []byte(`{}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "vault not configured") {
		t.Errorf("expected no-vault envelope; got %+v", m)
	}
}
