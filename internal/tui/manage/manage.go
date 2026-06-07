package manage

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
)

// Model is the top-level bubbletea Model for the manage TUI. It owns:
//
//   - the snapshot source (read-model over the agents projection),
//   - the focus pane (per-agent transcript + token ring),
//   - the verb dispatcher (steer / interrupt / stop),
//   - the active sort + filter,
//   - the visible window + scroll position.
//
// The model NEVER owns supervisor state. The roster is
// re-snapshotted from the projection every 250ms; the focus pane
// reads from EventLog.Subscribe + Replay. Verb keystrokes call into
// Supervisor; the result lands in the status bar.
type Model struct {
	src    SnapshotSource
	log    agent.EventLog
	sup    VerbDispatcher
	focus  *FocusPane
	filter Filter

	// rosterRows is the most-recent snapshot, post-sort+filter,
	// flattened with lineage indentation. Rebuilt on every refresh
	// AND on sort/filter changes.
	rosterRows []rosterRow

	// rawRows is the snapshot pre-sort+filter. Kept so sort/filter
	// changes don't need an immediate snapshot re-read.
	rawRows []agent.AgentRow

	// cursor is the index into rosterRows the user has selected.
	cursor int

	// win is the virtualization window over rosterRows.
	win Window

	// sort + sort direction.
	sortKey SortKey
	sortAsc bool

	// overlay state (steer / confirm / filter / reject-reason).
	overlay overlayKind
	input   textinput.Model

	// status echo (verb result, errors). Cleared after statusTimeout.
	status string

	// width/height of the terminal.
	width, height int

	// focusSubCancel + focusSubCh are the active Subscribe handle for
	// the currently-focused agent. Re-bound on every focus change.
	focusSubCh     <-chan agent.Event
	focusSubCancel func()

	// quitting set on ctrl-c so View can short-circuit.
	quitting bool

	// rosterRefreshErr surfaces the last snapshot error in the status
	// line if non-empty.
	rosterRefreshErr string

	// Slice 4h approval pane: viewMode swaps the body between the
	// roster+focus split and the approval-queue list. Lister/resolver
	// are wired via WithApprovals; when nil, the `A` key surfaces a
	// "not wired" line in the status bar and stays in roster view.
	view      viewMode
	approvals approvalsPane
	lister    approvalLister
	resolver  approvalResolver
}

// New constructs a manage Model bound to the given snapshot source,
// event log (for Subscribe + Replay on the focused agent), and verb
// dispatcher. The supervisor argument may be nil; we install a
// not-wired stub so the TUI still boots and the verbs surface a
// clear "no supervisor wired" line in the status bar.
func New(src SnapshotSource, log agent.EventLog, sup VerbDispatcher) *Model {
	if sup == nil {
		sup = noopDispatcher{}
	}
	ti := textinput.New()
	ti.Placeholder = ""
	ti.CharLimit = 0
	ti.Prompt = ""
	return &Model{
		src:     src,
		log:     log,
		sup:     sup,
		focus:   NewFocusPane(),
		sortKey: SortPriority,
		sortAsc: true,
		input:   ti,
	}
}

// Run is the convenience entry point for callers that just want to
// drop into the manage surface. Mirrors chat.Model.Run.
func (m *Model) Run() (tea.Model, error) {
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	return p.Run()
}

// WithApprovals wires the Phase 4h approval-queue pane. lister is
// what populates the list (typically a closure over
// agent.ListPendingApprovals + the user's state.db log); resolver is
// called when the user accepts / rejects. Both may be nil to indicate
// "approvals not wired" - the TUI still renders, the `A` key surfaces
// a status-bar line instead of panicking.
//
// Returns the Model for chaining so callers can keep New(...) terse:
//
//	mgr := manage.New(src, log, sup).WithApprovals(lister, resolver)
func (m *Model) WithApprovals(lister approvalLister, resolver approvalResolver) *Model {
	m.lister = lister
	m.resolver = resolver
	return m
}

// Init kicks off the first snapshot fetch + schedules the refresh and
// sparkline-advance tickers.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		snapshotCmd(m.src),
		scheduleRefreshTick(),
		scheduleSparkAdvance(),
	)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.relayout()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case fetchApprovalsMsg:
		m.approvals.applyFetch(msg)
		return m, nil

	case statusEchoMsg:
		m.status = msg.text
		return m, nil

	case acceptedOrRejectedMsg:
		// Resolution landed; refresh the pending list.
		return m, fetchApprovalsCmd(m.lister)

	case refreshTickMsg:
		return m, tea.Batch(snapshotCmd(m.src), scheduleRefreshTick())

	case snapshotReadyMsg:
		if msg.err != nil {
			m.rosterRefreshErr = msg.err.Error()
		} else {
			m.rosterRefreshErr = ""
			m.rawRows = msg.rows
			m.rebuildRoster()
		}
		return m, nil

	case sparklineTickMsg:
		if r := m.focus.Ring(); r != nil {
			r.Advance()
		}
		return m, scheduleSparkAdvance()

	case focusBackfillMsg:
		if msg.agentID == m.focus.AgentID() {
			m.focus.ApplyBackfill(msg.events)
		}
		return m, nil

	case focusSubscribedMsg:
		// Only adopt the subscription if it still matches the focused
		// agent (the user may have moved on in the meantime).
		if msg.agentID != m.focus.AgentID() {
			if msg.cancel != nil {
				msg.cancel()
			}
			return m, nil
		}
		if m.focusSubCancel != nil {
			m.focusSubCancel()
		}
		m.focusSubCh = msg.ch
		m.focusSubCancel = msg.cancel
		if msg.ch != nil {
			return m, pumpFocusEventCmd(msg.ch)
		}
		return m, nil

	case focusEventMsg:
		// Only apply events for the currently-focused agent; events
		// from the previous focus get discarded if the user switched.
		if msg.ev.AgentID == m.focus.AgentID() {
			m.focus.ApplyEvent(msg.ev)
		}
		return m, m.repumpFocus()

	case clearStatusMsg:
		m.status = ""
		return m, nil

	case VerbResult:
		m.status = msg.String()
		return m, scheduleClearStatus()
	}
	return m, nil
}

// repumpFocus re-arms the focus-pane event pump after each delivered
// event. nil when no subscription is active so the loop unwinds
// cleanly on focus change.
func (m *Model) repumpFocus() tea.Cmd {
	if m.focusSubCh == nil {
		return nil
	}
	return pumpFocusEventCmd(m.focusSubCh)
}

// handleKey routes keystrokes. The dispatch is layered:
//  1. Overlay (steer / confirm / filter) consumes everything until
//     ESC or enter.
//  2. Navigation (j/k, ↑/↓, enter, pgup/pgdn, home/end).
//  3. Verb keys (s/i/x) - open the corresponding overlay.
//  4. Sort keys (1–5; Shift+key reverses).
//  5. Filter key (/).
//  6. ctrl-c → quit.
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Overlay-active branch first.
	if m.overlay != overlayNone {
		return m.handleOverlayKey(msg)
	}

	// Approval-view branch: most keys behave differently here. ctrl+c
	// and `A` still work (quit + toggle back); everything else is
	// pane-local navigation + accept/reject.
	if m.view == viewApprovals {
		return m.handleApprovalsKey(msg)
	}

	switch msg.String() {
	case "ctrl+c", "q":
		m.quitting = true
		m.teardownFocus()
		return m, tea.Quit
	case "A":
		// Toggle into the approval queue view. The body swaps; the
		// header + footer stay (they re-render with mode=approvals).
		if m.lister == nil {
			m.status = "approvals not wired (cmd/carlos didn't pass a lister)"
			return m, nil
		}
		m.view = viewApprovals
		m.approvals.cursor = 0
		return m, fetchApprovalsCmd(m.lister)

	case "up", "k":
		m.moveCursor(-1)
		return m, nil
	case "down", "j":
		m.moveCursor(1)
		return m, nil
	case "pgup":
		m.moveCursor(-m.win.Visible)
		return m, nil
	case "pgdown":
		m.moveCursor(m.win.Visible)
		return m, nil
	case "home", "g":
		m.cursor = 0
		m.win = m.win.ScrollTo(0)
		return m, nil
	case "end", "G":
		m.cursor = len(m.rosterRows) - 1
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.win = m.win.ScrollTo(m.cursor)
		return m, nil

	case "enter":
		return m, m.focusSelected()

	case "s":
		if id := m.selectedID(); id != "" {
			m.openOverlay(overlaySteer)
		}
		return m, nil
	case "i":
		if id := m.selectedID(); id != "" {
			m.openOverlay(overlayInterruptConfirm)
		}
		return m, nil
	case "x":
		if id := m.selectedID(); id != "" {
			m.openOverlay(overlayStopConfirm)
		}
		return m, nil

	case "/":
		m.openOverlay(overlayFilter)
		return m, nil

	case "esc":
		// ESC outside an overlay clears the active filter.
		if m.filter.Active() {
			m.filter.Query = ""
			m.rebuildRoster()
		}
		return m, nil

	case "1":
		m.sortKey = SortID
		m.sortAsc = true
		m.rebuildRoster()
		return m, nil
	case "2":
		m.sortKey = SortState
		m.sortAsc = true
		m.rebuildRoster()
		return m, nil
	case "3":
		m.sortKey = SortCost
		m.sortAsc = true
		m.rebuildRoster()
		return m, nil
	case "4":
		m.sortKey = SortTokens
		m.sortAsc = true
		m.rebuildRoster()
		return m, nil
	case "5":
		m.sortKey = SortTime
		m.sortAsc = true
		m.rebuildRoster()
		return m, nil
	case "!":
		m.sortKey = SortID
		m.sortAsc = false
		m.rebuildRoster()
		return m, nil
	case "@":
		m.sortKey = SortState
		m.sortAsc = false
		m.rebuildRoster()
		return m, nil
	case "#":
		m.sortKey = SortCost
		m.sortAsc = false
		m.rebuildRoster()
		return m, nil
	case "$":
		m.sortKey = SortTokens
		m.sortAsc = false
		m.rebuildRoster()
		return m, nil
	case "%":
		m.sortKey = SortTime
		m.sortAsc = false
		m.rebuildRoster()
		return m, nil
	case "0":
		m.sortKey = SortPriority
		m.sortAsc = true
		m.rebuildRoster()
		return m, nil
	}
	return m, nil
}

// handleApprovalsKey routes keystrokes when the approval queue pane
// is the active view. Most roster keys (sort, filter, verbs) are
// disabled here - the approval pane has its own narrower vocabulary
// because it's a focused review surface.
func (m *Model) handleApprovalsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.quitting = true
		m.teardownFocus()
		return m, tea.Quit
	case "A", "esc":
		// Back to roster view.
		m.view = viewRoster
		return m, nil
	case "up", "k":
		m.approvals.moveCursor(-1)
		return m, nil
	case "down", "j":
		m.approvals.moveCursor(1)
		return m, nil
	case "pgup":
		m.approvals.moveCursor(-5)
		return m, nil
	case "pgdown":
		m.approvals.moveCursor(5)
		return m, nil
	case "home", "g":
		m.approvals.cursor = 0
		return m, nil
	case "end", "G":
		m.approvals.cursor = len(m.approvals.pending) - 1
		if m.approvals.cursor < 0 {
			m.approvals.cursor = 0
		}
		return m, nil
	case "y":
		return m, m.acceptSelectedApproval()
	case "r", "n":
		// Open the reason input overlay; commit triggers reject.
		sel, ok := m.approvals.selected()
		if !ok {
			return m, nil
		}
		_ = sel // selection captured implicitly via cursor at commit time
		m.openOverlay(overlayRejectReason)
		return m, nil
	case "R":
		// Capital R = refresh the queue without leaving the pane.
		return m, fetchApprovalsCmd(m.lister)
	}
	return m, nil
}

// acceptSelectedApproval invokes the resolver on the highlighted row
// with no note. Returns a tea.Cmd that refreshes the queue after the
// resolver returns (the resolution event drops the row from the
// derived queue, so the next list call shows it gone).
func (m *Model) acceptSelectedApproval() tea.Cmd {
	sel, ok := m.approvals.selected()
	if !ok || m.resolver == nil {
		return nil
	}
	id := sel.Ref.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := m.resolver(ctx, id, "", true)
		if err != nil {
			return statusEchoMsg{text: "accept failed: " + err.Error()}
		}
		// Trigger a fetch by returning a fetch result via the same
		// mechanism the initial enter used. We can't run two cmds in
		// one msg-return; the caller's loop will pick up the next
		// refresh tick. Returning a marker the Update can swap in.
		// Simpler: return a statusEchoMsg + let the next refresh tick
		// repopulate. (Refresh tick scheduled in Init runs every
		// snapshotRefreshInterval seconds; the approval pane piggy-
		// backs on it via the manage Model's repumpFocus chain, but
		// we don't have a per-pane tick - instead we issue a fetch
		// inline.)
		return acceptedOrRejectedMsg{}
	}
}

// statusEchoMsg lets non-overlay paths drop a one-shot string into
// the status bar. The approval pane uses it for resolver failures.
type statusEchoMsg struct{ text string }

// acceptedOrRejectedMsg signals "a resolution just landed; refetch
// the pending list". The Update handler issues a fetchApprovalsCmd.
type acceptedOrRejectedMsg struct{}

// handleOverlayKey routes keystrokes when an overlay is active.
func (m *Model) handleOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.closeOverlay()
		return m, nil
	case "enter":
		return m.commitOverlay()
	}

	// Confirm prompts (y/N) intercept y / n directly.
	if m.overlay == overlayInterruptConfirm || m.overlay == overlayStopConfirm {
		switch strings.ToLower(msg.String()) {
		case "y":
			return m.commitConfirm(true)
		case "n":
			return m.commitConfirm(false)
		}
		return m, nil
	}

	// Text-input overlays delegate to the textinput component.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// Live-filter: rebuild roster as the user types.
	if m.overlay == overlayFilter {
		m.filter.Query = m.input.Value()
		m.rebuildRoster()
	}
	return m, cmd
}

// openOverlay configures + focuses the textinput for an overlay kind.
func (m *Model) openOverlay(o overlayKind) {
	m.overlay = o
	m.input.Reset()
	m.input.Focus()
	switch o {
	case overlaySteer:
		m.input.Placeholder = "what to nudge the agent toward - enter to send, esc to cancel"
	case overlayFilter:
		m.input.Placeholder = "fuzzy: intent | state | id substring"
		m.input.SetValue(m.filter.Query)
		m.input.SetCursor(len(m.filter.Query))
	case overlayRejectReason:
		m.input.Placeholder = "why are you rejecting this? - enter to confirm reject, esc to cancel"
	}
}

// closeOverlay shuts the overlay and clears the input.
func (m *Model) closeOverlay() {
	m.overlay = overlayNone
	m.input.Blur()
	m.input.Reset()
}

// commitOverlay runs the verb behind the active overlay.
func (m *Model) commitOverlay() (tea.Model, tea.Cmd) {
	id := m.selectedID()
	switch m.overlay {
	case overlaySteer:
		text := strings.TrimSpace(m.input.Value())
		m.closeOverlay()
		if text == "" || id == "" {
			return m, nil
		}
		return m, dispatchSteer(m.sup, id, text)
	case overlayInterruptConfirm:
		// Enter (no explicit y/n) defaults to "no" - confirm prompts
		// must require the typed "y" so a stray Return doesn't kill an
		// agent.
		m.closeOverlay()
		return m, nil
	case overlayStopConfirm:
		m.closeOverlay()
		return m, nil
	case overlayFilter:
		// Filter is "live" - enter just commits the current value and
		// closes the overlay; the filter persists.
		m.overlay = overlayNone
		m.input.Blur()
		return m, nil
	case overlayRejectReason:
		reason := strings.TrimSpace(m.input.Value())
		m.closeOverlay()
		if reason == "" {
			m.status = "reject cancelled (reason required)"
			return m, nil
		}
		sel, ok := m.approvals.selected()
		if !ok || m.resolver == nil {
			return m, nil
		}
		id := sel.Ref.ID
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := m.resolver(ctx, id, reason, false); err != nil {
				return statusEchoMsg{text: "reject failed: " + err.Error()}
			}
			return acceptedOrRejectedMsg{}
		}
	}
	return m, nil
}

// commitConfirm runs the verb behind a yes/no confirm prompt.
func (m *Model) commitConfirm(yes bool) (tea.Model, tea.Cmd) {
	id := m.selectedID()
	overlay := m.overlay
	m.closeOverlay()
	if !yes || id == "" {
		return m, nil
	}
	switch overlay {
	case overlayInterruptConfirm:
		return m, dispatchInterrupt(m.sup, id)
	case overlayStopConfirm:
		return m, dispatchStop(m.sup, id)
	}
	return m, nil
}

// moveCursor shifts the selection by delta, clamping to the roster
// bounds, and slides the virtualization window to keep the selection
// on-screen.
func (m *Model) moveCursor(delta int) {
	if len(m.rosterRows) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rosterRows) {
		m.cursor = len(m.rosterRows) - 1
	}
	m.win.Total = len(m.rosterRows)
	m.win = m.win.ScrollTo(m.cursor)
}

// selectedID returns the agent ID under the cursor, or "" if the
// roster is empty.
func (m *Model) selectedID() string {
	if m.cursor < 0 || m.cursor >= len(m.rosterRows) {
		return ""
	}
	return m.rosterRows[m.cursor].row.ID
}

// focusSelected binds the focus pane to the row under the cursor and
// kicks off backfill + subscribe.
func (m *Model) focusSelected() tea.Cmd {
	id := m.selectedID()
	if id == "" || id == m.focus.AgentID() {
		return nil
	}
	m.teardownFocus()
	m.focus.Bind(id)
	if m.log == nil {
		return nil
	}
	return tea.Batch(
		focusBackfillCmd(m.log, id),
		focusSubscribeCmd(m.log, id),
	)
}

// teardownFocus unsubscribes the current focus channel and zeros the
// handles. Idempotent.
func (m *Model) teardownFocus() {
	if m.focusSubCancel != nil {
		m.focusSubCancel()
	}
	m.focusSubCancel = nil
	m.focusSubCh = nil
}

// rebuildRoster regenerates rosterRows from rawRows by running the
// active filter + sort + lineage flatten. Called on snapshot, sort
// change, and filter change.
func (m *Model) rebuildRoster() {
	rows := m.filter.Apply(m.rawRows)
	rows = SortBy(rows, m.sortKey, m.sortAsc)
	flat := buildRosterRows(rows, m.focus.AgentID(), defaultMaxDepth)
	m.rosterRows = flat

	// Clamp the cursor + scroll to the new row count.
	if len(m.rosterRows) == 0 {
		m.cursor = 0
	} else if m.cursor >= len(m.rosterRows) {
		m.cursor = len(m.rosterRows) - 1
	}
	m.win.Total = len(m.rosterRows)
	m.win = m.win.Clamp()
}

// relayout re-sizes the focus pane on terminal-size change. The view
// layer recomputes column widths each frame so we only need to bump
// the viewport dimensions here.
func (m *Model) relayout() {
	if m.width < minTermWidth || m.height < minTermHeight {
		return
	}
	rosterW := rosterPaneWidth(m.width)
	focusW := m.width - rosterW - 3 // -3: divider + 2x padding
	if focusW < 20 {
		focusW = 20
	}
	innerH := m.height - 4 // header + footer + border
	if innerH < 4 {
		innerH = 4
	}
	m.focus.Resize(focusW, innerH-2) // -2 leaves room for the focus header
	m.win.Visible = innerH - 2       // roster's body height (minus header row)
	m.win = m.win.Clamp()
}

// rosterPaneWidth picks the left-pane width. The brief says "~40% width
// or fixed 50 cols"; we pick the larger so wide terminals get more
// space and narrow terminals retain a usable minimum.
func rosterPaneWidth(termW int) int {
	if termW <= 0 {
		return 50
	}
	w := termW * 40 / 100
	if w < 50 {
		w = 50
	}
	if w > termW-30 {
		w = termW - 30
	}
	return w
}

// Quitting reports whether the model has begun shutdown (for
// tea.Quit + clean teardown in tests).
func (m *Model) Quitting() bool { return m.quitting }

// SetClockForTests exists so tests can pin a known "now" for elapsed
// time formatting. The default lives in the View path; this is a
// minimal seam so the View() snapshot is reproducible.
var nowFunc = func() time.Time { return time.Now() }
