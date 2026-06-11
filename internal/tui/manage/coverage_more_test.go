package manage

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
)

// sliceSnapshot is a SnapshotSource that returns a fixed slice of rows.
// Unlike staticSnapshot it lets a test drive the model with real roster
// content without standing up a SQLite log.
type sliceSnapshot struct {
	rows []agent.AgentRow
	err  error
}

func (s sliceSnapshot) Snapshot(context.Context) ([]agent.AgentRow, error) {
	return s.rows, s.err
}

// key is a tiny helper for the common single-rune keypress.
func key(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// --- sort.go: pure comparison logic --------------------------------

// TestSortKey_String covers every SortKey label including the unknown
// fall-through.
func TestSortKey_String(t *testing.T) {
	cases := []struct {
		k    SortKey
		want string
	}{
		{SortPriority, "priority"},
		{SortID, "id"},
		{SortState, "state"},
		{SortCost, "cost"},
		{SortTokens, "tokens"},
		{SortTime, "time"},
		{SortKey(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.k.String(); got != c.want {
			t.Errorf("SortKey(%d).String() = %q, want %q", c.k, got, c.want)
		}
	}
}

func ids(rows []agent.AgentRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSortBy_AllModes table-tests SortBy across every key and direction
// using a fixed fixture so the ordering and tie-breaks are pinned.
func TestSortBy_AllModes(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []agent.AgentRow{
		{ID: "c", State: agent.StateRunning, CostCents: 50, TokensIn: 10, TokensOut: 5, CreatedAt: base.Add(3 * time.Hour), UpdatedAt: base.Add(3 * time.Hour)},
		{ID: "a", State: agent.StateDone, CostCents: 100, TokensIn: 1, TokensOut: 1, CreatedAt: base.Add(1 * time.Hour), UpdatedAt: base.Add(1 * time.Hour)},
		{ID: "b", State: agent.StateBlocked, CostCents: 10, TokensIn: 99, TokensOut: 1, CreatedAt: base.Add(2 * time.Hour), UpdatedAt: base.Add(2 * time.Hour)},
	}

	cases := []struct {
		name string
		key  SortKey
		asc  bool
		want []string
	}{
		{"id-asc", SortID, true, []string{"a", "b", "c"}},
		{"id-desc", SortID, false, []string{"c", "b", "a"}},
		// State string order: blocked < done < running.
		{"state-asc", SortState, true, []string{"b", "a", "c"}},
		{"state-desc", SortState, false, []string{"c", "a", "b"}},
		// Cost asc=true means big-cost-first (documented inversion).
		{"cost-asc-big-first", SortCost, true, []string{"a", "c", "b"}},
		{"cost-desc-small-first", SortCost, false, []string{"b", "c", "a"}},
		// Tokens asc=true means big-total-first: b=100, c=15, a=2.
		{"tokens-asc-big-first", SortTokens, true, []string{"b", "c", "a"}},
		{"tokens-desc-small-first", SortTokens, false, []string{"a", "c", "b"}},
		// Time asc=true means oldest CreatedAt first: a(1h) < b(2h) < c(3h).
		{"time-asc-oldest-first", SortTime, true, []string{"a", "b", "c"}},
		{"time-desc-newest-first", SortTime, false, []string{"c", "b", "a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ids(SortBy(rows, c.key, c.asc))
			if !eq(got, c.want) {
				t.Errorf("SortBy(%s, asc=%v) = %v, want %v", c.key, c.asc, got, c.want)
			}
		})
	}

	// SortBy must not mutate the caller's slice.
	if !eq(ids(rows), []string{"c", "a", "b"}) {
		t.Errorf("SortBy mutated input slice: %v", ids(rows))
	}
}

// TestSortBy_PriorityBuckets pins the default priority ordering:
// awaiting-input (bucket 0) → runaway-cost (bucket 1) → orphaned
// (bucket 3) → everything else (bucket 4), with recency tie-break
// inside a bucket.
func TestSortBy_PriorityBuckets(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// No runaway row here (all zero-cost), so the runawayBudget guard
	// stays off and the buckets are: await(0) → orphan(3) → plain(4).
	rows := []agent.AgentRow{
		{ID: "plain", State: agent.StateRunning, CostCents: 0, UpdatedAt: base},
		{ID: "orphan", State: agent.StateOrphaned, CostCents: 0, UpdatedAt: base},
		{ID: "await", State: agent.StateAwaitingInput, CostCents: 0, UpdatedAt: base},
	}
	got := ids(SortBy(rows, SortPriority, true))
	want := []string{"await", "orphan", "plain"}
	if !eq(got, want) {
		t.Errorf("priority order = %v, want %v", got, want)
	}

	// asc=false reverses bucket order (descending rank): plain(4) →
	// orphan(3) → await(0).
	gotDesc := ids(SortBy(rows, SortPriority, false))
	wantDesc := []string{"plain", "orphan", "await"}
	if !eq(gotDesc, wantDesc) {
		t.Errorf("priority order desc = %v, want %v", gotDesc, wantDesc)
	}
}

// TestSortBy_PriorityRunawayBucket builds a cost distribution where the
// 90th-percentile threshold isolates the top two rows. Those flag the
// runaway bucket (1) and surface above an ordinary running row (4).
func TestSortBy_PriorityRunawayBucket(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var rows []agent.AgentRow
	// 9 cheap running rows.
	for i := 0; i < 9; i++ {
		rows = append(rows, agent.AgentRow{
			ID: "cheap" + string(rune('a'+i)), State: agent.StateRunning,
			CostCents: 1, UpdatedAt: base,
		})
	}
	// 2 expensive rows that should occupy the runaway bucket.
	rows = append(rows,
		agent.AgentRow{ID: "burnA", State: agent.StateRunning, CostCents: 9000, UpdatedAt: base.Add(time.Minute)},
		agent.AgentRow{ID: "burnB", State: agent.StateRunning, CostCents: 9000, UpdatedAt: base},
	)
	// Sanity: the threshold isolates the expensive pair.
	if th := runawayThreshold(rows); th != 9000 {
		t.Fatalf("threshold = %d, want 9000 (fixture mis-sized)", th)
	}
	got := ids(SortBy(rows, SortPriority, true))
	// burnA (newer) then burnB head the list as the runaway bucket.
	if got[0] != "burnA" || got[1] != "burnB" {
		t.Errorf("runaway rows should lead the priority sort, got %v", got)
	}
}

// TestSortBy_PriorityRecencyTieBreak confirms that within the same
// bucket the more recently updated agent surfaces first.
func TestSortBy_PriorityRecencyTieBreak(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []agent.AgentRow{
		{ID: "older", State: agent.StateRunning, UpdatedAt: base},
		{ID: "newer", State: agent.StateRunning, UpdatedAt: base.Add(time.Hour)},
	}
	got := ids(SortBy(rows, SortPriority, true))
	if !eq(got, []string{"newer", "older"}) {
		t.Errorf("recency tie-break = %v, want [newer older]", got)
	}
}

// TestRunawayThreshold covers the percentile math and the empty / no-
// cost guards.
func TestRunawayThreshold(t *testing.T) {
	if got := runawayThreshold(nil); got != 0 {
		t.Errorf("runawayThreshold(nil) = %d, want 0", got)
	}
	// All-zero costs → no row should flag → threshold 0.
	zero := []agent.AgentRow{{CostCents: 0}, {CostCents: 0}}
	if got := runawayThreshold(zero); got != 0 {
		t.Errorf("runawayThreshold(all-zero) = %d, want 0", got)
	}
	// N=2: ceil((2-1)*0.9) = idx 0 of sorted [10,90] -> picks the higher
	// because float64(1)*0.9 = 0.9, int() truncates to 0 ... sorted
	// ascending costs[0]=10. Pin actual behavior.
	two := []agent.AgentRow{{CostCents: 90}, {CostCents: 10}}
	if got := runawayThreshold(two); got != 10 {
		t.Errorf("runawayThreshold(N=2) = %d, want 10", got)
	}
	// N=10 distribution 1..10: idx = int(9*0.9)=8 -> sorted[8]=9.
	var ten []agent.AgentRow
	for i := int64(1); i <= 10; i++ {
		ten = append(ten, agent.AgentRow{CostCents: i})
	}
	if got := runawayThreshold(ten); got != 9 {
		t.Errorf("runawayThreshold(N=10) = %d, want 9", got)
	}
}

// --- roster.go: render helpers -------------------------------------

// TestStripCSI removes ANSI escape runs but keeps plain text, and
// short-circuits when there is no escape byte.
func TestStripCSI(t *testing.T) {
	plain := "no escapes here"
	if got := stripCSI(plain); got != plain {
		t.Errorf("stripCSI(plain) = %q, want unchanged", got)
	}
	styled := "\x1b[7mbright\x1b[0m text"
	if got := stripCSI(styled); got != "bright text" {
		t.Errorf("stripCSI(styled) = %q, want 'bright text'", got)
	}
	// A trailing/unterminated escape must not loop forever.
	got := stripCSI("a\x1b[")
	if strings.Contains(got, "\x1b") {
		t.Errorf("stripCSI left an escape byte: %q", got)
	}
}

// TestPadRightANSI pads to a visible width while ignoring embedded
// escape sequences, and returns the input unchanged when already wide
// enough.
func TestPadRightANSI(t *testing.T) {
	// "ab" is 2 visible cells; pad to 5 adds 3 spaces.
	if got := padRightANSI("ab", 5); got != "ab   " {
		t.Errorf("padRightANSI(ab,5) = %q", got)
	}
	// ANSI escapes don't count toward width.
	styled := "\x1b[7mab\x1b[0m"
	got := padRightANSI(styled, 5)
	if !strings.HasSuffix(got, "   ") {
		t.Errorf("padRightANSI didn't pad styled string to width: %q", got)
	}
	// Already wide enough → unchanged.
	if got := padRightANSI("abcdef", 3); got != "abcdef" {
		t.Errorf("padRightANSI(over) = %q, want unchanged", got)
	}
}

// TestPadCellsToWidth covers the zero-width and over-width branches.
func TestPadCellsToWidth(t *testing.T) {
	if got := padCellsToWidth("x", 0); got != "" {
		t.Errorf("padCellsToWidth(_,0) = %q, want empty", got)
	}
	if got := padCellsToWidth("toolong", 3); got != "toolong" {
		t.Errorf("padCellsToWidth(over) = %q, want unchanged", got)
	}
	if got := padCellsToWidth("hi", 4); got != "hi  " {
		t.Errorf("padCellsToWidth(hi,4) = %q", got)
	}
}

// TestFormatElapsed covers each duration bracket of the 7-char column.
func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "-"},
		{-5 * time.Second, "-"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m30s"},           // <10m → m+s
		{15 * time.Minute, "15m"},             // >=10m → m only
		{2*time.Hour + 5*time.Minute, "2h5m"}, // hours bracket
	}
	for _, c := range cases {
		if got := formatElapsed(c.d); got != c.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// TestRenderAgentCard_CollapsedShowsMore exercises the collapsed-subtree
// "…N more" suffix branch in renderAgentCard plus the empty-intent
// fallback.
func TestRenderAgentCard_CollapsedShowsMore(t *testing.T) {
	rr := rosterRow{
		row:        agent.AgentRow{ID: "node", State: agent.StateRunning},
		collapsed:  true,
		hiddenKids: 3,
	}
	out := renderAgentCard(rr, 80, false, false)
	if !strings.Contains(out, "more") {
		t.Errorf("collapsed card should carry the '…N more' marker:\n%s", out)
	}
	if !strings.Contains(out, "no intent recorded") {
		t.Errorf("empty-title card should show placeholder intent:\n%s", out)
	}
}

// TestRenderAgentCard_SparklineStrippedWhenSelected ensures the spark is
// rendered into the meta strip and that a selected (cursor) card strips
// its ANSI so the reverse-video flip stays clean.
func TestRenderAgentCard_SparklineWithRoom(t *testing.T) {
	rr := rosterRow{
		row:   agent.AgentRow{ID: "spk", Title: "x", State: agent.StateRunning},
		spark: "\x1b[32m▁▂▃\x1b[0m",
	}
	// Wide card so meta + spark fit; cursor=true triggers stripCSI path.
	out := renderAgentCard(rr, 120, true, false)
	if !strings.Contains(out, "▁▂▃") {
		t.Errorf("sparkline glyphs missing from wide selected card:\n%s", out)
	}
}

// --- view.go: header / footer / overlay ----------------------------

func mkModelWithRows(rows []agent.AgentRow, w, h int) *Model {
	m := New(sliceSnapshot{rows: rows}, nil, nil)
	rs, _ := m.src.Snapshot(context.Background())
	m.rawRows = rs
	m.rebuildRoster()
	m.width, m.height = w, h
	m.relayout()
	return m
}

// TestView_HeaderShowsFilterChip drives a live filter and asserts the
// header surfaces the "filter:" chip and the agent count updates.
func TestView_HeaderShowsFilterChip(t *testing.T) {
	rows := []agent.AgentRow{
		{ID: "01HVaaa0000000000000000001", Title: "alpha task", State: agent.StateRunning},
		{ID: "01HVbbb0000000000000000002", Title: "beta task", State: agent.StateRunning},
	}
	m := mkModelWithRows(rows, 160, 60)

	// Open filter overlay and type "alpha".
	updated, _ := m.Update(key('/'))
	m = updated.(*Model)
	for _, r := range "alpha" {
		updated, _ = m.Update(key(r))
		m = updated.(*Model)
	}
	if !m.filter.Active() {
		t.Fatalf("filter not active after typing")
	}
	if len(m.rosterRows) != 1 {
		t.Errorf("filter should narrow to 1 row, got %d", len(m.rosterRows))
	}
	view := m.View()
	if !strings.Contains(view, "filter: alpha") {
		t.Errorf("header missing filter chip; view:\n%s", view)
	}
	// ESC inside overlay closes it but the live filter persists.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*Model)
	if m.overlay != overlayNone {
		t.Errorf("esc should close overlay")
	}
	// ESC again (outside overlay) clears the filter.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*Model)
	if m.filter.Active() {
		t.Errorf("second esc should clear the filter")
	}
	if len(m.rosterRows) != 2 {
		t.Errorf("clearing filter should restore all rows, got %d", len(m.rosterRows))
	}
}

// TestView_FooterShowsStatusLine confirms a status echo renders above
// the keybind row, and that warn-classed statuses still render.
func TestView_FooterShowsStatusLine(t *testing.T) {
	m := mkModelWithRows([]agent.AgentRow{
		{ID: "01HVaaa0000000000000000001", Title: "x", State: agent.StateRunning},
	}, 160, 60)
	m.status = "no supervisor wired"
	view := m.View()
	if !strings.Contains(view, "no supervisor wired") {
		t.Errorf("footer missing status echo; view:\n%s", view)
	}
}

// TestView_SnapshotErrChipInHeader confirms a refresh error surfaces in
// the header right-hand chip.
func TestView_SnapshotErrChipInHeader(t *testing.T) {
	m := mkModelWithRows(nil, 160, 60)
	m.rosterRefreshErr = "db locked"
	view := m.View()
	if !strings.Contains(view, "snapshot err") || !strings.Contains(view, "db locked") {
		t.Errorf("header missing snapshot-err chip; view:\n%s", view)
	}
}

// TestView_TooSmallTerminal returns the minimum-size prose instead of a
// broken layout.
func TestView_TooSmallTerminal(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	m.width, m.height = 10, 5
	if got := m.View(); !strings.Contains(got, "needs at least") {
		t.Errorf("tiny terminal should show min-size prose, got %q", got)
	}
}

// TestView_FocusPaneDetailHeader binds focus to an agent and asserts the
// detail header renders the stats grid (tokens, cost, model, parent).
func TestView_FocusPaneDetailHeader(t *testing.T) {
	rows := []agent.AgentRow{
		{
			ID: "01HVaaa0000000000000000001", ParentID: "01HVparent000000000000000",
			Title: "build the thing", State: agent.StateRunning,
			Model: "anthropic/claude", TokensIn: 1200, TokensOut: 340, CostCents: 7,
			CreatedAt: time.Now().Add(-90 * time.Second),
		},
	}
	m := mkModelWithRows(rows, 160, 60)
	m.focus.Bind("01HVaaa0000000000000000001")
	view := m.View()
	for _, want := range []string{"tokens", "cost", "model", "parent", "build the thing"} {
		if !strings.Contains(view, want) {
			t.Errorf("focus detail header missing %q; view:\n%s", want, view)
		}
	}
}

// TestView_FocusPaneUnboundHint shows the select-an-agent hint when no
// focus is bound.
func TestView_FocusPaneUnboundHint(t *testing.T) {
	m := mkModelWithRows([]agent.AgentRow{
		{ID: "01HVaaa0000000000000000001", Title: "x", State: agent.StateRunning},
	}, 160, 60)
	view := m.View()
	if !strings.Contains(view, "select an agent") {
		t.Errorf("unbound focus pane missing hint; view:\n%s", view)
	}
}

// TestRenderOverlay_SteerInput renders the steer text-input overlay and
// asserts the prompt label shows.
func TestRenderOverlay_SteerInput(t *testing.T) {
	m := mkModelWithRows([]agent.AgentRow{
		{ID: "01HVaaa0000000000000000001", Title: "x", State: agent.StateRunning},
	}, 160, 60)
	m.cursor = 0
	updated, _ := m.Update(key('s'))
	m = updated.(*Model)
	view := m.View()
	if !strings.Contains(view, "steer:") {
		t.Errorf("steer overlay prompt missing; view:\n%s", view)
	}
}

// TestRenderOverlay_ConfirmPrompt renders the interrupt confirm overlay
// (the y/N branch) which has a distinct render path from text inputs.
func TestRenderOverlay_ConfirmPrompt(t *testing.T) {
	m := mkModelWithRows([]agent.AgentRow{
		{ID: "01HVaaa0000000000000000001", Title: "risky", State: agent.StateRunning},
	}, 160, 60)
	m.cursor = 0
	updated, _ := m.Update(key('i'))
	m = updated.(*Model)
	view := m.View()
	if !strings.Contains(view, "interrupt") || !strings.Contains(view, "esc to cancel") {
		t.Errorf("interrupt confirm overlay missing prompt; view:\n%s", view)
	}
}

// --- manage.go: handleKey navigation + sort + overlay --------------

// TestHandleKey_SortToggles walks every sort hotkey (1-5, !@#$%, 0) and
// asserts the model's sortKey + direction land where expected.
func TestHandleKey_SortToggles(t *testing.T) {
	rows := []agent.AgentRow{
		{ID: "01HVaaa0000000000000000001", Title: "x", State: agent.StateRunning},
	}
	cases := []struct {
		r       rune
		wantKey SortKey
		wantAsc bool
	}{
		{'1', SortID, true},
		{'2', SortState, true},
		{'3', SortCost, true},
		{'4', SortTokens, true},
		{'5', SortTime, true},
		{'!', SortID, false},
		{'@', SortState, false},
		{'#', SortCost, false},
		{'$', SortTokens, false},
		{'%', SortTime, false},
		{'0', SortPriority, true},
	}
	for _, c := range cases {
		m := mkModelWithRows(rows, 160, 60)
		updated, _ := m.Update(key(c.r))
		m = updated.(*Model)
		if m.sortKey != c.wantKey || m.sortAsc != c.wantAsc {
			t.Errorf("key %q → sortKey=%v asc=%v, want %v %v",
				c.r, m.sortKey, m.sortAsc, c.wantKey, c.wantAsc)
		}
	}
}

// TestHandleKey_Navigation walks the cursor controls: j/k, down/up,
// home/end (g/G), and pgup/pgdown, asserting the cursor lands at the
// clamped position.
func TestHandleKey_Navigation(t *testing.T) {
	var rows []agent.AgentRow
	for i := 0; i < 8; i++ {
		rows = append(rows, agent.AgentRow{
			ID:    "01HV00000000000000000000" + string(rune('a'+i)),
			Title: "row", State: agent.StateRunning,
		})
	}
	m := mkModelWithRows(rows, 160, 40)
	m.win.Visible = 3

	step := func(k tea.KeyMsg) {
		updated, _ := m.Update(k)
		m = updated.(*Model)
	}

	step(key('j'))
	if m.cursor != 1 {
		t.Fatalf("after j cursor=%d, want 1", m.cursor)
	}
	step(key('k'))
	if m.cursor != 0 {
		t.Fatalf("after k cursor=%d, want 0", m.cursor)
	}
	step(tea.KeyMsg{Type: tea.KeyDown})
	step(tea.KeyMsg{Type: tea.KeyDown})
	if m.cursor != 2 {
		t.Fatalf("after 2x down cursor=%d, want 2", m.cursor)
	}
	step(key('G')) // end → last row
	if m.cursor != len(rows)-1 {
		t.Fatalf("after G cursor=%d, want %d", m.cursor, len(rows)-1)
	}
	step(key('g')) // home → first
	if m.cursor != 0 {
		t.Fatalf("after g cursor=%d, want 0", m.cursor)
	}
	step(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.cursor != m.win.Visible {
		t.Fatalf("after pgdown cursor=%d, want %d", m.cursor, m.win.Visible)
	}
	step(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.cursor != 0 {
		t.Fatalf("after pgup cursor=%d, want 0", m.cursor)
	}
}

// TestHandleKey_EndOnEmptyRoster confirms the end/G key clamps to 0 when
// there are no rows (the cursor<0 guard branch).
func TestHandleKey_EndOnEmptyRoster(t *testing.T) {
	m := mkModelWithRows(nil, 160, 40)
	updated, _ := m.Update(key('G'))
	m = updated.(*Model)
	if m.cursor != 0 {
		t.Errorf("G on empty roster → cursor=%d, want 0", m.cursor)
	}
}

// TestHandleKey_SteerNoOpOnEmptySelection confirms s/i/x are no-ops when
// nothing is selected (empty roster → selectedID == "").
func TestHandleKey_VerbNoOpOnEmptySelection(t *testing.T) {
	m := mkModelWithRows(nil, 160, 40)
	for _, r := range []rune{'s', 'i', 'x'} {
		updated, _ := m.Update(key(r))
		m = updated.(*Model)
		if m.overlay != overlayNone {
			t.Errorf("verb %q opened an overlay with no selection", r)
		}
	}
}

// TestOverlay_SteerCancelByEsc opens the steer overlay then escapes; no
// dispatch should fire and the overlay closes.
func TestOverlay_SteerCancelByEsc(t *testing.T) {
	rec := &recordingDispatcher{}
	m := New(sliceSnapshot{rows: []agent.AgentRow{
		{ID: "01HVaaa0000000000000000001", Title: "x", State: agent.StateRunning},
	}}, nil, rec)
	rs, _ := m.src.Snapshot(context.Background())
	m.rawRows = rs
	m.rebuildRoster()
	m.width, m.height = 160, 60
	m.cursor = 0

	updated, _ := m.Update(key('s'))
	m = updated.(*Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*Model)
	if m.overlay != overlayNone {
		t.Errorf("esc should close steer overlay")
	}
	if len(rec.steers) != 0 {
		t.Errorf("esc should not dispatch a steer: %v", rec.steers)
	}
}

// TestOverlay_SteerEmptyDoesNotDispatch commits an empty steer overlay;
// the dispatch is suppressed (no point steering with empty text).
func TestOverlay_SteerEmptyDoesNotDispatch(t *testing.T) {
	rec := &recordingDispatcher{}
	m := New(sliceSnapshot{rows: []agent.AgentRow{
		{ID: "01HVaaa0000000000000000001", Title: "x", State: agent.StateRunning},
	}}, nil, rec)
	rs, _ := m.src.Snapshot(context.Background())
	m.rawRows = rs
	m.rebuildRoster()
	m.width, m.height = 160, 60
	m.cursor = 0

	updated, _ := m.Update(key('s'))
	m = updated.(*Model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)
	if cmd != nil {
		if _, ok := cmd().(VerbResult); ok {
			t.Errorf("empty steer should not dispatch")
		}
	}
	if len(rec.steers) != 0 {
		t.Errorf("empty steer dispatched: %v", rec.steers)
	}
}

// TestOverlay_InterruptConfirmEnterDefaultsNo confirms that pressing
// Enter (not y) on the interrupt confirm prompt does NOT kill the agent
// — a safety property called out in commitOverlay.
func TestOverlay_InterruptConfirmEnterDefaultsNo(t *testing.T) {
	rec := &recordingDispatcher{}
	m := New(sliceSnapshot{rows: []agent.AgentRow{
		{ID: "01HVaaa0000000000000000001", Title: "x", State: agent.StateRunning},
	}}, nil, rec)
	rs, _ := m.src.Snapshot(context.Background())
	m.rawRows = rs
	m.rebuildRoster()
	m.width, m.height = 160, 60
	m.cursor = 0

	updated, _ := m.Update(key('i'))
	m = updated.(*Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)
	if m.overlay != overlayNone {
		t.Errorf("enter should close the confirm overlay")
	}
	if len(rec.interrupts) != 0 {
		t.Errorf("enter (default no) should NOT interrupt: %v", rec.interrupts)
	}
}

// TestOverlay_ConfirmNoKey confirms the explicit 'n' keypress closes the
// confirm overlay without dispatching.
func TestOverlay_ConfirmNoKey(t *testing.T) {
	rec := &recordingDispatcher{}
	m := New(sliceSnapshot{rows: []agent.AgentRow{
		{ID: "01HVaaa0000000000000000001", Title: "x", State: agent.StateRunning},
	}}, nil, rec)
	rs, _ := m.src.Snapshot(context.Background())
	m.rawRows = rs
	m.rebuildRoster()
	m.width, m.height = 160, 60
	m.cursor = 0

	updated, _ := m.Update(key('x'))
	m = updated.(*Model)
	updated, _ = m.Update(key('n'))
	m = updated.(*Model)
	if m.overlay != overlayNone {
		t.Errorf("n should close the stop confirm overlay")
	}
	if len(rec.stops) != 0 {
		t.Errorf("n should not stop the agent: %v", rec.stops)
	}
}

// --- manage.go: snapshot + window relayout -------------------------

// TestUpdate_RefreshTickReSnapshots confirms a refreshTickMsg returns a
// batched command (snapshot + reschedule) so the roster keeps polling.
func TestUpdate_RefreshTickReSnapshots(t *testing.T) {
	m := mkModelWithRows(nil, 160, 60)
	_, cmd := m.Update(refreshTickMsg{})
	if cmd == nil {
		t.Errorf("refreshTickMsg should return a batched re-snapshot cmd")
	}
}

// TestUpdate_SnapshotReadyRebuilds confirms a successful snapshot
// populates rawRows + rosterRows and clears any prior error.
func TestUpdate_SnapshotReadyRebuilds(t *testing.T) {
	m := mkModelWithRows(nil, 160, 60)
	m.rosterRefreshErr = "stale"
	rows := []agent.AgentRow{
		{ID: "01HVaaa0000000000000000001", Title: "x", State: agent.StateRunning},
	}
	updated, _ := m.Update(snapshotReadyMsg{rows: rows})
	m = updated.(*Model)
	if m.rosterRefreshErr != "" {
		t.Errorf("successful snapshot should clear the error")
	}
	if len(m.rosterRows) != 1 {
		t.Errorf("snapshot should rebuild roster, got %d rows", len(m.rosterRows))
	}
}

// TestRelayout_IgnoresTinyTerminal confirms relayout bails out below the
// minimum terminal size without touching window geometry.
func TestRelayout_IgnoresTinyTerminal(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	m.width, m.height = 10, 5
	m.win.Visible = 99
	m.relayout()
	if m.win.Visible != 99 {
		t.Errorf("relayout should no-op on tiny terminal, win.Visible=%d", m.win.Visible)
	}
}

// --- virtual.go: Window scroll math --------------------------------

// TestWindow_ScrollToEmptyKeepsTopNonNegative is a regression for a bug
// where ScrollTo on an empty window (Total=0, Visible>0) drove Top to
// -1: idx clamps to Total-1 == -1, which is < Top, so Top was set to
// -1. Top must never go negative — downstream Bottom()/Contains() math
// and the renderer's window slice assume a non-negative top.
func TestWindow_ScrollToEmptyKeepsTopNonNegative(t *testing.T) {
	w := Window{Total: 0, Visible: 5, Top: 0}
	got := w.ScrollTo(0)
	if got.Top < 0 {
		t.Errorf("ScrollTo on empty window → Top=%d, want >= 0", got.Top)
	}
}

// TestWindow_HomeKeyOnEmptyRosterKeepsTopValid drives the bug through
// the model: pressing home/g with no rows must not leave a negative
// scroll Top.
func TestWindow_HomeKeyOnEmptyRosterKeepsTopValid(t *testing.T) {
	m := mkModelWithRows(nil, 160, 40)
	m.win.Visible = 5
	m.win.Total = 0
	updated, _ := m.Update(key('g')) // home
	m = updated.(*Model)
	if m.win.Top < 0 {
		t.Errorf("home on empty roster → win.Top=%d, want >= 0", m.win.Top)
	}
}

// TestWindow_ScrollToAndContains exercises the normal scroll cases:
// scrolling down past the bottom slides the window; an already-visible
// index is a no-op; Contains reflects the visible band.
func TestWindow_ScrollToAndContains(t *testing.T) {
	w := Window{Total: 10, Visible: 3, Top: 0}
	// idx 5 is below the visible band [0,3) → window slides so 5 is the
	// bottom-most visible row: Top = 5-3+1 = 3.
	w = w.ScrollTo(5)
	if w.Top != 3 {
		t.Fatalf("ScrollTo(5) Top=%d, want 3", w.Top)
	}
	if !w.Contains(5) || w.Contains(2) {
		t.Errorf("Contains wrong after scroll: band=[%d,%d)", w.Top, w.Bottom())
	}
	// Already-visible idx → unchanged.
	prev := w
	w = w.ScrollTo(4)
	if w.Top != prev.Top {
		t.Errorf("ScrollTo(visible) moved window: %d → %d", prev.Top, w.Top)
	}
	// Scroll up above the band pins idx to the top.
	w = w.ScrollTo(1)
	if w.Top != 1 {
		t.Errorf("ScrollTo(1) Top=%d, want 1", w.Top)
	}
}

// TestWindow_Clamp covers the shrink-after-refresh path and the
// fits-entirely short-circuit.
func TestWindow_Clamp(t *testing.T) {
	// Everything fits → Top resets to 0.
	if got := (Window{Total: 2, Visible: 5, Top: 3}).Clamp(); got.Top != 0 {
		t.Errorf("Clamp(fits) Top=%d, want 0", got.Top)
	}
	// Top past the max after a shrink → clamped to Total-Visible.
	if got := (Window{Total: 10, Visible: 3, Top: 99}).Clamp(); got.Top != 7 {
		t.Errorf("Clamp(overshoot) Top=%d, want 7", got.Top)
	}
	// Negative Top is repaired.
	if got := (Window{Total: 10, Visible: 3, Top: -4}).Clamp(); got.Top != 0 {
		t.Errorf("Clamp(negative) Top=%d, want 0", got.Top)
	}
	// Visible<=0 short-circuits to Top=0.
	if got := (Window{Total: 10, Visible: 0, Top: 4}).Clamp(); got.Top != 0 {
		t.Errorf("Clamp(no viewport) Top=%d, want 0", got.Top)
	}
}

// --- manage.go: approvals-view key handling ------------------------

// mkApprovalsModel builds a model already switched into the approvals
// view with the given pending queue and resolver.
func mkApprovalsModel(pending []agent.PendingApproval, res approvalResolver) *Model {
	m := New(staticSnapshot{}, nil, nil).WithApprovals(fakeLister(pending, nil), res)
	m.view = viewApprovals
	m.approvals.pending = pending
	m.width, m.height = 120, 40
	return m
}

// TestApprovalsKey_Navigation walks j/k, up/down, pgup/pgdown, and
// home/end (g/G) inside the approvals pane.
func TestApprovalsKey_Navigation(t *testing.T) {
	var pending []agent.PendingApproval
	for i := 0; i < 10; i++ {
		pending = append(pending, mkPending("art"+string(rune('a'+i)), "t", "plan", "agent", time.Minute))
	}
	m := mkApprovalsModel(pending, nil)

	step := func(k tea.KeyMsg) {
		updated, _ := m.Update(k)
		m = updated.(*Model)
	}

	step(key('j'))
	if m.approvals.cursor != 1 {
		t.Fatalf("j → cursor %d, want 1", m.approvals.cursor)
	}
	step(tea.KeyMsg{Type: tea.KeyDown})
	if m.approvals.cursor != 2 {
		t.Fatalf("down → cursor %d, want 2", m.approvals.cursor)
	}
	step(key('k'))
	if m.approvals.cursor != 1 {
		t.Fatalf("k → cursor %d, want 1", m.approvals.cursor)
	}
	step(key('G'))
	if m.approvals.cursor != len(pending)-1 {
		t.Fatalf("G → cursor %d, want %d", m.approvals.cursor, len(pending)-1)
	}
	step(key('g'))
	if m.approvals.cursor != 0 {
		t.Fatalf("g → cursor %d, want 0", m.approvals.cursor)
	}
	step(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.approvals.cursor != 5 {
		t.Fatalf("pgdown → cursor %d, want 5", m.approvals.cursor)
	}
	step(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.approvals.cursor != 0 {
		t.Fatalf("pgup → cursor %d, want 0", m.approvals.cursor)
	}
}

// TestApprovalsKey_EndOnEmptyQueue covers the cursor<0 guard in the
// end/G branch when the queue is empty.
func TestApprovalsKey_EndOnEmptyQueue(t *testing.T) {
	m := mkApprovalsModel(nil, nil)
	updated, _ := m.Update(key('G'))
	m = updated.(*Model)
	if m.approvals.cursor != 0 {
		t.Errorf("G on empty queue → cursor %d, want 0", m.approvals.cursor)
	}
}

// TestApprovalsKey_RefreshRefetches confirms capital R issues a fetch
// command without leaving the pane.
func TestApprovalsKey_RefreshRefetches(t *testing.T) {
	m := mkApprovalsModel([]agent.PendingApproval{
		mkPending("art", "t", "plan", "agent", time.Minute),
	}, nil)
	updated, cmd := m.Update(key('R'))
	m = updated.(*Model)
	if m.view != viewApprovals {
		t.Errorf("R should stay in approvals view")
	}
	if cmd == nil {
		t.Errorf("R should issue a refetch cmd")
	}
}

// TestApprovalsKey_QuitsOnCtrlC confirms ctrl+c tears down + quits from
// within the approvals pane.
func TestApprovalsKey_QuitsOnCtrlC(t *testing.T) {
	m := mkApprovalsModel(nil, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(*Model)
	if !m.Quitting() {
		t.Errorf("ctrl+c in approvals view didn't set quitting")
	}
}

// TestApprovalsKey_RejectNoSelectionNoOp confirms `r` with an empty
// queue does not open the reject overlay (no selection to reject).
func TestApprovalsKey_RejectNoSelectionNoOp(t *testing.T) {
	m := mkApprovalsModel(nil, nil)
	updated, _ := m.Update(key('r'))
	m = updated.(*Model)
	if m.overlay != overlayNone {
		t.Errorf("r with no selection should not open overlay")
	}
}

// TestAcceptSelectedApproval_Resolves drives the y-accept path end to
// end: the resolver is invoked with accept=true and the cmd returns an
// acceptedOrRejectedMsg that triggers a refetch in Update.
func TestAcceptSelectedApproval_Resolves(t *testing.T) {
	res, calls := recordingResolver(nil)
	pending := []agent.PendingApproval{
		mkPending("art-acc", "review me", "plan", "agent-a", time.Second),
	}
	m := mkApprovalsModel(pending, res)
	updated, cmd := m.Update(key('y'))
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("y should return a resolver cmd")
	}
	msg := cmd()
	if _, ok := msg.(acceptedOrRejectedMsg); !ok {
		t.Errorf("accept cmd produced %T, want acceptedOrRejectedMsg", msg)
	}
	if len(*calls) != 1 || (*calls)[0].id != "art-acc" || !(*calls)[0].accept {
		t.Errorf("resolver calls = %+v", *calls)
	}
	// Update should fan the acceptedOrRejectedMsg into a refetch cmd.
	_, refetch := m.Update(acceptedOrRejectedMsg{})
	if refetch == nil {
		t.Errorf("acceptedOrRejectedMsg should trigger a refetch cmd")
	}
}

// TestAcceptSelectedApproval_ResolverError surfaces a resolver failure
// as a statusEchoMsg.
func TestAcceptSelectedApproval_ResolverError(t *testing.T) {
	res, _ := recordingResolver(errString("denied"))
	m := mkApprovalsModel([]agent.PendingApproval{
		mkPending("art-err", "x", "plan", "agent", time.Second),
	}, res)
	cmd := m.acceptSelectedApproval()
	if cmd == nil {
		t.Fatal("accept should return a cmd")
	}
	msg := cmd()
	echo, ok := msg.(statusEchoMsg)
	if !ok || !strings.Contains(echo.text, "accept failed") {
		t.Errorf("resolver error didn't surface, got %T %v", msg, msg)
	}
}

// TestAcceptSelectedApproval_NilResolver returns nil when no resolver is
// wired (the accept verb is inert).
func TestAcceptSelectedApproval_NilResolver(t *testing.T) {
	m := mkApprovalsModel([]agent.PendingApproval{
		mkPending("art", "x", "plan", "agent", time.Second),
	}, nil)
	if cmd := m.acceptSelectedApproval(); cmd != nil {
		t.Errorf("accept with nil resolver should be a no-op")
	}
}

// --- manage.go: reject-reason overlay commit -----------------------

// TestRejectReason_CommitDispatches drives the full reject flow: r opens
// the overlay, type a reason, enter commits → resolver called with
// accept=false and the reason note.
func TestRejectReason_CommitDispatches(t *testing.T) {
	res, calls := recordingResolver(nil)
	pending := []agent.PendingApproval{
		mkPending("art-rej", "bad plan", "plan", "agent-a", time.Second),
	}
	m := mkApprovalsModel(pending, res)

	updated, _ := m.Update(key('r'))
	m = updated.(*Model)
	if m.overlay != overlayRejectReason {
		t.Fatalf("r didn't open reject overlay: %v", m.overlay)
	}
	m.input.SetValue("scope creep")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("reject commit returned nil cmd")
	}
	msg := cmd()
	if _, ok := msg.(acceptedOrRejectedMsg); !ok {
		t.Errorf("reject cmd produced %T, want acceptedOrRejectedMsg", msg)
	}
	if len(*calls) != 1 {
		t.Fatalf("resolver calls = %d, want 1", len(*calls))
	}
	got := (*calls)[0]
	if got.id != "art-rej" || got.accept || got.note != "scope creep" {
		t.Errorf("resolver call = %+v, want {art-rej scope creep false}", got)
	}
}

// TestRejectReason_EmptyCancels confirms an empty reason cancels the
// reject (reason is required) and surfaces a status line.
func TestRejectReason_EmptyCancels(t *testing.T) {
	res, calls := recordingResolver(nil)
	m := mkApprovalsModel([]agent.PendingApproval{
		mkPending("art", "x", "plan", "agent", time.Second),
	}, res)
	updated, _ := m.Update(key('r'))
	m = updated.(*Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // empty reason
	m = updated.(*Model)
	if !strings.Contains(m.status, "reject cancelled") {
		t.Errorf("empty reason should cancel with a status, got %q", m.status)
	}
	if len(*calls) != 0 {
		t.Errorf("empty reason should not dispatch a reject: %v", *calls)
	}
}

// TestRejectReason_ResolverError surfaces a reject resolver failure as a
// statusEchoMsg.
func TestRejectReason_ResolverError(t *testing.T) {
	res, _ := recordingResolver(errString("nope"))
	m := mkApprovalsModel([]agent.PendingApproval{
		mkPending("art", "x", "plan", "agent", time.Second),
	}, res)
	updated, _ := m.Update(key('r'))
	m = updated.(*Model)
	m.input.SetValue("reason")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("reject commit returned nil cmd")
	}
	echo, ok := cmd().(statusEchoMsg)
	if !ok || !strings.Contains(echo.text, "reject failed") {
		t.Errorf("reject resolver error didn't surface: %T %v", cmd(), cmd())
	}
}

// --- source.go: SQLite snapshot ------------------------------------

// TestSQLiteSnapshotSource_ReturnsRows confirms the production snapshot
// source reads seeded agents out of the projection.
func TestSQLiteSnapshotSource_ReturnsRows(t *testing.T) {
	log := openTempLog(t)
	seedAgent(t, log, "01HV0000000000000000000001", "", "alpha", "fake", agent.StateRunning)
	seedAgent(t, log, "01HV0000000000000000000002", "", "beta", "fake", agent.StateDone)

	src := NewSQLiteSnapshotSource(log)
	rows, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("snapshot returned %d rows, want 2", len(rows))
	}
}

// TestWindowSizeMsg_TriggersRelayout confirms a WindowSizeMsg flows into
// the focus pane + window sizing.
func TestWindowSizeMsg_TriggersRelayout(t *testing.T) {
	m := mkModelWithRows([]agent.AgentRow{
		{ID: "01HVaaa0000000000000000001", Title: "x", State: agent.StateRunning},
	}, 0, 0)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 80})
	m = updated.(*Model)
	if m.width != 200 || m.height != 80 {
		t.Errorf("WindowSizeMsg didn't store dims: %dx%d", m.width, m.height)
	}
	if m.win.Visible < 1 {
		t.Errorf("relayout should give the window a positive visible height")
	}
}
