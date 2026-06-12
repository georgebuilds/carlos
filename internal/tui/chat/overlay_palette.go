// overlay_palette.go - the Ctrl+P command palette (roadmap slice 9k).
//
// Ctrl+P opens a fuzzy palette over the slash-command vocabulary plus
// an MRU of recently used verbs. A query row sits on top, a ranked
// list below: command name, dim description, fuzzy-match positions
// highlighted. Enter runs the selection through the SAME path as a
// typed slash command (dispatchSlash); Esc or a second Ctrl+P closes.
//
// Args handling: slash.Builtins encodes required args as "<...>" and
// optional args as "[...]" in ArgsHint. Required-args verbs (e.g.
// /memory <query>) can't run bare, so Enter prefills "/verb " into the
// composer and hands the keyboard back for argument entry - the slash
// suggest band engages on the prefilled value exactly as if the user
// had typed it. Optional-args and no-args verbs dispatch immediately.
//
// MRU semantics: every recognized verb that dispatchSlash routes -
// typed or palette-launched - lands one EvtCommandUsed row in the
// event log (recordCommandUsed below is called from the single choke
// point at the top of dispatchSlash). Session agent IDs are fresh
// ULIDs, so the palette loads its MRU cross-agent via
// SQLiteEventLog.RecentCommandsUsed on every open.
//
// Ranking rules (paletteResults):
//
//   - Empty query: deduped MRU verbs first (newest first), then every
//     remaining builtin alphabetically.
//   - Non-empty query: fuzzy.Rank-style scoring over name AND
//     description. A name match gets paletteNameOffset added so ANY
//     name match outranks EVERY description-only match; ties inside
//     each band resolve by fuzzy score. MRU verbs get a modest
//     recency bonus (paletteMRUBonus per recency step) - enough to
//     break near-ties between similar matches, far below a single
//     fuzzy boundary bonus so a clearly better textual match wins.
//
// Design rules (sandlot sketchbook, per George): dashed ┄ rules and a
// dim italic corner tag, NEVER a left color stripe. NO_COLOR-safe:
// the selection marker is the ▸ glyph (not a color), the recent tag
// and corner tag are plain text, and stripping every SGR code leaves
// the full list readable.

package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/fuzzy"
	"github.com/georgebuilds/carlos/internal/tui/slash"
)

const (
	// paletteMRUWindow caps how many command_used rows the palette
	// loads on open. 50 raw rows dedupe down to at most the full
	// Builtins set while still giving repeated verbs a deep enough
	// recency signal.
	paletteMRUWindow = 50

	// paletteNameOffset lifts every name match above every
	// description-only match. 1<<16 dominates any achievable fuzzy
	// score on these short candidates (an exact name match peaks
	// around bonusExact=10000 plus small boundary terms).
	paletteNameOffset = 1 << 16

	// paletteMRUBonus is the per-recency-step bump for verbs in the
	// MRU: the most recent verb gets len(mru)*bonus, the oldest gets
	// 1*bonus. 24 breaks near-ties (gap penalties are 1-2 per rune)
	// without overpowering a real boundary bonus (120+).
	paletteMRUBonus = 24
)

// paletteItem is one ranked row of the palette list.
type paletteItem struct {
	spec  slash.Spec
	score int
	// namePos / descPos are RUNE indices into spec.Name resp.
	// spec.Description (fuzzy.Match contract) for highlight. At most
	// one of the two is set: a name match wins the row outright and
	// the description renders unhighlighted.
	namePos []int
	descPos []int
	// recent marks MRU-sourced rows; the empty-query render tags them.
	recent bool
}

// openCommandPalette flips the takeover open. Loads the cross-session
// MRU from the event log (best-effort - a non-SQLite log or a query
// error just means no recency data) and closes the composer's
// transient suggest bands so the palette is the only completion
// surface on screen. The peek card is suppressed render-side while
// the palette is up (renderInput checks showPalette).
func (m *Model) openCommandPalette() {
	m.slashSuggest.reset()
	m.mentionSuggest.reset()
	m.paletteMRU = m.loadPaletteMRU()
	m.paletteQuery = ""
	m.paletteCursor = 0
	m.paletteItems = paletteResults("", m.paletteMRU)
	m.showPalette = true
	m.rerenderViewport()
}

// closeCommandPalette returns to the underlying chat. Mirrors
// closeResumePicker's shape so the overlay state machine reads as
// uniform. Idempotent.
func (m *Model) closeCommandPalette() {
	m.showPalette = false
	m.paletteQuery = ""
	m.paletteCursor = 0
	m.paletteItems = nil
	m.paletteMRU = nil
	m.rerenderViewport()
}

// loadPaletteMRU reads the recent-verbs list off the event log. Only
// the SQLite log supports the by-type query; the dev-aid MemEventLog
// path degrades to "no recency data" rather than failing the open.
func (m *Model) loadPaletteMRU() []string {
	log, ok := m.log.(*agent.SQLiteEventLog)
	if !ok || log == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmds, err := log.RecentCommandsUsed(ctx, paletteMRUWindow)
	if err != nil {
		w := m.diag
		if w == nil {
			w = io.Discard
		}
		fmt.Fprintf(w, "carlos: palette mru load: %v\n", err)
		return nil
	}
	return cmds
}

// recordCommandUsed appends one EvtCommandUsed row for verb. Called
// from the top of dispatchSlash - the single choke point both the
// typed path and the palette path flow through - so the MRU sees
// every execution exactly once. Unrecognized verbs never reach the
// log (the unknown-command echo is not an execution). Best-effort: an
// append error degrades to a diag line, never a user-visible failure;
// the command itself already routed.
func (m *Model) recordCommandUsed(verb string) {
	if verb == "q" {
		// /q is the undocumented exit alias; normalize so the MRU
		// counts one verb instead of fragmenting across spellings.
		verb = "quit"
	}
	if _, ok := slash.Lookup(verb); !ok {
		return
	}
	if m.log == nil {
		return
	}
	payload, err := json.Marshal(agent.CommandUsedPayload{Command: verb})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := m.log.Append(ctx, agent.Event{
		AgentID: m.agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtCommandUsed,
		Payload: payload,
	}); err != nil {
		w := m.diag
		if w == nil {
			w = io.Discard
		}
		fmt.Fprintf(w, "carlos: record command used: %v\n", err)
	}
}

// handlePaletteKey is the palette's key router. Returns (model, cmd,
// handled); handled=false only for ctrl+c so the quit path stays
// reachable, mirroring the other overlays.
func (m *Model) handlePaletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c":
		return m, nil, false
	case "esc", "ctrl+p":
		m.closeCommandPalette()
		return m, nil, true
	case "up":
		if n := len(m.paletteItems); n > 0 {
			m.paletteCursor--
			if m.paletteCursor < 0 {
				m.paletteCursor = n - 1
			}
			m.rerenderViewport()
		}
		return m, nil, true
	case "down":
		if n := len(m.paletteItems); n > 0 {
			m.paletteCursor++
			if m.paletteCursor >= n {
				m.paletteCursor = 0
			}
			m.rerenderViewport()
		}
		return m, nil, true
	case "enter":
		return m.paletteCommit()
	case "backspace":
		if m.paletteQuery != "" {
			r := []rune(m.paletteQuery)
			m.paletteQuery = string(r[:len(r)-1])
			m.refreshPaletteItems()
		}
		return m, nil, true
	}
	// Printable input extends the query. Space participates so
	// description matching can cross word boundaries.
	switch msg.Type {
	case tea.KeyRunes:
		m.paletteQuery += string(msg.Runes)
		m.refreshPaletteItems()
	case tea.KeySpace:
		m.paletteQuery += " "
		m.refreshPaletteItems()
	}
	// Everything else is swallowed while the palette owns the keyboard.
	return m, nil, true
}

// refreshPaletteItems re-ranks after a query edit and snaps the cursor
// to the top so the best match is always the Enter default.
func (m *Model) refreshPaletteItems() {
	m.paletteItems = paletteResults(m.paletteQuery, m.paletteMRU)
	m.paletteCursor = 0
	m.rerenderViewport()
}

// paletteCommit runs the focused row. Required-args verbs pivot to the
// composer ("/verb " prefilled, suggest band engaged); everything else
// dispatches bare through the same dispatchSlash the typed path uses.
func (m *Model) paletteCommit() (tea.Model, tea.Cmd, bool) {
	if m.paletteCursor < 0 || m.paletteCursor >= len(m.paletteItems) {
		m.closeCommandPalette()
		return m, nil, true
	}
	spec := m.paletteItems[m.paletteCursor].spec
	m.closeCommandPalette()
	if paletteNeedsArgs(spec) {
		// Prefill replaces the composer value - the same trade a
		// VSCode-style palette makes. The verb can't run without an
		// argument, so dispatching bare would only echo a usage line.
		m.ta.SetValue("/" + spec.Name + " ")
		m.ta.CursorEnd()
		m.refreshSuggests()
		m.rerenderViewport()
		return m, nil, true
	}
	return m, m.dispatchSlash(slash.Command{Name: spec.Name}), true
}

// paletteNeedsArgs reports whether the spec's args are REQUIRED. House
// convention in slash.Builtins: "<...>" marks a required arg (e.g.
// /memory <query>), "[...]" an optional one (e.g. /model
// [provider:model] usefully runs bare and lists options).
func paletteNeedsArgs(s slash.Spec) bool {
	return strings.HasPrefix(s.ArgsHint, "<")
}

// paletteResults is the pure ranking core (tested directly). See the
// file header for the full rule set.
func paletteResults(query string, mru []string) []paletteItem {
	recent := dedupeMRU(mru)
	if strings.TrimSpace(query) == "" {
		return paletteDefaultOrder(recent)
	}
	rank := make(map[string]int, len(recent))
	for i, v := range recent {
		rank[v] = i
	}
	out := make([]paletteItem, 0, len(slash.Builtins))
	for _, s := range slash.Builtins {
		item := paletteItem{spec: s}
		if score, pos, ok := fuzzy.Match(query, s.Name); ok {
			item.score = score + paletteNameOffset
			item.namePos = pos
		} else if score, pos, ok := fuzzy.Match(query, s.Description); ok {
			item.score = score
			item.descPos = pos
		} else {
			continue
		}
		if i, hit := rank[s.Name]; hit {
			item.recent = true
			item.score += (len(recent) - i) * paletteMRUBonus
		}
		out = append(out, item)
	}
	// Stable: equal scores keep Builtins order, matching fuzzy.Rank's
	// contract and the help panel's curated reading order.
	sort.SliceStable(out, func(a, b int) bool { return out[a].score > out[b].score })
	return out
}

// paletteDefaultOrder is the empty-query listing: deduped MRU verbs
// first (newest first), then every remaining builtin alphabetically.
func paletteDefaultOrder(recent []string) []paletteItem {
	seen := make(map[string]bool, len(recent))
	out := make([]paletteItem, 0, len(slash.Builtins))
	for _, v := range recent {
		spec, ok := slash.Lookup(v)
		if !ok || seen[spec.Name] {
			continue
		}
		seen[spec.Name] = true
		out = append(out, paletteItem{spec: spec, recent: true})
	}
	rest := make([]paletteItem, 0, len(slash.Builtins))
	for _, s := range slash.Builtins {
		if !seen[s.Name] {
			rest = append(rest, paletteItem{spec: s})
		}
	}
	sort.SliceStable(rest, func(a, b int) bool { return rest[a].spec.Name < rest[b].spec.Name })
	return append(out, rest...)
}

// dedupeMRU normalizes the raw newest-first verb stream from the log:
// lower-cased, first occurrence wins, verbs that no longer exist in
// Builtins are dropped (an old log may reference retired commands).
func dedupeMRU(mru []string) []string {
	seen := make(map[string]bool, len(mru))
	out := make([]string, 0, len(mru))
	for _, v := range mru {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" || seen[v] {
			continue
		}
		if _, ok := slash.Lookup(v); !ok {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// renderPaletteOverlay paints the palette into the takeover slot:
//
//	┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄ commands
//	⌕ fra▌
//	┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄
//	▸ /frame         show or switch the active frame
//	  /fg            foreground a background shell job
//	┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄ 3 more ↓
//	↑↓ pick  ·  enter run  ·  esc close
//
// Sandlot language throughout: dashed rules, dim italic corner tags,
// no bordered panel, no left stripe. Selection reads from the ▸ glyph
// so it survives NO_COLOR.
func renderPaletteOverlay(m *Model, innerW, innerH int) string {
	const indent = "  "
	contentW := innerW - len(indent)
	if contentW < 24 {
		contentW = 24
	}

	dim := lipgloss.NewStyle().Foreground(colorMuted)
	tagStyle := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	accent := lipgloss.NewStyle().Foreground(colorAccent)
	marker := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	// Top rule + corner tag (peek.go precedent).
	tag := "commands"
	ruleW := contentW - lipgloss.Width(tag) - 1
	if ruleW < 1 {
		ruleW = 1
	}
	top := dim.Render(strings.Repeat("┄", ruleW)) + " " + tagStyle.Render(tag)

	// Query row: search glyph + live query + block cursor. The hint
	// only shows while the query is empty so it never crowds typing.
	query := truncateRight(m.paletteQuery, contentW-6)
	queryRow := accent.Render("⌕ ") + query + accent.Render("▌")
	if m.paletteQuery == "" {
		queryRow += " " + tagStyle.Render("type to filter · recent first")
	}

	midRule := dim.Render(strings.Repeat("┄", contentW))

	// Visible window: 5 fixed rows (top, query, mid rule, bottom rule,
	// footer) leave the rest for list rows.
	maxVisible := innerH - 5
	if maxVisible < 3 {
		maxVisible = 3
	}
	start := 0
	if m.paletteCursor >= maxVisible {
		start = m.paletteCursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(m.paletteItems) {
		end = len(m.paletteItems)
	}

	// Name column width: widest visible "/verb" so descriptions align.
	nameW := 0
	for _, it := range m.paletteItems {
		if w := lipgloss.Width("/" + it.spec.Name); w > nameW {
			nameW = w
		}
	}

	rows := make([]string, 0, maxVisible+5)
	rows = append(rows, indent+top, indent+queryRow, indent+midRule)
	if len(m.paletteItems) == 0 {
		rows = append(rows, indent+"  "+tagStyle.Render("no matching commands"))
	}
	emptyQuery := strings.TrimSpace(m.paletteQuery) == ""
	for i := start; i < end; i++ {
		rows = append(rows, indent+renderPaletteRow(
			m.paletteItems[i], i == m.paletteCursor, emptyQuery, nameW, contentW,
			marker, dim, tagStyle, accent,
		))
	}

	// Bottom rule carries an overflow tag when rows are clipped.
	bottom := dim.Render(strings.Repeat("┄", contentW))
	if hidden := len(m.paletteItems) - end; hidden > 0 {
		overflow := fmt.Sprintf("%d more ↓", hidden)
		bw := contentW - lipgloss.Width(overflow) - 1
		if bw < 1 {
			bw = 1
		}
		bottom = dim.Render(strings.Repeat("┄", bw)) + " " + tagStyle.Render(overflow)
	}
	rows = append(rows, indent+bottom, indent+renderPaletteFooter())
	return strings.Join(rows, "\n")
}

// renderPaletteRow paints one list row: cursor marker, name column
// (fuzzy positions highlighted), dim description, optional dim italic
// "recent" tag in empty-query mode.
func renderPaletteRow(
	it paletteItem,
	selected, emptyQuery bool,
	nameW, contentW int,
	marker, dim, tagStyle, accent lipgloss.Style,
) string {
	hi := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Underline(true)

	mark := "  "
	if selected {
		mark = marker.Render("▸") + " "
	}

	name := "/" + it.spec.Name
	nameStyle := accent
	if !selected {
		nameStyle = lipgloss.NewStyle().Foreground(colorAgent)
	}
	// namePos indices are into spec.Name; the rendered string has a
	// leading "/" so shift by one rune.
	var shifted []int
	for _, p := range it.namePos {
		shifted = append(shifted, p+1)
	}
	nameR := highlightRunes(name, shifted, nameStyle, hi)
	pad := nameW - lipgloss.Width(name) + 2
	if pad < 1 {
		pad = 1
	}

	// Description budget: marker(2) + name column + gap; recent tag
	// reserves its own width so it never clips off the row.
	recentTag := ""
	recentW := 0
	if emptyQuery && it.recent {
		recentTag = " " + tagStyle.Render("recent")
		recentW = 7
	}
	descW := contentW - 2 - nameW - pad - recentW
	if descW < 4 {
		descW = 4
	}
	desc := truncateRight(it.spec.Description, descW)
	descR := highlightRunes(desc, it.descPos, dim, hi)

	return mark + nameR + strings.Repeat(" ", pad) + descR + recentTag
}

func renderPaletteFooter() string {
	return footerKey("↑↓") + footerLabel(" pick") +
		footerSep() + footerKey("enter") + footerLabel(" run") +
		footerSep() + footerKey("esc") + footerLabel(" close")
}

// highlightRunes paints the runes of s at the given RUNE indices with
// hi and everything else with base, grouping consecutive same-style
// runs so the output stays compact. Positions outside [0,len) are
// ignored (defensive - truncateRight may have clipped the candidate
// shorter than the match). NO_COLOR-safe by construction: styles
// carry zero text, so stripping SGR codes yields s verbatim.
func highlightRunes(s string, positions []int, base, hi lipgloss.Style) string {
	if len(positions) == 0 {
		return base.Render(s)
	}
	r := []rune(s)
	set := make(map[int]bool, len(positions))
	for _, p := range positions {
		if p >= 0 && p < len(r) {
			set[p] = true
		}
	}
	if len(set) == 0 {
		return base.Render(s)
	}
	var b strings.Builder
	for i := 0; i < len(r); {
		j := i
		for j < len(r) && set[j] == set[i] {
			j++
		}
		if set[i] {
			b.WriteString(hi.Render(string(r[i:j])))
		} else {
			b.WriteString(base.Render(string(r[i:j])))
		}
		i = j
	}
	return b.String()
}
