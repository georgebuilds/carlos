package manage

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
)

// refreshTickMsg fires every refreshInterval to re-snapshot the
// agents projection. Cheap query (≤ a few dozen rows) — see source.go
// for the SQL.
type refreshTickMsg struct{}

// sparklineTickMsg advances the focused agent's token ring by one
// slot. Fires once per second so the 60-slot ring spans one minute.
type sparklineTickMsg struct{}

// snapshotReadyMsg carries a fresh roster snapshot. Emitted by the
// snapshot loader after each refreshTick.
type snapshotReadyMsg struct {
	rows []agent.AgentRow
	err  error
}

// focusEventMsg wraps an event arriving from the focused agent's
// EventLog.Subscribe channel. Each one updates the focus pane's
// transcript and (if it's a token_usage event) the burn-rate ring.
type focusEventMsg struct {
	ev agent.Event
}

// focusBackfillMsg carries the initial event-history backfill for a
// newly-focused agent. Emitted by the backfill loader after each
// focus change.
type focusBackfillMsg struct {
	agentID string
	events  []agent.Event
	err     error
}

// focusSubscribedMsg carries the live channel handle for the focused
// agent. The orchestrator stashes the channel and the cancel func and
// starts pumping events from it.
type focusSubscribedMsg struct {
	agentID string
	ch      <-chan agent.Event
	cancel  func()
}

// clearStatusMsg is fired after statusTimeout to wipe a transient
// status echo (verb result, filter-cleared notice, etc.).
type clearStatusMsg struct{}

// refreshInterval is the cadence at which the roster re-reads the
// projection. Design target is 250–500ms; we pick the lower end so
// state transitions surface quickly without hammering SQLite.
const refreshInterval = 250 * time.Millisecond

// sparkAdvanceInterval is the per-second advance for the burn-rate
// ring. 60 slots × 1s = 1 minute window.
const sparkAdvanceInterval = 1 * time.Second

// scheduleRefreshTick re-arms the projection refresh tick. Init
// starts the first one; the handler self-reschedules.
func scheduleRefreshTick() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return refreshTickMsg{} })
}

// scheduleSparkAdvance re-arms the per-second ring advance.
func scheduleSparkAdvance() tea.Cmd {
	return tea.Tick(sparkAdvanceInterval, func(time.Time) tea.Msg { return sparklineTickMsg{} })
}

// scheduleClearStatus is a one-shot timer so the verb-result echo
// fades after statusTimeout.
func scheduleClearStatus() tea.Cmd {
	return tea.Tick(statusTimeout, func(time.Time) tea.Msg { return clearStatusMsg{} })
}

// snapshotCmd runs the SnapshotSource once and returns the result as
// a snapshotReadyMsg. Errors are non-fatal — the model keeps the
// previous snapshot and surfaces the error inline via the status bar.
func snapshotCmd(src SnapshotSource) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		rows, err := src.Snapshot(ctx)
		return snapshotReadyMsg{rows: rows, err: err}
	}
}

// focusBackfillCmd Reads the full event history for agentID and
// returns it as a focusBackfillMsg so the focus pane can re-render
// from a known starting point on focus change.
func focusBackfillCmd(log agent.EventLog, agentID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		evs, err := log.Read(ctx, agentID, 0)
		return focusBackfillMsg{agentID: agentID, events: evs, err: err}
	}
}

// focusSubscribeCmd opens a Subscribe channel for the focused agent.
// Failure surfaces as focusSubscribedMsg with nil channel so the
// caller knows to skip the pump.
func focusSubscribeCmd(log agent.EventLog, agentID string) tea.Cmd {
	return func() tea.Msg {
		ch, cancel, err := log.Subscribe(agentID)
		if err != nil {
			return focusSubscribedMsg{agentID: agentID}
		}
		return focusSubscribedMsg{agentID: agentID, ch: ch, cancel: cancel}
	}
}

// pumpFocusEventCmd reads one event from the live channel and emits
// it as a focusEventMsg. The handler self-reschedules so events keep
// flowing. Returns nil when the channel is closed (unsubscribed).
func pumpFocusEventCmd(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return focusEventMsg{ev: ev}
	}
}
