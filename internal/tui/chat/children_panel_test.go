package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
)

// stubChildrenView is the test seam that lets a Model render the
// inline panel without standing up a real supervisor. Snapshot returns
// whatever the test pre-loaded.
type stubChildrenView struct {
	snaps []ChildSnapshot
}

func (s *stubChildrenView) Snapshot() []ChildSnapshot { return s.snaps }

func sampleSnapshot(id, lastEvent string, state agent.State, tokens int) ChildSnapshot {
	return ChildSnapshot{
		AgentID:   id,
		State:     state,
		LastEvent: lastEvent,
		Spend:     ChildSpend{Tokens: tokens, Cents: 1},
		StartedAt: time.Now().Add(-12 * time.Second),
	}
}

func TestRenderChildrenPanel_EmptyReturnsNothing(t *testing.T) {
	out := renderChildrenPanel(nil, 40, time.Now())
	if out != "" {
		t.Errorf("empty snapshot should render nothing; got %q", out)
	}
}

func TestRenderChildrenPanel_ListsEveryChildAt200Cols(t *testing.T) {
	now := time.Now()
	snaps := []ChildSnapshot{
		sampleSnapshot("01J0AAAAAAAAAAAAAAAA1234", "research phase: synthesize", agent.StateRunning, 4100),
		sampleSnapshot("01J0BBBBBBBBBBBBBBBB5678", "apply diff: 12 files", agent.StateRunning, 920),
		sampleSnapshot("01J0CCCCCCCCCCCCCCCCABCD", "verify: tests", agent.StateAwaitingInput, 250),
	}
	innerW := 200
	panelW := panelWidth(innerW)
	out := renderChildrenPanel(snaps, panelW, now)

	if !strings.Contains(out, "sub-agents (3)") {
		t.Errorf("header missing or wrong count; got:\n%s", out)
	}
	for _, want := range []string{"1234", "5678", "abcd"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel missing short id %q; got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "/agents for full view") {
		t.Errorf("hint missing; got:\n%s", out)
	}
}

func TestRenderChildrenPanel_TotalsSpend(t *testing.T) {
	now := time.Now()
	snaps := []ChildSnapshot{
		{AgentID: "01J0AAAAAAAAAAAAAAAA0001", State: agent.StateRunning, LastEvent: "search", Spend: ChildSpend{Tokens: 3500, Cents: 1}, StartedAt: now.Add(-5 * time.Second)},
		{AgentID: "01J0BBBBBBBBBBBBBBBB0002", State: agent.StateRunning, LastEvent: "fetch", Spend: ChildSpend{Tokens: 1500, Cents: 0}, StartedAt: now.Add(-3 * time.Second)},
	}
	out := renderChildrenPanel(snaps, 50, now)
	if !strings.Contains(out, "total: 5000 tok") {
		t.Errorf("total tokens missing or wrong; got:\n%s", out)
	}
	if !strings.Contains(out, "$0.010") {
		t.Errorf("total cents missing or wrong; got:\n%s", out)
	}
}

func TestRenderChildrenPanel_TruncatesLongLastEvent(t *testing.T) {
	now := time.Now()
	long := strings.Repeat("phase-synthesize-very-long-event-line ", 8)
	snaps := []ChildSnapshot{
		sampleSnapshot("01J0AAAAAAAAAAAAAAAA1234", long, agent.StateRunning, 1000),
	}
	out := renderChildrenPanel(snaps, panelMinWidth, now)
	if !strings.Contains(out, "…") {
		t.Errorf("expected ellipsis on truncation; got:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if widthOfLine(line) > panelMinWidth {
			t.Errorf("line wider than panel (%d > %d): %q", widthOfLine(line), panelMinWidth, line)
		}
	}
}

func TestRenderChildrenFallbackLine_PluralAndSingular(t *testing.T) {
	snaps1 := []ChildSnapshot{sampleSnapshot("01J0AAAAAAAAAAAAAAAA1234", "search", agent.StateRunning, 100)}
	out := renderChildrenFallbackLine(snaps1, 80)
	if !strings.Contains(out, "1 sub-agent is running") {
		t.Errorf("singular fallback wrong; got %q", out)
	}
	snaps3 := []ChildSnapshot{
		sampleSnapshot("01J0AAAAAAAAAAAAAAAA0001", "a", agent.StateRunning, 100),
		sampleSnapshot("01J0BBBBBBBBBBBBBBBB0002", "b", agent.StateRunning, 100),
		sampleSnapshot("01J0CCCCCCCCCCCCCCCC0003", "c", agent.StateRunning, 100),
	}
	out = renderChildrenFallbackLine(snaps3, 80)
	if !strings.Contains(out, "3 sub-agents are running") {
		t.Errorf("plural fallback wrong; got %q", out)
	}
	if !strings.Contains(out, "/agents to view") {
		t.Errorf("fallback missing /agents hint; got %q", out)
	}
}

func TestRenderChildrenFallbackLine_EmptyReturnsNothing(t *testing.T) {
	if got := renderChildrenFallbackLine(nil, 80); got != "" {
		t.Errorf("empty snapshot should produce no fallback; got %q", got)
	}
}

func TestPanelWidth_BoundsAndFraction(t *testing.T) {
	// 35% of 200 = 70, capped at 60.
	if w := panelWidth(200); w != panelMaxWidth {
		t.Errorf("panelWidth(200) = %d, want %d (cap)", w, panelMaxWidth)
	}
	// 35% of 120 = 42 (clears the 40 floor).
	if w := panelWidth(120); w != 42 {
		t.Errorf("panelWidth(120) = %d, want 42", w)
	}
	// Right at the floor: 35% of 100 = 35, clamped up to 40.
	if w := panelWidth(100); w != panelMinWidth {
		t.Errorf("panelWidth(100) = %d, want %d (floor)", w, panelMinWidth)
	}
}

func TestModelView_RendersSplitWhenChildrenAndWideEnough(t *testing.T) {
	cv := &stubChildrenView{snaps: []ChildSnapshot{
		sampleSnapshot("01J0AAAAAAAAAAAAAAAA1234", "research phase: search", agent.StateRunning, 1000),
		sampleSnapshot("01J0BBBBBBBBBBBBBBBB5678", "apply diff: 3 files", agent.StateRunning, 800),
	}}
	m := newChildrenModel(t, cv)
	m.width = 160
	m.height = 30
	m.childrenSnap = cv.Snapshot()

	out := m.View()
	if !strings.Contains(out, "sub-agents (2)") {
		t.Errorf("split layout should include the right panel header; got:\n%s", out)
	}
	if !strings.Contains(out, "1234") || !strings.Contains(out, "5678") {
		t.Errorf("panel rows missing; got:\n%s", out)
	}
}

func TestModelView_FallsBackToFooterLineUnderSplitMinWidth(t *testing.T) {
	cv := &stubChildrenView{snaps: []ChildSnapshot{
		sampleSnapshot("01J0AAAAAAAAAAAAAAAA1234", "search", agent.StateRunning, 1000),
		sampleSnapshot("01J0BBBBBBBBBBBBBBBB5678", "fetch", agent.StateRunning, 500),
	}}
	m := newChildrenModel(t, cv)
	m.width = 80
	m.height = 30
	m.childrenSnap = cv.Snapshot()

	out := m.View()
	if strings.Contains(out, "sub-agents (2)") {
		t.Errorf("narrow terminal should suppress the split panel; got:\n%s", out)
	}
	if !strings.Contains(out, "2 sub-agents are running") {
		t.Errorf("narrow terminal should show the fallback footer line; got:\n%s", out)
	}
}

func TestModelView_NoSnapshotKeepsLegacyLayout(t *testing.T) {
	cv := &stubChildrenView{snaps: nil}
	m := newChildrenModel(t, cv)
	m.width = 160
	m.height = 30
	m.childrenSnap = cv.Snapshot()
	out := m.View()
	if strings.Contains(out, "sub-agents") {
		t.Errorf("empty snapshot must not paint the panel; got:\n%s", out)
	}
	if strings.Contains(out, "/agents to view") {
		t.Errorf("empty snapshot must not paint the fallback line; got:\n%s", out)
	}
}

func TestChildrenTick_StopsOnEmptySnapshotAfterPriorChildren(t *testing.T) {
	cv := &stubChildrenView{snaps: []ChildSnapshot{
		sampleSnapshot("01J0AAAAAAAAAAAAAAAA1234", "search", agent.StateRunning, 100),
	}}
	m := newChildrenModel(t, cv)
	m.childrenSnap = cv.Snapshot()
	if len(m.childrenSnap) == 0 {
		t.Fatalf("seed snapshot should be non-empty")
	}
	cv.snaps = nil
	updated, cmd := m.Update(childrenTickMsg{})
	m2, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned wrong model type: %T", updated)
	}
	if len(m2.childrenSnap) != 0 {
		t.Errorf("snapshot should be empty after the tick; got %d", len(m2.childrenSnap))
	}
	if cmd != nil {
		t.Errorf("empty snapshot should stop the children tick (nil cmd); got %T", cmd)
	}
}

func TestFormatTokens_TableDriven(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0 tok"},
		{920, "920 tok"},
		{9999, "9999 tok"},
		{10000, "10k tok"},
		{12345, "12.3k tok"},
		{20000, "20k tok"},
	}
	for _, c := range cases {
		if got := formatTokens(c.in); got != c.want {
			t.Errorf("formatTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// newChildrenModel mirrors newFramedModel but plugs in a stub
// ChildrenView so the split layout renders without the full supervisor
// wiring. Keeps test files self-contained.
func newChildrenModel(t *testing.T, cv ChildrenView) *Model {
	t.Helper()
	return New(stubLog{}, "test-agent", NewMemTextSource(), WithChildrenView(cv))
}

// widthOfLine wraps lipgloss.Width without pulling the dependency
// into every assert site.
func widthOfLine(line string) int {
	return lipgloss.Width(line)
}
