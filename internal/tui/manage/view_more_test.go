package manage

import (
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestModel_FindRow_HitAndMiss exercises the linear-scan lookup over
// the cached rawRows. Hit returns the row + true; miss returns the
// zero row + false.
func TestModel_FindRow_HitAndMiss(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	m.rawRows = []agent.AgentRow{
		{ID: "abc", Title: "first"},
		{ID: "def", Title: "second"},
	}

	got, ok := m.findRow("def")
	if !ok || got.Title != "second" {
		t.Errorf("findRow(def) = (%+v, %v), want second/true", got, ok)
	}
	zero, ok := m.findRow("ghi")
	if ok || zero.ID != "" {
		t.Errorf("findRow(ghi) = (%+v, %v), want zero/false", zero, ok)
	}
}

// TestPlural_BasicShape covers the 0/1/many branches of the count
// helper used in the header ("1 agent" vs "3 agents").
func TestPlural_BasicShape(t *testing.T) {
	if got := plural(0); got != "s" {
		t.Errorf("plural(0) = %q", got)
	}
	if got := plural(1); got != "" {
		t.Errorf("plural(1) = %q", got)
	}
	if got := plural(5); got != "s" {
		t.Errorf("plural(5) = %q", got)
	}
}

// TestSortDirGlyph_AscDesc covers both arrows.
func TestSortDirGlyph_AscDesc(t *testing.T) {
	if got := sortDirGlyph(true); !strings.Contains(got, "↑") {
		t.Errorf("sortDirGlyph(true) = %q, want '↑'", got)
	}
	if got := sortDirGlyph(false); !strings.Contains(got, "↓") {
		t.Errorf("sortDirGlyph(false) = %q, want '↓'", got)
	}
}

// TestRenderOverlay_AllKinds drives renderOverlay across each overlay
// shape so the confirm-vs-input branches both run. We set rawRows + a
// cursor so selectedID() resolves to a real intent for the confirm
// prompts.
func TestRenderOverlay_AllKinds(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	m.rawRows = []agent.AgentRow{{ID: "abc12345xyz", Title: "test intent"}}
	m.rosterRows = []rosterRow{{row: m.rawRows[0]}}

	cases := []struct {
		kind overlayKind
		want string
	}{
		{overlayNone, ""},
		{overlaySteer, "steer:"},
		{overlayInterruptConfirm, "interrupt"},
		{overlayStopConfirm, "stop"},
		{overlayFilter, "filter"},
		{overlayRejectReason, "reject reason"},
	}
	for _, c := range cases {
		m.overlay = c.kind
		got := m.renderOverlay(120)
		if c.want == "" {
			if got != "" {
				t.Errorf("renderOverlay(%v) = %q, want empty", c.kind, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("renderOverlay(%v) = %q, want substring %q", c.kind, got, c.want)
		}
	}
}

// TestRenderOverlay_NarrowWidthClamps narrows the terminal width so the
// width-clamp branch inside renderOverlay (input width floor 10) runs.
func TestRenderOverlay_NarrowWidthClamps(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	m.overlay = overlayFilter
	out := m.renderOverlay(8)
	if out == "" {
		t.Errorf("renderOverlay narrow returned empty")
	}
	if m.input.Width < 10 {
		t.Errorf("input width = %d, want clamp to 10", m.input.Width)
	}
}

// TestRenderFocusPane_UnboundAndBound covers the three branches of
// the rewritten focus pane: unbound (hint message), bound to an agent
// in the snapshot (rich detail card with title + stats), and bound to
// a stale id no longer in the projection (graceful fallback line).
func TestRenderFocusPane_UnboundAndBound(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)

	// Unbound focus pane shows the "pick an agent" hint.
	if got := m.renderFocusPane(120, 30); !strings.Contains(got, "select an agent") {
		t.Errorf("unbound pane = %q, want 'select an agent' hint", got)
	}

	// Bind to an agent that's in rawRows: the pane should surface the
	// title, the short id, and a stats label.
	m.rawRows = []agent.AgentRow{
		{ID: "01HVfound1234567", Title: "demo task", State: agent.StateRunning, Model: "gpt-4o-mini"},
	}
	m.focus.Bind("01HVfound1234567")
	got := m.renderFocusPane(120, 30)
	if !strings.Contains(got, "demo task") {
		t.Errorf("bound pane missing title: %q", got)
	}
	if !strings.Contains(got, "01HVfoun") {
		t.Errorf("bound pane missing short id: %q", got)
	}
	if !strings.Contains(got, "tokens") {
		t.Errorf("bound pane missing stats grid: %q", got)
	}
	if !strings.Contains(got, "model") {
		t.Errorf("bound pane missing model row: %q", got)
	}

	// Bind to a stale id: the fallback line surfaces the short id +
	// "no longer in projection" so the user knows the cursor moved.
	m.focus.Bind("01HVghost1234567")
	if got := m.renderFocusPane(120, 30); !strings.Contains(got, "01HVghos") {
		t.Errorf("stale-id pane = %q, want short id substring", got)
	}
}

// TestPopulateSparklines_OnlyFocusedRowGetsSpark confirms that
// populateSparklines decorates only the focused row's spark cell; the
// rest keep the empty placeholder.
func TestPopulateSparklines_OnlyFocusedRowGetsSpark(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	now := time.Now().UTC()
	rows := []rosterRow{
		{row: agent.AgentRow{ID: "aaa11111xyz", CreatedAt: now.Add(-time.Minute)}},
		{row: agent.AgentRow{ID: "bbb22222xyz", CreatedAt: now.Add(-time.Minute)}},
	}
	m.focus.Bind("bbb22222xyz")

	out := m.populateSparklines(rows)
	if out[0].spark != "" {
		t.Errorf("non-focused row got spark = %q", out[0].spark)
	}
	if out[1].spark == "" {
		t.Errorf("focused row spark is empty")
	}
}
