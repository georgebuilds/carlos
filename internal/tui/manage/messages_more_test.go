package manage

import (
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestScheduleClearStatus_ReturnsTickThatEmitsClearMsg invokes the
// scheduler and asserts the underlying tea.Cmd ultimately produces a
// clearStatusMsg. tea.Tick wraps a time.AfterFunc so we just invoke
// the cmd in a goroutine + receive within the timeout window.
func TestScheduleClearStatus_ReturnsTickThatEmitsClearMsg(t *testing.T) {
	cmd := scheduleClearStatus()
	if cmd == nil {
		t.Fatal("scheduleClearStatus returned nil cmd")
	}

	done := make(chan any, 1)
	go func() { done <- cmd() }()

	select {
	case msg := <-done:
		if _, ok := msg.(clearStatusMsg); !ok {
			t.Errorf("clear-status tick produced %T, want clearStatusMsg", msg)
		}
	case <-time.After(2 * statusTimeout):
		t.Fatalf("clear-status tick did not fire within %v", 2*statusTimeout)
	}
}

// TestPumpFocusEventCmd_DeliversBufferedEvent confirms the focus pump
// reads one event off a buffered channel and wraps it in a focusEventMsg.
func TestPumpFocusEventCmd_DeliversBufferedEvent(t *testing.T) {
	ch := make(chan agent.Event, 1)
	want := agent.Event{
		AgentID: "01HVabc12345678",
		Type:    agent.EvtHeartbeat,
		TS:      time.Now().UTC(),
	}
	ch <- want

	msg := pumpFocusEventCmd(ch)()
	wrap, ok := msg.(focusEventMsg)
	if !ok {
		t.Fatalf("pumpFocusEventCmd msg = %T, want focusEventMsg", msg)
	}
	if wrap.ev.AgentID != want.AgentID || wrap.ev.Type != want.Type {
		t.Errorf("pump event = %+v, want %+v", wrap.ev, want)
	}
}

// TestPumpFocusEventCmd_ClosedChannelReturnsNil is the unsubscribe path:
// a closed channel signals "no more events" and the pump emits nil so
// the bubbletea loop unwinds rather than spinning forever.
func TestPumpFocusEventCmd_ClosedChannelReturnsNil(t *testing.T) {
	ch := make(chan agent.Event)
	close(ch)
	if msg := pumpFocusEventCmd(ch)(); msg != nil {
		t.Errorf("pump on closed channel = %T, want nil", msg)
	}
}

// TestScheduleRefreshTick_FiresRefreshTickMsg covers the refresh tick
// scheduler. tea.Tick can deliver the msg either via the function the
// tick wraps or via the timer; either way the cmd-as-function path
// must eventually produce refreshTickMsg.
func TestScheduleRefreshTick_FiresRefreshTickMsg(t *testing.T) {
	cmd := scheduleRefreshTick()
	if cmd == nil {
		t.Fatal("scheduleRefreshTick returned nil cmd")
	}
	done := make(chan any, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if _, ok := msg.(refreshTickMsg); !ok {
			t.Errorf("refresh tick produced %T, want refreshTickMsg", msg)
		}
	case <-time.After(2 * refreshInterval):
		t.Fatalf("refresh tick did not fire within %v", 2*refreshInterval)
	}
}

// TestScheduleSparkAdvance_FiresSparklineTickMsg is the same shape as
// the refresh-tick test but for the per-second sparkline advance.
func TestScheduleSparkAdvance_FiresSparklineTickMsg(t *testing.T) {
	cmd := scheduleSparkAdvance()
	if cmd == nil {
		t.Fatal("scheduleSparkAdvance returned nil cmd")
	}
	done := make(chan any, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if _, ok := msg.(sparklineTickMsg); !ok {
			t.Errorf("spark tick produced %T, want sparklineTickMsg", msg)
		}
	case <-time.After(2 * sparkAdvanceInterval):
		t.Fatalf("spark tick did not fire within %v", 2*sparkAdvanceInterval)
	}
}
