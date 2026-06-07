package manage

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// VerbDispatcher is the slice the TUI binds to for steer / interrupt /
// stop. Mirrors the agent.Supervisor methods 1:1, so production wires
// `*agent.Supervisor` directly. Tests pass a recording double.
//
// Per SPEC § Verbs the three verbs must NOT be aliased - each has a
// distinct effect (steer = inject message, interrupt = abort turn,
// stop = terminate). The interface intentionally enumerates them as
// three methods rather than a single Dispatch(verb string, ...) so the
// type system enforces the discipline.
type VerbDispatcher interface {
	Steer(id, message string) error
	Interrupt(id string) error
	Stop(id string) error
}

// ModeReporter is an optional interface implementations of
// VerbDispatcher can satisfy to surface the active orchestrator mode +
// effective spawn cap in the manage header. *agent.Supervisor
// implements this naturally; tests may opt in by embedding a tiny
// stub. When the wired dispatcher does NOT implement ModeReporter the
// header skips the "mode=... (cap N)" line.
type ModeReporter interface {
	Mode() string
	SpawnCap() int
}

// noopDispatcher is the zero-value dispatcher used before a real one
// is wired. Returns a not-wired error for each call so the status bar
// can surface the failure exactly like a not-implemented error from a
// real Supervisor.
type noopDispatcher struct{}

func (noopDispatcher) Steer(id, message string) error { return fmt.Errorf("no supervisor wired") }
func (noopDispatcher) Interrupt(id string) error      { return fmt.Errorf("no supervisor wired") }
func (noopDispatcher) Stop(id string) error           { return fmt.Errorf("no supervisor wired") }

// VerbResult flows back through the bubbletea message loop after a
// verb fires. The status bar renders the resulting line; on error,
// the error.Error() is displayed verbatim so a "not implemented"
// stub surfaces clearly while the real backing impl lands in
// parallel main work.
type VerbResult struct {
	Verb    string // "steer" | "interrupt" | "stop"
	AgentID string
	Err     error
}

// String renders the result as the status-bar line. Successful verbs
// say "steered <id>"; errors carry the verb and the message so the
// user can grep the log.
func (r VerbResult) String() string {
	id := shortID(r.AgentID)
	if r.Err != nil {
		return fmt.Sprintf("%s %s: %v", r.Verb, id, r.Err)
	}
	switch r.Verb {
	case "steer":
		return "steered " + id
	case "interrupt":
		return "interrupting " + id
	case "stop":
		return "stopping " + id + " - graceful drain → hard-kill after 30s"
	}
	return r.Verb + " " + id + " ok"
}

// dispatchSteer fires the steer verb in a tea.Cmd so the model's
// Update loop stays sync. Producer of VerbResult messages.
func dispatchSteer(sup VerbDispatcher, id, message string) tea.Cmd {
	return func() tea.Msg {
		err := sup.Steer(id, message)
		return VerbResult{Verb: "steer", AgentID: id, Err: err}
	}
}

func dispatchInterrupt(sup VerbDispatcher, id string) tea.Cmd {
	return func() tea.Msg {
		err := sup.Interrupt(id)
		return VerbResult{Verb: "interrupt", AgentID: id, Err: err}
	}
}

func dispatchStop(sup VerbDispatcher, id string) tea.Cmd {
	return func() tea.Msg {
		err := sup.Stop(id)
		return VerbResult{Verb: "stop", AgentID: id, Err: err}
	}
}

// overlayKind identifies which transient overlay is active. The
// orchestrator owns at most one overlay at a time: the steer text
// input, the interrupt confirm, or the stop confirm.
type overlayKind int

const (
	overlayNone overlayKind = iota
	overlaySteer
	overlayInterruptConfirm
	overlayStopConfirm
	overlayFilter
	// overlayRejectReason is the text-input overlay for capturing the
	// user's rejection reason on a pending approval (Slice 4h pane).
	overlayRejectReason
)

// overlayPromptLabel returns the human-readable prompt for the given
// overlay. Empty string when overlay is None.
func overlayPromptLabel(o overlayKind, intent string) string {
	switch o {
	case overlaySteer:
		return "steer: "
	case overlayInterruptConfirm:
		return fmt.Sprintf("interrupt %q? [y/N] ", intent)
	case overlayStopConfirm:
		return fmt.Sprintf("stop %q? [y/N]  (graceful drain → hard-kill after 30s) ", intent)
	case overlayFilter:
		return "filter: "
	case overlayRejectReason:
		return "reject reason: "
	}
	return ""
}

// statusTimeout is how long the verb-result echo stays visible
// before the next refresh tick clears it.
const statusTimeout = 4 * time.Second
