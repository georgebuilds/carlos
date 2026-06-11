package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
)

// emptyVaultEnv builds a notesEnv whose configured vault path is empty,
// so every notes_* tool takes its "vault not configured" envelope branch.
func emptyVaultEnv() *notesEnv {
	return newNotesEnv(config.VaultConfig{})
}

// notesToolExec is the common (tool, input) -> error-string driver for
// the uniform envelope assertions below.
type notesToolExec func(ctx context.Context, input []byte) ([]byte, error)

func runNotesErr(t *testing.T, exec notesToolExec, input string) string {
	t.Helper()
	out, err := exec(context.Background(), []byte(input))
	if err != nil {
		t.Fatalf("tool returned transport error (want envelope): %v", err)
	}
	return errMsg(t, out)
}

// TestNotesTools_BadJSON — every notes_* Execute surfaces a parse-error
// envelope (not a panic, not a transport error) for malformed input.
func TestNotesTools_BadJSON(t *testing.T) {
	env := newTestEnv(t)
	execs := map[string]notesToolExec{
		"notes_get":       NewNotesGetTool(env).Execute,
		"notes_search":    NewNotesSearchTool(env).Execute,
		"notes_backlinks": NewNotesBacklinksTool(env).Execute,
		"notes_tagged":    NewNotesTaggedTool(env).Execute,
		"notes_neighbors": NewNotesNeighborsTool(env).Execute,
		"notes_resolve":   NewNotesResolveTool(env).Execute,
	}
	for name, ex := range execs {
		if msg := runNotesErr(t, ex, `{bad json`); !strings.Contains(msg, "parse input") {
			t.Errorf("%s: want parse-input envelope, got %q", name, msg)
		}
	}
}

// TestNotesTools_MissingRequiredField — each tool reports its required
// field by name when omitted.
func TestNotesTools_MissingRequiredField(t *testing.T) {
	env := newTestEnv(t)
	cases := []struct {
		name  string
		exec  notesToolExec
		input string
		field string
	}{
		{"notes_get", NewNotesGetTool(env).Execute, `{}`, "note"},
		{"notes_search", NewNotesSearchTool(env).Execute, `{}`, "query"},
		{"notes_backlinks", NewNotesBacklinksTool(env).Execute, `{}`, "note"},
		{"notes_tagged", NewNotesTaggedTool(env).Execute, `{}`, "tag"},
		{"notes_neighbors", NewNotesNeighborsTool(env).Execute, `{}`, "note"},
		{"notes_resolve", NewNotesResolveTool(env).Execute, `{}`, "link"},
	}
	for _, c := range cases {
		msg := runNotesErr(t, c.exec, c.input)
		if !strings.Contains(msg, c.field) {
			t.Errorf("%s: want envelope naming %q, got %q", c.name, c.field, msg)
		}
	}
}

// TestNotesTools_NoVaultConfigured — with an empty vault config each tool
// returns the documented "vault not configured" envelope rather than
// crashing.
func TestNotesTools_NoVaultConfigured(t *testing.T) {
	env := emptyVaultEnv()
	cases := []struct {
		name  string
		exec  notesToolExec
		input string
	}{
		{"notes_get", NewNotesGetTool(env).Execute, `{"note":"x"}`},
		{"notes_search", NewNotesSearchTool(env).Execute, `{"query":"x"}`},
		{"notes_backlinks", NewNotesBacklinksTool(env).Execute, `{"note":"x"}`},
		{"notes_tagged", NewNotesTaggedTool(env).Execute, `{"tag":"x"}`},
		{"notes_neighbors", NewNotesNeighborsTool(env).Execute, `{"note":"x"}`},
		{"notes_resolve", NewNotesResolveTool(env).Execute, `{"link":"x"}`},
		{"notes_recent", NewNotesRecentTool(env).Execute, `{}`},
	}
	for _, c := range cases {
		msg := runNotesErr(t, c.exec, c.input)
		if !strings.Contains(msg, "vault not configured") {
			t.Errorf("%s: want vault-not-configured envelope, got %q", c.name, msg)
		}
	}
}
