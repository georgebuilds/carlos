package chat

import (
	"fmt"
	"sort"
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
	case "new":
		// Phase F-10: opens the wizard with optional name pre-fill.
		// Works even from the bare chat (no switcher takeover) since
		// the wizard is its own overlay slot.
		m.openNewFrameWizard(rest)
		return nil
	default:
		return statusCmd(
			"/frame   show active · /frame list · /frame switch <name> · /frame new [name]",
			statusInfo,
		)
	}
}

// frameSwitchCmd persists a frame switch via the wired SwitchActive
// hook. The chat header pill updates in place; the provider/model
// swap is recorded but does not re-instantiate the running loop -
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
	// Refresh the per-frame render fields in place so the header pill
	// + /whoami reflect the new frame immediately. The live-swap loop
	// has already replaced the underlying chatglue.Loop; this picks up
	// the new frame's Mode / Capabilities / Glyph / Accent.
	if m.frame.LookupFrame != nil {
		if upd, ok := m.frame.LookupFrame(target); ok {
			m.frame.Glyph = upd.Glyph
			m.frame.Accent = upd.Accent
			m.frame.Mode = upd.Mode
			m.frame.Capabilities = upd.Capabilities
		}
	}
	m.rerenderViewport()
	return statusCmd("switched to "+target, statusInfo)
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

// statusCmd is the small helper for slash echoes - exposed here so the
// frame package's switch flow uses the same statusMsg shape as every
// other slash without dragging in the chat.go branches.
func statusCmd(text string, kind statusKind) tea.Cmd {
	return func() tea.Msg {
		return statusMsg{text: text, kind: kind}
	}
}

// modeSlash echoes the active frame's orchestrator mode, or sets it
// when called with one of the three names. The persisted SwitchMode
// hook is responsible for writing the config; the in-process Model
// updates immediately so the header pill reflects the new mode.
func (m *Model) modeSlash(args string) tea.Cmd {
	if m.frame.Active == "" {
		return statusCmd("frames not wired (legacy single-shelf mode)", statusWarn)
	}
	args = strings.TrimSpace(args)
	current := m.frame.Mode
	if current == "" {
		current = "solo"
	}
	if args == "" {
		return statusCmd("mode in "+m.frame.Active+": "+current+" (try /mode solo|tight|orchestrator)", statusInfo)
	}
	switch args {
	case "solo", "tight", "orchestrator":
		// fall through
	default:
		return statusCmd("unknown mode "+args+"; want solo, tight, or orchestrator", statusWarn)
	}
	if args == current {
		return statusCmd("mode already "+args, statusInfo)
	}
	if m.frame.SwitchMode == nil {
		return statusCmd("mode switching not wired in this session", statusWarn)
	}
	if err := m.frame.SwitchMode(args); err != nil {
		return statusCmd("mode switch failed: "+err.Error(), statusWarn)
	}
	m.frame.Mode = args
	m.rerenderViewport()
	return statusCmd("mode is now "+args+" in "+m.frame.Active, statusInfo)
}

// whoamiSlash echoes a concise identity surface: frame, mode, provider,
// model. Useful after a /frame switch when the user wants to confirm
// the live swap actually flipped the dispatch. When the chat is
// running in legacy single-shelf mode (no frame wired) the slash
// returns just the model name surfaced in the header.
func (m *Model) whoamiSlash() tea.Cmd {
	if m.frame.Active == "" {
		return statusCmd("carlos (no frame wired)", statusInfo)
	}
	mode := m.frame.Mode
	if mode == "" {
		mode = "solo"
	}
	parts := []string{
		"carlos in frame " + m.frame.Active + " (" + mode + ")",
	}
	if m.frame.Identity != nil {
		provider, model := m.frame.Identity()
		if provider != "" || model != "" {
			parts = append(parts, "provider="+provider+" model="+model)
		}
	}
	if len(m.frame.Capabilities) > 0 {
		caps := make([]string, 0, len(m.frame.Capabilities))
		for k, v := range m.frame.Capabilities {
			caps = append(caps, k+"="+v)
		}
		sort.Strings(caps)
		parts = append(parts, "capabilities: "+strings.Join(caps, ", "))
	}
	return statusCmd(strings.Join(parts, " · "), statusInfo)
}

// capabilitiesSlash echoes the wired capability -> backend map for the
// active frame. Phase C-7. Empty map prints a CTA pointing the user at
// the config block they need to populate.
func (m *Model) capabilitiesSlash() tea.Cmd {
	if m.frame.Active == "" {
		return statusCmd("frames not wired (legacy single-shelf mode)", statusWarn)
	}
	if len(m.frame.Capabilities) == 0 {
		return statusCmd(
			"no capabilities wired in frame "+m.frame.Active+
				"; add `capabilities.<name>."+m.frame.Active+".backend` to config.yaml",
			statusInfo,
		)
	}
	parts := make([]string, 0, len(m.frame.Capabilities))
	for k, v := range m.frame.Capabilities {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return statusCmd(
		"frame "+m.frame.Active+": "+strings.Join(parts, ", "),
		statusInfo,
	)
}
