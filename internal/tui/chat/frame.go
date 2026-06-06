package chat

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// frameSlash dispatches /frame. With no args it echoes the current
// frame; with `list` it echoes the available frames; with `switch
// <name>` it persists the new active frame (calling the wired
// SwitchActive hook) and prints a hint that provider/model swap waits
// for the next session start.
//
// A future slice (F-5) will replace the args-driven verb with a
// full-screen takeover switcher bound to Ctrl+F; the slash stays as
// the headless / muscle-memory entry point.
func (m *Model) frameSlash(args string) tea.Cmd {
	args = strings.TrimSpace(args)
	if m.frame.Active == "" {
		return statusCmd("frames not wired (legacy single-shelf mode)", statusWarn)
	}
	verb, rest, _ := strings.Cut(args, " ")
	rest = strings.TrimSpace(rest)
	switch verb {
	case "":
		return statusCmd("active frame: "+m.frame.Active, statusInfo)
	case "list":
		return statusCmd("frames: "+strings.Join(m.frame.Available, ", "), statusInfo)
	case "switch", "use":
		// `use` kept short for muscle memory. Same semantics as switch.
		return m.frameSwitchCmd(rest)
	default:
		return statusCmd(
			"/frame   show active · /frame list · /frame switch <name>",
			statusInfo,
		)
	}
}

// frameSwitchCmd persists a frame switch via the wired SwitchActive
// hook. The chat header pill updates in place; the provider/model
// swap is recorded but does not re-instantiate the running loop —
// users who care about model switch restart the session. The status
// echo names that constraint so the surprise doesn't land at the
// next assistant turn.
func (m *Model) frameSwitchCmd(target string) tea.Cmd {
	switch {
	case target == "":
		return statusCmd("usage: /frame switch <name>", statusWarn)
	case target == m.frame.Active:
		return statusCmd("already in frame "+target, statusInfo)
	case !m.frameKnown(target):
		return statusCmd(
			fmt.Sprintf("unknown frame %q; available: %s",
				target, strings.Join(m.frame.Available, ", ")),
			statusWarn,
		)
	case m.frame.SwitchActive == nil:
		return statusCmd("frame switching not wired in this session", statusWarn)
	}
	if err := m.frame.SwitchActive(target); err != nil {
		return statusCmd("switch failed: "+err.Error(), statusWarn)
	}
	m.frame.Active = target
	m.rerenderViewport()
	return statusCmd(
		"switched to "+target+
			" (provider/model take effect at next session start)",
		statusInfo,
	)
}

// frameKnown reports whether name is in the wired Available list. Used
// by frameSwitchCmd to fail fast on typos before calling the hook.
func (m *Model) frameKnown(name string) bool {
	for _, n := range m.frame.Available {
		if n == name {
			return true
		}
	}
	return false
}

// statusCmd is the small helper for slash echoes — exposed here so the
// frame package's switch flow uses the same statusMsg shape as every
// other slash without dragging in the chat.go branches.
func statusCmd(text string, kind statusKind) tea.Cmd {
	return func() tea.Msg {
		return statusMsg{text: text, kind: kind}
	}
}
