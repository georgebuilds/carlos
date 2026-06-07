package chat

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
)

// eventMsg wraps a single agent.Event arriving from the EventLog
// Subscribe channel. Each one passes through Update and is folded into
// the transcript + projection.
type eventMsg struct {
	ev agent.Event
}

// backfillMsg carries the initial replay batch — the events present in
// the log when the model was constructed. We send these as one message
// so the transcript is populated in a single render cycle on first paint.
type backfillMsg struct {
	events []agent.Event
}

// errMsg is non-fatal: we surface it inline as a system-note transcript
// entry. The model keeps running so a transient subscribe blip doesn't
// kill the chat.
type errMsg struct {
	err error
}

// statusMsg is a transient line shown in the footer above the keybind
// row. Used by slash dispatch to echo a verb / error / pending-handler
// notice. Cleared on the next keystroke.
type statusMsg struct {
	text string
	kind statusKind
}

// textTickMsg fires on a low-frequency timer so the chat view re-reads
// the TextSource and re-renders the live assistant text. The focused
// agent's pane targets 60 Hz reads from the buffer; we run at a more
// conservative 30 Hz (33ms) for the slice so the
// notes file's reader doesn't blink. Slice 1f can dial this in.
//
// The ticker is started in Init and self-rescheduled by the handler.
type textTickMsg struct{}

const textTickInterval = 33 * time.Millisecond

func scheduleTextTick() tea.Cmd {
	return tea.Tick(textTickInterval, func(time.Time) tea.Msg { return textTickMsg{} })
}

// subscriptionReady carries the live event channel produced by
// EventLog.Subscribe. Init returns a Cmd that opens the subscription
// and emits this once; thereafter we loop a pump goroutine that pushes
// eventMsgs.
type subscriptionReady struct {
	ch     <-chan agent.Event
	cancel func()
}

// pumpEventCmd reads one event from the channel and returns it as an
// eventMsg. Re-scheduled by Update after each event so we keep pumping.
// If the channel closes (unsub or log close), we emit nil and stop.
func pumpEventCmd(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg{ev: ev}
	}
}

// subscribeCmd opens the subscription against the given log + agent and
// returns subscriptionReady. We do this as a Cmd (not in New) so that
// failure surfaces as a tea.Msg rather than a constructor error — the
// chat view should boot even if the log is briefly unreachable.
func subscribeCmd(log agent.EventLog, agentID string) tea.Cmd {
	return func() tea.Msg {
		ch, cancel, err := log.Subscribe(agentID)
		if err != nil {
			return errMsg{err: err}
		}
		return subscriptionReady{ch: ch, cancel: cancel}
	}
}

// childrenTickMsg fires while the chat is polling the ChildrenView for
// live sub-agent state. The handler refreshes m.childrenSnap and re-
// arms the tick when the snapshot is non-empty; an empty snapshot
// stops the loop so a long idle chat doesn't keep ticking.
type childrenTickMsg struct{}

func scheduleChildrenTick() tea.Cmd {
	return tea.Tick(panelTickInterval, func(time.Time) tea.Msg { return childrenTickMsg{} })
}

// approvalRequestMsg carries one in-flight tool-call approval request
// from the TUIApprover's pump goroutine into the Model's Update. The
// Model parks the request on m.pendingApproval and renders an overlay
// until the user resolves it.
type approvalRequestMsg struct {
	req *ApprovalRequest
}

// approvalPumpCmd reads one ApprovalRequest off the channel and
// returns it as an approvalRequestMsg. Re-scheduled by Update after
// each request lands, so the pump keeps draining.
func approvalPumpCmd(reqCh <-chan *ApprovalRequest) tea.Cmd {
	return func() tea.Msg {
		req, ok := <-reqCh
		if !ok {
			return nil
		}
		return approvalRequestMsg{req: req}
	}
}

// backfillCmd replays the existing log for agentID into a single
// backfillMsg. We do this on Init so the transcript renders in one shot
// rather than scrolling-in event by event.
func backfillCmd(log agent.EventLog, agentID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		evs, err := log.Read(ctx, agentID, 0)
		if err != nil {
			return errMsg{err: err}
		}
		return backfillMsg{events: evs}
	}
}
