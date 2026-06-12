package chat

// Tests for the Ctrl+P command palette (roadmap slice 9k): ranking
// rules, open/close/dispatch flows, args-vs-bare commit handling, MRU
// emission + load, highlight rendering, narrow widths, and the
// open-while-streaming interaction.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/tui/slash"
)

// --- ranking ---------------------------------------------------------

func TestPaletteResults_EmptyQueryMRUFirstThenAlpha(t *testing.T) {
	// "frame" repeated + an unknown verb: dedupe keeps the first
	// occurrence and drops verbs that aren't in Builtins.
	items := paletteResults("", []string{"frame", "help", "frame", "bogus"})
	if len(items) != len(slash.Builtins) {
		t.Fatalf("empty query should list every builtin once; got %d want %d",
			len(items), len(slash.Builtins))
	}
	if items[0].spec.Name != "frame" || !items[0].recent {
		t.Errorf("items[0] should be recent /frame; got %+v", items[0])
	}
	if items[1].spec.Name != "help" || !items[1].recent {
		t.Errorf("items[1] should be recent /help; got %+v", items[1])
	}
	// The remainder is alphabetical and not marked recent.
	rest := items[2:]
	for i := 1; i < len(rest); i++ {
		if rest[i-1].spec.Name > rest[i].spec.Name {
			t.Errorf("non-MRU tail not alphabetical: %q > %q",
				rest[i-1].spec.Name, rest[i].spec.Name)
		}
		if rest[i].recent {
			t.Errorf("tail entry %q should not be marked recent", rest[i].spec.Name)
		}
	}
	// No duplicates anywhere.
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.spec.Name] {
			t.Errorf("duplicate entry %q", it.spec.Name)
		}
		seen[it.spec.Name] = true
	}
}

func TestPaletteResults_EmptyQueryNoMRUIsAlphabetical(t *testing.T) {
	items := paletteResults("", nil)
	if len(items) != len(slash.Builtins) {
		t.Fatalf("got %d items want %d", len(items), len(slash.Builtins))
	}
	for i := 1; i < len(items); i++ {
		if items[i-1].spec.Name > items[i].spec.Name {
			t.Errorf("not alphabetical: %q > %q", items[i-1].spec.Name, items[i].spec.Name)
		}
	}
}

func TestPaletteResults_NameOutranksDescription(t *testing.T) {
	// "frame" matches /frame by name; several other verbs only via
	// their descriptions ("the active frame", "frame's mode", ...).
	items := paletteResults("frame", nil)
	if len(items) < 2 {
		t.Fatalf("expected name + description matches; got %d", len(items))
	}
	if items[0].spec.Name != "frame" {
		t.Errorf("items[0] should be /frame; got %q", items[0].spec.Name)
	}
	if len(items[0].namePos) == 0 {
		t.Error("/frame should carry name highlight positions")
	}
	// Every name match must sort before every description-only match.
	seenDescOnly := false
	for _, it := range items {
		if len(it.descPos) > 0 {
			seenDescOnly = true
		}
		if len(it.namePos) > 0 && seenDescOnly {
			t.Errorf("name match %q ranked below a description-only match", it.spec.Name)
		}
	}
	if !seenDescOnly {
		t.Error("expected at least one description-only match for 'frame'")
	}
}

func TestPaletteResults_MRUBonusBreaksNearTies(t *testing.T) {
	// "tru" prefix-matches both /trust and /trusts; /trust wins on raw
	// fuzzy score (shorter trailing gap). A recent /trusts flips it.
	base := paletteResults("tru", nil)
	if len(base) < 2 || base[0].spec.Name != "trust" {
		t.Fatalf("without MRU, /trust should rank first; got %+v", base)
	}
	boosted := paletteResults("tru", []string{"trusts"})
	if boosted[0].spec.Name != "trusts" {
		t.Errorf("recent /trusts should outrank /trust on a near-tie; got %q",
			boosted[0].spec.Name)
	}
	if !boosted[0].recent {
		t.Error("boosted row should be marked recent")
	}
}

func TestPaletteResults_MRUBonusDoesNotOverpowerClearMatch(t *testing.T) {
	// An exact name match must stay on top no matter how recent some
	// unrelated verb is.
	items := paletteResults("clear", []string{"jobs", "frame", "help"})
	if items[0].spec.Name != "clear" {
		t.Errorf("exact match /clear should rank first; got %q", items[0].spec.Name)
	}
}

func TestPaletteResults_NoMatches(t *testing.T) {
	if items := paletteResults("zzzzqq", nil); len(items) != 0 {
		t.Errorf("nonsense query should match nothing; got %d items", len(items))
	}
}

func TestDedupeMRU(t *testing.T) {
	got := dedupeMRU([]string{"Frame", "frame", "  help ", "nonexistent", "", "help"})
	want := []string{"frame", "help"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestPaletteNeedsArgs(t *testing.T) {
	cases := []struct {
		verb string
		want bool
	}{
		{"memory", true},   // <query> - required
		{"research", true}, // <question> - required
		{"shell", true},    // <cmd> - required
		{"model", false},   // [provider:model] - optional, lists bare
		{"clear", false},   // no args at all
		{"frame", false},   // [list|switch <name>|new [name]] - optional
	}
	for _, c := range cases {
		spec, ok := slash.Lookup(c.verb)
		if !ok {
			t.Fatalf("builtin %q missing", c.verb)
		}
		if got := paletteNeedsArgs(spec); got != c.want {
			t.Errorf("paletteNeedsArgs(%q) = %v, want %v (hint %q)",
				c.verb, got, c.want, spec.ArgsHint)
		}
	}
}

// --- open / close / key routing --------------------------------------

func TestOpenCommandPalette_ClosesSuggestBands(t *testing.T) {
	m := newTestModel(t)
	m.slashSuggest.open = true
	m.mentionSuggest.open = true
	m.openCommandPalette()
	if !m.showPalette {
		t.Fatal("openCommandPalette should flip showPalette")
	}
	if m.slashSuggest.open || m.mentionSuggest.open {
		t.Error("opening the palette must close the composer suggest bands")
	}
	if len(m.paletteItems) != len(slash.Builtins) {
		t.Errorf("fresh palette lists all builtins; got %d", len(m.paletteItems))
	}
}

func TestCtrlP_OpensAndTogglesClosed(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(*Model)
	if !m.showPalette {
		t.Fatal("ctrl+p should open the palette")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(*Model)
	if m.showPalette {
		t.Error("second ctrl+p should close the palette")
	}
}

func TestCtrlP_ReadOnlyNoOp(t *testing.T) {
	m := newTestModel(t)
	m.readOnly = true
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(*Model)
	if m.showPalette {
		t.Error("read-only chats have no command surface; ctrl+p must not open")
	}
}

func TestPaletteKey_EscCloses(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	_, _, handled := m.handlePaletteKey(key("esc"))
	if !handled {
		t.Error("esc should be handled")
	}
	if m.showPalette {
		t.Error("esc should close the palette")
	}
	if m.paletteItems != nil || m.paletteQuery != "" {
		t.Error("close should reset query + items")
	}
}

func TestPaletteKey_CtrlCFallsThrough(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	_, _, handled := m.handlePaletteKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if handled {
		t.Error("ctrl+c must fall through so the quit path stays reachable")
	}
}

func TestPaletteKey_TypingFiltersAndBackspace(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	for _, r := range "fra" {
		m.handlePaletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.paletteQuery != "fra" {
		t.Fatalf("query should accumulate; got %q", m.paletteQuery)
	}
	if len(m.paletteItems) == 0 || m.paletteItems[0].spec.Name != "frame" {
		t.Errorf("'fra' should rank /frame first; got %+v", m.paletteItems)
	}
	m.handlePaletteKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.paletteQuery != "fr" {
		t.Errorf("backspace should trim one rune; got %q", m.paletteQuery)
	}
	// Backspace on an empty query is a safe no-op.
	m.paletteQuery = ""
	m.handlePaletteKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.paletteQuery != "" {
		t.Errorf("backspace on empty query should no-op; got %q", m.paletteQuery)
	}
}

func TestPaletteKey_SpaceExtendsQuery(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	m.handlePaletteKey(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if m.paletteQuery != " " {
		t.Errorf("space should extend the query; got %q", m.paletteQuery)
	}
}

func TestPaletteKey_NavigationWraps(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	n := len(m.paletteItems)
	m.handlePaletteKey(key("up"))
	if m.paletteCursor != n-1 {
		t.Errorf("up from 0 should wrap to %d; got %d", n-1, m.paletteCursor)
	}
	m.handlePaletteKey(key("down"))
	if m.paletteCursor != 0 {
		t.Errorf("down past end should wrap to 0; got %d", m.paletteCursor)
	}
}

func TestPaletteKey_SwallowsOtherKeys(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	_, _, handled := m.handlePaletteKey(tea.KeyMsg{Type: tea.KeyTab})
	if !handled {
		t.Error("the palette should swallow unbound keys while open")
	}
	if !m.showPalette {
		t.Error("an unbound key must not close the palette")
	}
}

// --- commit: bare dispatch vs args prefill ----------------------------

func TestPaletteCommit_BareVerbDispatchesThroughSlashPath(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	for _, r := range "help" {
		m.handlePaletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.paletteItems[0].spec.Name != "help" {
		t.Fatalf("'help' should rank /help first; got %q", m.paletteItems[0].spec.Name)
	}
	_, cmd, _ := m.handlePaletteKey(key("enter"))
	if m.showPalette {
		t.Error("enter should close the palette")
	}
	if !m.showHelp {
		t.Error("/help dispatched via the palette should open the help overlay")
	}
	if cmd != nil {
		t.Errorf("/help dispatch returns nil cmd; got %v", cmd)
	}
}

func TestPaletteCommit_RequiredArgsPrefillsComposer(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	for _, r := range "memory" {
		m.handlePaletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.paletteItems[0].spec.Name != "memory" {
		t.Fatalf("'memory' should rank /memory first; got %q", m.paletteItems[0].spec.Name)
	}
	_, cmd, _ := m.handlePaletteKey(key("enter"))
	if m.showPalette {
		t.Error("enter should close the palette")
	}
	if cmd != nil {
		t.Error("required-args verbs must NOT dispatch bare")
	}
	if got := m.ta.Value(); got != "/memory " {
		t.Errorf("composer should be prefilled %q; got %q", "/memory ", got)
	}
	if !m.slashSuggest.open {
		t.Error("the slash suggest band should engage on the prefilled verb")
	}
}

func TestPaletteCommit_EmptyListCloses(t *testing.T) {
	m := newTestModel(t)
	m.showPalette = true
	m.paletteItems = nil
	_, cmd, _ := m.handlePaletteKey(key("enter"))
	if m.showPalette {
		t.Error("enter with no matches should close the palette")
	}
	if cmd != nil {
		t.Error("nothing should dispatch")
	}
}

// --- MRU emission + load ----------------------------------------------

func TestDispatchSlash_RecordsCommandUsed(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HVPALETTEMRUTESTSESSION001"
	seedAgent(t, log, agentID, "palette", "fake")
	m := New(log, agentID, NewMemTextSource())

	_ = m.dispatchSlash(slash.Command{Name: "help"})
	_ = m.dispatchSlash(slash.Command{Name: "bogus"}) // unknown: not recorded
	_ = m.dispatchSlash(slash.Command{Name: "q"})     // alias: recorded as quit

	got, err := log.RecentCommandsUsed(context.Background(), 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	want := []string{"quit", "help"} // newest first
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestPaletteLaunch_EmitsCommandUsedThroughSameChokePoint(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HVPALETTEMRUTESTSESSION002"
	seedAgent(t, log, agentID, "palette", "fake")
	m := New(log, agentID, NewMemTextSource())

	m.openCommandPalette()
	for _, r := range "whoami" {
		m.handlePaletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, _, _ = m.handlePaletteKey(key("enter"))

	got, err := log.RecentCommandsUsed(context.Background(), 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 || got[0] != "whoami" {
		t.Errorf("palette launch should record exactly one verb; got %v", got)
	}
}

func TestOpenPalette_LoadsMRUFromLog(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HVPALETTEMRUTESTSESSION003"
	seedAgent(t, log, agentID, "palette", "fake")
	m := New(log, agentID, NewMemTextSource())

	// Use two verbs, then reopen: the MRU should order them newest
	// first even though they were recorded by "this" session (the
	// query is cross-agent, so a fresh ULID would see them too).
	_ = m.dispatchSlash(slash.Command{Name: "jobs"})
	_ = m.dispatchSlash(slash.Command{Name: "whoami"})

	m.openCommandPalette()
	if len(m.paletteItems) < 2 {
		t.Fatal("expected the full listing")
	}
	if m.paletteItems[0].spec.Name != "whoami" || !m.paletteItems[0].recent {
		t.Errorf("most recent verb should lead; got %+v", m.paletteItems[0])
	}
	if m.paletteItems[1].spec.Name != "jobs" || !m.paletteItems[1].recent {
		t.Errorf("second-most-recent verb should follow; got %+v", m.paletteItems[1])
	}
}

func TestLoadPaletteMRU_QueryErrorDegradesToDiag(t *testing.T) {
	log := openTempLog(t)
	_ = log.Close() // force the by-type query to fail
	var diag bytes.Buffer
	m := &Model{log: log, diag: &diag}
	if got := m.loadPaletteMRU(); got != nil {
		t.Errorf("query failure should yield no MRU; got %v", got)
	}
	if !strings.Contains(diag.String(), "palette mru load") {
		t.Errorf("query failure should land in diag; got %q", diag.String())
	}
}

func TestPaletteDefaultOrder_FiltersRawInput(t *testing.T) {
	// Called with un-deduped input (defensive): duplicates and unknown
	// verbs must still be filtered.
	items := paletteDefaultOrder([]string{"frame", "frame", "bogus"})
	if len(items) != len(slash.Builtins) {
		t.Fatalf("got %d items want %d", len(items), len(slash.Builtins))
	}
	if items[0].spec.Name != "frame" || items[1].spec.Name == "frame" {
		t.Errorf("frame should appear exactly once, first; got %q then %q",
			items[0].spec.Name, items[1].spec.Name)
	}
}

func TestLoadPaletteMRU_NonSQLiteLogDegrades(t *testing.T) {
	m := &Model{log: &fakeNonSQLiteLog{}}
	if got := m.loadPaletteMRU(); got != nil {
		t.Errorf("non-SQLite log should yield no MRU; got %v", got)
	}
}

func TestRecordCommandUsed_NilLogSafe(t *testing.T) {
	m := &Model{}
	m.recordCommandUsed("help") // must not panic
}

// failingAppendLog errors on Append so the diag path is exercised.
type failingAppendLog struct{ fakeNonSQLiteLog }

func (failingAppendLog) Append(_ context.Context, _ agent.Event) (int64, error) {
	return 0, errors.New("disk full")
}

func TestRecordCommandUsed_AppendErrorGoesToDiag(t *testing.T) {
	var diag bytes.Buffer
	m := &Model{log: failingAppendLog{}, diag: &diag}
	m.recordCommandUsed("help")
	if !strings.Contains(diag.String(), "record command used") {
		t.Errorf("append failure should land in diag; got %q", diag.String())
	}
	// And with no diag writer wired, the failure is swallowed silently
	// (io.Discard fallback) - must not panic.
	bare := &Model{log: failingAppendLog{}}
	bare.recordCommandUsed("help")
}

// --- rendering --------------------------------------------------------

func TestRenderPaletteOverlay_Basics(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	out := renderPaletteOverlay(m, 80, 24)
	for _, want := range []string{"commands", "⌕", "▸", "┄", "pick", "run", "close"} {
		if !strings.Contains(out, want) {
			t.Errorf("overlay missing %q:\n%s", want, out)
		}
	}
	// The cursor row is the first listed command.
	if !strings.Contains(out, "/"+m.paletteItems[0].spec.Name) {
		t.Errorf("overlay should list the top-ranked verb")
	}
	// Empty-query hint present.
	if !strings.Contains(out, "recent first") {
		t.Error("empty-query hint missing")
	}
}

func TestRenderPaletteOverlay_RecentTagInEmptyQueryMode(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	m.paletteItems = paletteResults("", []string{"frame"})
	out := renderPaletteOverlay(m, 80, 24)
	if !strings.Contains(out, "recent") {
		t.Errorf("MRU rows should carry the dim italic recent tag:\n%s", out)
	}
}

func TestRenderPaletteOverlay_OverflowTag(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()                 // ~28 builtins
	out := renderPaletteOverlay(m, 80, 10) // 5 visible rows max
	if !strings.Contains(out, "more ↓") {
		t.Errorf("clipped list should show the overflow corner tag:\n%s", out)
	}
}

func TestRenderPaletteOverlay_ScrollFollowsCursor(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	last := len(m.paletteItems) - 1
	m.paletteCursor = last
	out := renderPaletteOverlay(m, 80, 10)
	if !strings.Contains(out, "/"+m.paletteItems[last].spec.Name) {
		t.Errorf("window should scroll so the cursor row is visible:\n%s", out)
	}
}

func TestRenderPaletteOverlay_NarrowWidth(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	for _, w := range []int{20, 30, 40} {
		out := renderPaletteOverlay(m, w, 8)
		if out == "" {
			t.Errorf("width %d: empty render", w)
		}
		if !strings.Contains(out, "/") {
			t.Errorf("width %d: no command rows survived", w)
		}
	}
}

func TestRenderPaletteOverlay_NoMatches(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	m.paletteQuery = "zzzzqq"
	m.paletteItems = paletteResults(m.paletteQuery, nil)
	out := renderPaletteOverlay(m, 80, 20)
	if !strings.Contains(out, "no matching commands") {
		t.Errorf("empty result should render the placeholder row:\n%s", out)
	}
}

// TestRenderPaletteOverlay_NoLeftStripe pins the house design rule:
// no `border-left`-style colored stripe, which in lipgloss terms means
// no Border() call producing │ runs at column 0. The palette is built
// from dashed rules only; assert no box-drawing vertical bars at all.
func TestRenderPaletteOverlay_NoLeftStripe(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	out := renderPaletteOverlay(m, 80, 24)
	for _, banned := range []string{"│", "┃", "▌ /"} {
		if strings.Contains(out, banned) {
			t.Errorf("palette must not paint a left stripe (%q found):\n%s", banned, out)
		}
	}
}

// TestRenderPaletteOverlay_NoColorSafe strips every SGR escape and
// asserts the load-bearing signals survive as plain text: the corner
// tag, the ▸ selection marker, and the command names.
func TestRenderPaletteOverlay_NoColorSafe(t *testing.T) {
	m := newTestModel(t)
	m.openCommandPalette()
	out := stripSGR(renderPaletteOverlay(m, 80, 24))
	for _, want := range []string{"commands", "▸", "/" + m.paletteItems[0].spec.Name} {
		if !strings.Contains(out, want) {
			t.Errorf("NO_COLOR output missing %q:\n%s", want, out)
		}
	}
}

// stripSGR removes ANSI SGR sequences (ESC [ ... m) so tests can assert
// on the text a NO_COLOR terminal would show.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestHighlightRunes(t *testing.T) {
	// In the test environment lipgloss renders with the Ascii profile,
	// so styles are no-ops and the content contract is what we pin:
	// positions are RUNE indices, multibyte text must round-trip, and
	// out-of-range positions are ignored.
	base := lipgloss.NewStyle()
	hi := lipgloss.NewStyle().Bold(true).Underline(true)
	cases := []struct {
		name string
		s    string
		pos  []int
	}{
		{"no positions", "frame", nil},
		{"ascii", "frame", []int{0, 1}},
		{"multibyte", "héllo wörld", []int{1, 8}},
		{"consecutive run", "research", []int{2, 3, 4}},
		{"out of range ignored", "fg", []int{-1, 99}},
		{"all positions", "fg", []int{0, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stripSGR(highlightRunes(c.s, c.pos, base, hi))
			if got != c.s {
				t.Errorf("highlight must preserve text: got %q want %q", got, c.s)
			}
		})
	}
}

// --- view + streaming interplay ---------------------------------------

func TestPalette_RendersInTakeoverSlot(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HVPALETTEVIEWTESTSESSION01"
	seedAgent(t, log, agentID, "palette", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 100, 30)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(*Model)
	view := m.View()
	if !strings.Contains(view, "commands") {
		t.Errorf("View should paint the palette corner tag:\n%s", view)
	}
}

func TestPalette_OpenWhileStreamLiveStaysOpen(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HVPALETTESTREAMTESTSESSI01"
	seedAgent(t, log, agentID, "palette", "fake")
	src := NewMemTextSource()
	m := New(log, agentID, src)
	m = drive(t, m, 100, 30)

	// Live stream in flight: the assistant is mid-turn.
	src.Append(agentID, "streaming tokens...")
	if !m.assistantBusy() {
		t.Fatal("test setup: assistant should read as busy")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(*Model)
	if !m.showPalette {
		t.Fatal("ctrl+p should open mid-stream")
	}

	// An event arriving mid-palette (the stream sealing into an
	// assistant message) must not close or corrupt the overlay.
	payload, _ := json.Marshal(agent.MessagePayload{Text: "done"})
	updated, _ = m.Update(eventMsg{ev: agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtAssistantMessage,
		Payload: payload,
	}})
	m = updated.(*Model)
	if !m.showPalette {
		t.Error("a streamed event must not close the palette")
	}
	if !strings.Contains(m.View(), "commands") {
		t.Error("palette should still render after the event")
	}
}

// TestPalette_CommandUsedEventIsTransparentToTranscript proves the
// round-trip: the EvtCommandUsed row recorded by dispatchSlash flows
// back through the subscription pump into applyEvent without painting
// a projection-error note (the projection treats it as passive).
func TestPalette_CommandUsedEventIsTransparentToTranscript(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HVPALETTEPASSIVETESTSESS01"
	seedAgent(t, log, agentID, "palette", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 100, 30)

	payload, _ := json.Marshal(agent.CommandUsedPayload{Command: "help"})
	updated, _ := m.Update(eventMsg{ev: agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtCommandUsed,
		Payload: payload,
	}})
	m = updated.(*Model)
	for _, e := range m.transcript {
		if e.kind == entrySystemNote && strings.Contains(e.text, "projection error") {
			t.Errorf("command_used must be projection-passive; got note %q", e.text)
		}
	}
}
