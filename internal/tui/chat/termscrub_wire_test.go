package chat

import (
	"bytes"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestPostUpdateScrub_CleansLeakedMouseReportInComposer covers the
// defense-in-depth scrub that runs after m.ta.Update: a leaked SGR
// mouse-report sequence reaching the textarea value is stripped in
// place so the composer never shows the escape remnant as text the
// user has to backspace out. This replaces the chat-level coverage the
// deleted mouse_scrub_test.go used to provide; the patterns now live in
// internal/tui/termscrub.
func TestPostUpdateScrub_CleansLeakedMouseReportInComposer(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000001"
	seedAgent(t, log, agentID, "scrub", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 100, 30)

	// Feed the leak as a KeyRunes burst whose runes are the printable
	// tail of an SGR mouse report (ESC already stripped by the textarea
	// sanitizer). Routed through the chat Update so it lands in the
	// textarea value, where the post-update termscrub.Scrub catches it.
	leak := "[<64;96;7M"
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(leak), Paste: true})
	m = updated.(*Model)

	if got := m.ta.Value(); strings.Contains(got, "[<") || got != "" {
		t.Errorf("composer value should be scrubbed clean; got %q", got)
	}
}

// TestPostUpdateScrub_KeepsRealTextAroundLeak pins that the post-update
// scrub only removes the leak and leaves legitimately typed surrounding
// text intact.
func TestPostUpdateScrub_KeepsRealTextAroundLeak(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000002"
	seedAgent(t, log, agentID, "scrub2", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 100, 30)

	// Paste=true so the global filter passes the burst untouched and we
	// exercise the textarea-value scrub specifically.
	mixed := "hello [<0;1;2M world"
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(mixed), Paste: true})
	m = updated.(*Model)

	got := m.ta.Value()
	if strings.Contains(got, "[<") {
		t.Errorf("leak survived the scrub: %q", got)
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Errorf("scrub damaged surrounding text: %q", got)
	}
}

// TestWithStartupNotices_RendersInView confirms the notices passed via
// WithStartupNotices surface in the rendered View output (the footer
// banner), and that a nil slice renders nothing.
func TestWithStartupNotices_RendersInView(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000003"
	seedAgent(t, log, agentID, "notices", "claude-4.7-sonnet")

	notices := []string{
		"recovered 2 orphaned agent(s)",
		"mcp: registered 5 tool(s)",
	}
	m := New(log, agentID, NewMemTextSource(), WithStartupNotices(notices))
	m = drive(t, m, 100, 30)

	view := m.View()
	for _, n := range notices {
		if !strings.Contains(view, n) {
			t.Errorf("startup notice %q not found in View output", n)
		}
	}

	// renderStartupNotices is the unit the footer calls; a nil slice
	// must render the empty string so the footer falls through.
	bare := New(log, agentID, NewMemTextSource())
	if got := bare.renderStartupNotices(); got != "" {
		t.Errorf("empty notices should render nothing; got %q", got)
	}
}

// TestWithDiagWriter_RoutesResumePruneError exercises the /resume prune
// best-effort path: closing the event log forces DeleteEmptyOrphanedAgents
// to fail, and the error must land on the WithDiagWriter sink rather than
// stderr. Without WithDiagWriter the default io.Discard swallows it (no
// stderr write), which we assert by running the same path on a model that
// has no diag writer and confirming it does not panic / still degrades to
// a status echo.
func TestWithDiagWriter_RoutesResumePruneError(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000004"
	seedAgent(t, log, agentID, "diag", "claude-4.7-sonnet")

	var buf bytes.Buffer
	m := New(log, agentID, NewMemTextSource(), WithDiagWriter(&buf))

	// Force the prune query to fail by closing the underlying log.
	if err := log.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}

	// openResumePicker prunes (best-effort, error -> diag) then lists.
	// The list will also fail on the closed log and return a status
	// command; that is fine, we only assert the prune error was routed.
	_ = m.openResumePicker()

	if !strings.Contains(buf.String(), "prune empty orphans") {
		t.Errorf("prune error not routed to diag writer; got %q", buf.String())
	}
}

// TestDiagWriter_DefaultsToDiscard pins that a Model constructed without
// WithDiagWriter has a non-nil diag sink (io.Discard) so the /resume
// prune path never dereferences nil and never writes to stderr.
func TestDiagWriter_DefaultsToDiscard(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000005"
	seedAgent(t, log, agentID, "discard", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource())

	if m.diag == nil {
		t.Fatal("diag writer should default to io.Discard, not nil")
	}

	// Close the log to force the prune error and confirm the discard
	// path is exercised without panicking.
	if err := log.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}
	_ = m.openResumePicker()
}

// TestWithDiagWriter_NilWriterKeepsDiscard pins that passing a nil
// io.Writer to WithDiagWriter does not clobber the io.Discard default
// with a nil sink.
func TestWithDiagWriter_NilWriterKeepsDiscard(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000006"
	seedAgent(t, log, agentID, "nilwriter", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource(), WithDiagWriter(nil))
	if m.diag == nil {
		t.Fatal("nil writer must leave diag at the io.Discard default")
	}
}
