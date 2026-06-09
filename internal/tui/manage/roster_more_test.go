package manage

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestCountDescendants_TreeWalk builds a small adjacency map and
// asserts the recursive descendant count handles trees, leaves, and
// missing entries.
func TestCountDescendants_TreeWalk(t *testing.T) {
	childrenOf := map[string][]agent.AgentRow{
		"root": {{ID: "a"}, {ID: "b"}},
		"a":    {{ID: "a1"}, {ID: "a2"}},
		"a1":   {{ID: "a1x"}},
		// b is a leaf.
	}

	if got := countDescendants("root", childrenOf); got != 5 {
		t.Errorf("countDescendants(root) = %d, want 5", got)
	}
	if got := countDescendants("a", childrenOf); got != 3 {
		t.Errorf("countDescendants(a) = %d, want 3", got)
	}
	if got := countDescendants("b", childrenOf); got != 0 {
		t.Errorf("countDescendants(b) = %d, want 0", got)
	}
	if got := countDescendants("missing", childrenOf); got != 0 {
		t.Errorf("countDescendants(missing) = %d, want 0", got)
	}
}

// TestSuffixMore_Format covers the small "…N more" formatter.
func TestSuffixMore_Format(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{1, "…1 more"},
		{12, "…12 more"},
		{0, "…0 more"},
		{999, "…999 more"},
	}
	for _, c := range cases {
		if got := suffixMore(c.n); got != c.want {
			t.Errorf("suffixMore(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestBuildRosterRows_DepthCap exercises the maxDepth branch in
// buildRosterRows: when the tree is deeper than the cap, the deepest
// visible node is marked collapsed + carries the descendant count.
func TestBuildRosterRows_DepthCap(t *testing.T) {
	rows := []agent.AgentRow{
		{ID: "root", Title: "r"},
		{ID: "c1", ParentID: "root", Title: "c1"},
		{ID: "g1", ParentID: "c1", Title: "g1"},
		{ID: "g2", ParentID: "c1", Title: "g2"},
		{ID: "gg1", ParentID: "g1", Title: "gg1"},
		{ID: "ggg1", ParentID: "gg1", Title: "ggg1"},
	}
	out := buildRosterRows(rows, "", 2)

	// At depth cap=2, the depth-2 nodes should be collapsed entries.
	var collapsed []rosterRow
	for _, rr := range out {
		if rr.collapsed {
			collapsed = append(collapsed, rr)
		}
	}
	if len(collapsed) == 0 {
		t.Fatalf("no collapsed rows produced; depth cap not honoured")
	}
	for _, rr := range collapsed {
		if rr.indent < 2 {
			t.Errorf("collapsed row at indent %d, want >= 2", rr.indent)
		}
	}
}

// TestBuildRosterRows_OrphanIsRoot drops a child whose parent isn't in
// the slice; the orphan should be surfaced as a root row rather than
// silently dropped.
func TestBuildRosterRows_OrphanIsRoot(t *testing.T) {
	rows := []agent.AgentRow{
		{ID: "orphan", ParentID: "ghost", Title: "orphaned"},
	}
	out := buildRosterRows(rows, "", 3)
	if len(out) != 1 {
		t.Fatalf("expected 1 row, got %d", len(out))
	}
	if out[0].indent != 0 {
		t.Errorf("orphan indent = %d, want 0 (treated as root)", out[0].indent)
	}
}

// TestBuildRosterRows_ZeroMaxDepthDefaults exercises the maxDepth<=0
// guard - the build helper falls back to defaultMaxDepth so callers
// can't accidentally render a fully-flattened tree.
func TestBuildRosterRows_ZeroMaxDepthDefaults(t *testing.T) {
	rows := []agent.AgentRow{{ID: "x", Title: "x"}}
	out := buildRosterRows(rows, "", 0)
	if len(out) != 1 {
		t.Fatalf("expected 1 row, got %d", len(out))
	}
}

// TestItoa_BasicShape covers the local int-to-string helper, including
// zero (its dedicated branch).
func TestItoa_BasicShape(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{7, "7"},
		{42, "42"},
		{123, "123"},
		{9001, "9001"},
	}
	for _, c := range cases {
		if got := itoa(c.n); got != c.want {
			t.Errorf("itoa(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestTruncate_Edges covers the rune-counting truncation helper across
// the empty / shorter / longer / n==1 branches.
func TestTruncate_Edges(t *testing.T) {
	cases := []struct {
		s    string
		n    int
		want string
	}{
		{"hello", 0, ""},
		{"hello", 1, "…"},
		{"hello", 10, "hello"},
		{"helloworld", 5, "hell…"},
		// Multi-byte runes - "café" is 4 runes but 5 bytes.
		{"café", 4, "café"},
	}
	for _, c := range cases {
		if got := truncate(c.s, c.n); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}

// TestPadRight_Plain covers the plain-string padder.
func TestPadRight_Plain(t *testing.T) {
	if got := padRight("hi", 5); got != "hi   " {
		t.Errorf("padRight(hi, 5) = %q", got)
	}
	if got := padRight("longer", 3); got != "lon" {
		t.Errorf("padRight(longer, 3) = %q", got)
	}
}

// TestRenderRoster_VirtualizationClampsScroll renders a window large
// enough to see all rows then scrolls past the end - the renderer must
// not panic and must clamp end to len(rows).
func TestRenderRoster_VirtualizationClampsScroll(t *testing.T) {
	rows := []rosterRow{
		{row: agent.AgentRow{ID: "aaa", Title: "first", State: agent.StateRunning}},
		{row: agent.AgentRow{ID: "bbb", Title: "second", State: agent.StateRunning}},
	}
	out := renderRoster(rows, rosterRenderOptions{
		width:    120,
		height:   6,
		scroll:   100, // way past the end
		maxDepth: 3,
	})
	if out == "" {
		t.Error("renderRoster with scroll past end returned empty")
	}

	// Scroll negative - guard kicks in, no panic, first row visible.
	out = renderRoster(rows, rosterRenderOptions{
		width:    120,
		height:   6,
		scroll:   -10,
		maxDepth: 3,
	})
	if !strings.Contains(out, "first") {
		t.Error("renderRoster with negative scroll didn't show first row")
	}
}

// TestRenderRoster_CursorMarkerOnSelectedRow pins the post-fix
// behavior: the row whose index matches cursorIdx gets the "›"
// cursor marker. Before this fix ↑/↓ moved the internal cursor but
// no visual feedback fired, so users couldn't tell which row was
// selected.
func TestRenderRoster_CursorMarkerOnSelectedRow(t *testing.T) {
	rows := []rosterRow{
		{row: agent.AgentRow{ID: "aaa", Title: "first", State: agent.StateRunning}},
		{row: agent.AgentRow{ID: "bbb", Title: "second", State: agent.StateRunning}},
		{row: agent.AgentRow{ID: "ccc", Title: "third", State: agent.StateRunning}},
	}
	out := renderRoster(rows, rosterRenderOptions{
		width:     120,
		height:    6,
		cursorIdx: 1,
		maxDepth:  3,
	})
	if !strings.Contains(out, "›") {
		t.Errorf("cursor marker missing from output:\n%s", out)
	}
	// Make sure the marker is on the SECOND visible row by checking
	// the marker appears after "first" and before "third".
	firstIdx := strings.Index(out, "first")
	cursorIdx := strings.Index(out, "›")
	thirdIdx := strings.Index(out, "third")
	if !(firstIdx < cursorIdx && cursorIdx < thirdIdx) {
		t.Errorf("cursor marker not on the expected row (first=%d cursor=%d third=%d):\n%s",
			firstIdx, cursorIdx, thirdIdx, out)
	}
}

// TestRenderRoster_CursorOnFocusedRowDoublesMarker covers the
// "cursor and focus on same row" branch — the renderer paints "▸›"
// as a combined marker so the user can tell both states overlap.
func TestRenderRoster_CursorOnFocusedRowDoublesMarker(t *testing.T) {
	rows := []rosterRow{
		{row: agent.AgentRow{ID: "aaa", Title: "alpha", State: agent.StateRunning}},
	}
	out := renderRoster(rows, rosterRenderOptions{
		width:     120,
		height:    3,
		focusID:   "aaa",
		cursorIdx: 0,
		maxDepth:  3,
	})
	if !strings.Contains(out, "▸›") {
		t.Errorf("combined marker missing:\n%s", out)
	}
}

// TestRenderRoster_EmptyStateMessage proves the helpful prose shows
// up when there are zero agents — previously the pane went blank.
func TestRenderRoster_EmptyStateMessage(t *testing.T) {
	out := renderRoster(nil, rosterRenderOptions{
		width:    120,
		height:   6,
		maxDepth: 3,
	})
	if !strings.Contains(out, "no agents yet") {
		t.Errorf("empty roster should show helpful prose:\n%s", out)
	}
}

// TestRenderRoster_NarrowWidthDropsModel forces width below the
// model-column floor so the dropModel branch fires.
func TestRenderRoster_NarrowWidthDropsModel(t *testing.T) {
	rows := []rosterRow{
		{row: agent.AgentRow{ID: "aaa", Title: "narrow", State: agent.StateRunning, Model: "model-x"}},
	}
	// Render at the minimum width - intentW dips below 12 so model
	// drops out.
	out := renderRoster(rows, rosterRenderOptions{
		width:    50,
		height:   3,
		maxDepth: 3,
	})
	if strings.Contains(out, "model-x") {
		t.Errorf("narrow render should drop model col, got:\n%s", out)
	}
}
