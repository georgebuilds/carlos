package farewell

import (
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/theme"
)

func testPalette() theme.Palette {
	return theme.Palette{
		Accent: lipgloss.Color("39"),
		Muted:  lipgloss.Color("245"),
		Agent:  lipgloss.Color("252"),
		Subtle: lipgloss.Color("248"),
	}
}

// TestRender_EmptyPanelProducesNothing is the no-op contract: a
// session with no warnings or notes returns "" so the caller's
// unconditional WriteString to stderr emits zero bytes.
func TestRender_EmptyPanelProducesNothing(t *testing.T) {
	p := New()
	if got := p.Render(80, testPalette()); got != "" {
		t.Errorf("empty panel should render to empty string; got %q", got)
	}
}

// TestRender_SingleMessageHasEmojiAndText pins the headline format:
// the emoji column is visible AND the message text survives.
func TestRender_SingleMessageHasEmojiAndText(t *testing.T) {
	p := New()
	p.Add("👋", "later, George")
	out := p.Render(80, testPalette())
	if !strings.Contains(out, "👋") {
		t.Errorf("rendered panel missing emoji:\n%s", out)
	}
	if !strings.Contains(out, "later, George") {
		t.Errorf("rendered panel missing text:\n%s", out)
	}
}

// TestRender_DetailLineRendersBelow proves the optional second-line
// detail format works — used by the daemon hint and the update note.
func TestRender_DetailLineRendersBelow(t *testing.T) {
	p := New()
	p.AddWithDetail("🛰️", "daemon offline", "run `carlos daemon enable` to start it")
	out := p.Render(80, testPalette())
	if !strings.Contains(out, "daemon offline") {
		t.Errorf("missing headline:\n%s", out)
	}
	if !strings.Contains(out, "carlos daemon enable") {
		t.Errorf("missing detail line:\n%s", out)
	}
}

// TestAdd_EmptyTextIsNoOp guards a defensive caller that conditionally
// builds an empty message — we don't want a blank row in the box.
func TestAdd_EmptyTextIsNoOp(t *testing.T) {
	p := New()
	p.Add("👋", "")
	if got := p.Len(); got != 0 {
		t.Errorf("empty text should not queue a message; got %d", got)
	}
}

// TestConcurrentAdd_NoRace verifies the Panel is safe to share between
// the main shutdown path and the background brew-update probe.
func TestConcurrentAdd_NoRace(t *testing.T) {
	p := New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p.Add("•", "msg")
		}(i)
	}
	wg.Wait()
	if got := p.Len(); got != 8 {
		t.Errorf("expected 8 messages, got %d", got)
	}
}

// TestRender_MessageOrderMatchesAddOrder makes the rendering order
// load-bearing: the goodbye line should land last because that's the
// order the cmd/carlos wire-up queues messages in.
func TestRender_MessageOrderMatchesAddOrder(t *testing.T) {
	p := New()
	p.Add("⬆️", "update available")
	p.Add("🛰️", "daemon offline")
	p.Add("👋", "later, friend")
	out := p.Render(80, testPalette())
	upd := strings.Index(out, "update available")
	dae := strings.Index(out, "daemon offline")
	bye := strings.Index(out, "later, friend")
	if !(upd < dae && dae < bye) {
		t.Errorf("expected update < daemon < bye order; got upd=%d dae=%d bye=%d", upd, dae, bye)
	}
}
