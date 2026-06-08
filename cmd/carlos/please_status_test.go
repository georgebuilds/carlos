package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/theme"
)

func testPleasePalette() theme.Palette {
	return theme.Palette{
		Accent: lipgloss.Color("39"),
		Muted:  lipgloss.Color("245"),
		Subtle: lipgloss.Color("248"),
		Warn:   lipgloss.Color("203"),
	}
}

// TestPleaseStatus_ToolStartSwitchesActivityLine pins the headline
// state machine: a fresh model is "thinking", a pleaseToolStartMsg
// flips it to the focused tool, and pleaseToolDoneMsg clears the
// focus + bumps the counter.
func TestPleaseStatus_ToolStartSwitchesActivityLine(t *testing.T) {
	m := newPleaseStatusModel("add a file", "openrouter", "gemini-3.5-flash", testPleasePalette())
	m.width = 100

	if !strings.Contains(m.View(), "thinking") {
		t.Errorf("fresh model should render 'thinking':\n%s", m.View())
	}

	next, _ := m.Update(pleaseToolStartMsg{name: "bash", inputJSON: `{"cmd":"touch x"}`, t: time.Now()})
	m = next.(pleaseStatusModel)
	v := m.View()
	if !strings.Contains(v, "bash") {
		t.Errorf("View should show 'bash' after tool start:\n%s", v)
	}
	if !strings.Contains(v, "touch x") {
		t.Errorf("View should show the tool input preview:\n%s", v)
	}

	next, _ = m.Update(pleaseToolDoneMsg{name: "bash"})
	m = next.(pleaseStatusModel)
	if m.toolsDone != 1 {
		t.Errorf("toolsDone = %d, want 1", m.toolsDone)
	}
	if strings.Contains(m.View(), "bash") {
		t.Errorf("View should not still show 'bash' after done:\n%s", m.View())
	}
}

// TestPleaseStatus_TextDeltaSetsWritingPreview confirms the streaming
// branch: a text delta with newlines surfaces only the latest non-
// empty line for the activity row.
func TestPleaseStatus_TextDeltaSetsWritingPreview(t *testing.T) {
	m := newPleaseStatusModel("draft", "openai", "gpt-4o", testPleasePalette())
	m.width = 100

	next, _ := m.Update(pleaseTextDeltaMsg{text: "first line\n\nlast meaningful line\n"})
	m = next.(pleaseStatusModel)
	if m.lastTextLine != "last meaningful line" {
		t.Errorf("lastTextLine = %q, want 'last meaningful line'", m.lastTextLine)
	}
	if !strings.Contains(m.View(), "writing") {
		t.Errorf("View should show 'writing' marker:\n%s", m.View())
	}
}

// TestPleaseStatus_DoneSwitchesToCheck verifies the terminal frame
// (✓ on success) so the user sees a clear "complete" beat before
// the panel quits.
func TestPleaseStatus_DoneSwitchesToCheck(t *testing.T) {
	m := newPleaseStatusModel("ok", "anthropic", "sonnet", testPleasePalette())
	m.width = 100

	next, _ := m.Update(pleaseDoneMsg{})
	m = next.(pleaseStatusModel)
	if !m.done {
		t.Error("done flag not set on pleaseDoneMsg")
	}
	if !strings.Contains(m.View(), "✓") {
		t.Errorf("done view should include check glyph:\n%s", m.View())
	}
}

// TestPleaseStatus_DoneWithErrorSwitchesToCross verifies the
// failure branch + paints the error message on line 2.
func TestPleaseStatus_DoneWithErrorSwitchesToCross(t *testing.T) {
	m := newPleaseStatusModel("ok", "anthropic", "sonnet", testPleasePalette())
	m.width = 100

	next, _ := m.Update(pleaseDoneMsg{errMsg: "provider blew up"})
	m = next.(pleaseStatusModel)
	v := m.View()
	if !strings.Contains(v, "✗") {
		t.Errorf("error done view should include cross glyph:\n%s", v)
	}
	if !strings.Contains(v, "provider blew up") {
		t.Errorf("error done view should include err msg:\n%s", v)
	}
}

// TestPleaseTextSink_BuffersAndForwards covers the io.Writer wrapper:
// every Write lands in the internal buffer, and (when a *tea.Program
// is wired) every Write also forwards a delta msg. Test uses a nil
// program to skip the forward; we just verify the buffer captures.
func TestPleaseTextSink_BuffersAndForwards(t *testing.T) {
	sink := &pleaseTextSink{}
	if _, err := sink.Write([]byte("hello ")); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write([]byte("world\n")); err != nil {
		t.Fatal(err)
	}
	if got := sink.String(); got != "hello world\n" {
		t.Errorf("buffered text = %q, want %q", got, "hello world\n")
	}
}

// TestPleaseTextSink_RaceFree exercises concurrent Write calls. The
// mutex inside pleaseTextSink keeps the buffer consistent under
// stress. Run with `go test -race` for the actual race detection;
// this test exists to give the race detector something to chew on.
func TestPleaseTextSink_RaceFree(t *testing.T) {
	sink := &pleaseTextSink{}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = sink.Write([]byte("x"))
		}()
	}
	wg.Wait()
	if len(sink.String()) != 32 {
		t.Errorf("expected 32 bytes, got %d", len(sink.String()))
	}
}

// TestFirstLine_HandlesNewlineAndNone covers both branches of the
// tool-error clipping helper.
func TestFirstLine_HandlesNewlineAndNone(t *testing.T) {
	if got := firstLine("one line"); got != "one line" {
		t.Errorf("no-newline: got %q", got)
	}
	if got := firstLine("first\nsecond"); got != "first" {
		t.Errorf("with-newline: got %q", got)
	}
}

// TestIsContextCanceled covers nil + canceled + unrelated err.
func TestIsContextCanceled(t *testing.T) {
	if isContextCanceled(nil) {
		t.Error("nil should not be reported as canceled")
	}
	if !isContextCanceled(context.Canceled) {
		t.Error("context.Canceled should be reported")
	}
	if isContextCanceled(errors.New("network reset")) {
		t.Error("unrelated err should not be reported as canceled")
	}
}

// avoid unused import for tea in test build (Update returns tea.Cmd
// but we ignore it everywhere above).
var _ tea.Cmd = nil
