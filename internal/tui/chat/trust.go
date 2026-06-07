// trust.go - chat-side handlers for the Phase T-2 workspace-trust
// slash commands. Three verbs:
//
//   /trust    - persist the chat's cwd into the trust store and flip
//               the in-session policy so the next tool call sees it
//   /untrust  - opposite: remove cwd from store + clear session view
//   /trusts   - list every trusted workspace (sorted by path)
//
// All three return a tea.Cmd that surfaces a one-line statusMsg, so
// the user sees what happened in the status bar without a modal. The
// list form drops a short multi-line text into the same status row;
// it stays compact (no overlay) because we render the manage view
// for the dense case.
//
// Failure modes are all "not wired" - the slashes don't crash when
// the policy isn't attached (tests + the headless `please` path).

package chat

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *Model) trustSlashEnable() tea.Cmd {
	if m.workspace == nil {
		return func() tea.Msg {
			return statusMsg{text: "/trust: workspace policy not wired", kind: statusWarn}
		}
	}
	store := m.workspace.Store()
	cwd := m.workspace.Cwd()
	if store == nil || cwd == "" {
		return func() tea.Msg {
			return statusMsg{text: "/trust: no workspace anchored (cwd resolution failed at startup)", kind: statusWarn}
		}
	}
	if err := store.Trust(cwd); err != nil {
		return func() tea.Msg {
			return statusMsg{text: "/trust: " + err.Error(), kind: statusError}
		}
	}
	m.workspace.SetTrusted(true)
	return func() tea.Msg {
		return statusMsg{
			text: fmt.Sprintf("trusted workspace: %s (read-only bash auto-approved)", cwd),
			kind: statusInfo,
		}
	}
}

func (m *Model) trustSlashDisable() tea.Cmd {
	if m.workspace == nil {
		return func() tea.Msg {
			return statusMsg{text: "/untrust: workspace policy not wired", kind: statusWarn}
		}
	}
	store := m.workspace.Store()
	cwd := m.workspace.Cwd()
	if store == nil || cwd == "" {
		return func() tea.Msg {
			return statusMsg{text: "/untrust: no workspace anchored", kind: statusWarn}
		}
	}
	if err := store.Untrust(cwd); err != nil {
		return func() tea.Msg {
			return statusMsg{text: "/untrust: " + err.Error(), kind: statusError}
		}
	}
	m.workspace.SetTrusted(false)
	return func() tea.Msg {
		return statusMsg{
			text: fmt.Sprintf("untrusted workspace: %s (bash will prompt again)", cwd),
			kind: statusInfo,
		}
	}
}

func (m *Model) trustSlashList() tea.Cmd {
	if m.workspace == nil || m.workspace.Store() == nil {
		return func() tea.Msg {
			return statusMsg{text: "/trusts: workspace policy not wired", kind: statusWarn}
		}
	}
	entries, err := m.workspace.Store().List()
	if err != nil {
		return func() tea.Msg {
			return statusMsg{text: "/trusts: " + err.Error(), kind: statusError}
		}
	}
	if len(entries) == 0 {
		return func() tea.Msg {
			return statusMsg{text: "no trusted workspaces yet - type /trust to add the current one", kind: statusInfo}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d trusted workspace(s):", len(entries))
	for _, e := range entries {
		fmt.Fprintf(&b, "\n  • %s", e.Path)
	}
	text := b.String()
	return func() tea.Msg {
		return statusMsg{text: text, kind: statusInfo}
	}
}
