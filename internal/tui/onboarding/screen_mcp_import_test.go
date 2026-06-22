package onboarding

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/mcp"
	"github.com/georgebuilds/carlos/internal/mcp/ccimport"
)

func sampleItems() []ccimport.Discovered {
	return []ccimport.Discovered{
		{Server: mcp.ServerConfig{Name: "github", Command: "npx", Args: []string{"-y", "pkg"}}, Source: "~/.claude.json"},
		{Server: mcp.ServerConfig{Name: "home-tools", Transport: "http", URL: "http://host/mcp"}, Source: "~/.claude.json [/Users/x/Code]"},
		{Server: mcp.ServerConfig{Name: "events", Transport: "sse", URL: "http://host/sse"}, Source: ".mcp.json"},
	}
}

// resultFromCmd executes a nextScreen cmd and extracts the mcpImportResult.
func resultFromCmd(t *testing.T, cmd tea.Cmd) mcpImportResult {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a nextScreen cmd, got nil")
	}
	msg := cmd()
	ns, ok := msg.(nextScreenMsg)
	if !ok {
		t.Fatalf("expected nextScreenMsg, got %T", msg)
	}
	res, ok := ns.payload.(mcpImportResult)
	if !ok {
		t.Fatalf("expected mcpImportResult payload, got %T", ns.payload)
	}
	return res
}

// TestMCPImport_EmptyAdvances: with nothing discovered the screen is a
// single skippable step - enter advances and imports nothing.
func TestMCPImport_EmptyAdvances(t *testing.T) {
	m := newMCPImportModelFromItems(nil)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	res := resultFromCmd(t, cmd)
	if len(res.servers) != 0 {
		t.Errorf("empty screen should import nothing, got %d", len(res.servers))
	}
	if !strings.Contains(next.(mcpImportModel).View(), "No Claude Code MCP servers") {
		t.Errorf("empty view should explain there's nothing to import:\n%s", next.(mcpImportModel).View())
	}
}

// TestMCPImport_SelectSubset: toggle two of three servers and confirm only
// those are returned, in list order.
func TestMCPImport_SelectSubset(t *testing.T) {
	m := newMCPImportModelFromItems(sampleItems())

	// Toggle item 0 (github), move to item 2 (events), toggle it. Leave 1.
	step := func(model tea.Model, key tea.KeyMsg) mcpImportModel {
		next, _ := model.Update(key)
		return next.(mcpImportModel)
	}
	m = step(m, tea.KeyMsg{Type: tea.KeySpace})                     // toggle github
	m = step(m, tea.KeyMsg{Type: tea.KeyDown})                      // -> home-tools
	m = step(m, tea.KeyMsg{Type: tea.KeyDown})                      // -> events
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}) // toggle events

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	res := resultFromCmd(t, cmd)
	if len(res.servers) != 2 {
		t.Fatalf("want 2 selected, got %d", len(res.servers))
	}
	if res.servers[0].Name != "github" || res.servers[1].Name != "events" {
		t.Errorf("wrong selection / order: %+v", res.servers)
	}
}

// TestMCPImport_ToggleAll: 'a' selects everything, and again clears it.
func TestMCPImport_ToggleAll(t *testing.T) {
	m := newMCPImportModelFromItems(sampleItems())
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = next.(mcpImportModel)
	if !m.allSelected() {
		t.Fatal("'a' should select all")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := len(resultFromCmd(t, cmd).servers); got != 3 {
		t.Errorf("want all 3, got %d", got)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = next.(mcpImportModel)
	if m.allSelected() {
		t.Error("second 'a' should clear the selection")
	}
}

// TestMCPImport_CursorBounds: up at the top and down at the bottom must not
// move the cursor out of range (a panic guard for the View loop).
func TestMCPImport_CursorBounds(t *testing.T) {
	m := newMCPImportModelFromItems(sampleItems())
	up, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if up.(mcpImportModel).cursor != 0 {
		t.Errorf("cursor went negative: %d", up.(mcpImportModel).cursor)
	}
	// Walk past the bottom.
	cur := m
	for i := 0; i < 10; i++ {
		next, _ := cur.Update(tea.KeyMsg{Type: tea.KeyDown})
		cur = next.(mcpImportModel)
	}
	if cur.cursor != len(cur.items)-1 {
		t.Errorf("cursor overran: %d (want %d)", cur.cursor, len(cur.items)-1)
	}
}

// TestMCPImport_ViewShowsTransport: the picker labels each server with its
// transport and target so two servers of the same name purpose are
// distinguishable.
func TestMCPImport_ViewShowsTransport(t *testing.T) {
	m := newMCPImportModelFromItems(sampleItems())
	v := m.View()
	for _, want := range []string{"github", "home-tools", "events", "(http)", "(sse)", "http://host/mcp"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}
