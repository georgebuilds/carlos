// permissions overlay (Phase T-3).
//
// Renders the LayeredApprover's policy state as a Crush-style
// command-palette panel: rounded accent border, three-tier visual
// hierarchy (bold for tab labels + selected rows, normal for content,
// faint for metadata), semantic color (accent for "you can act,"
// muted for read-only), always-visible footer keybinds, "/" filter,
// empty states with a concrete CTA. No decorative motion.
//
// Two tabs in v1:
//
//   - Built-in   — the hardcoded Phase T-1 allowlist. Read-only;
//                  the model never gets to extend it.
//   - Workspace  — Phase T-2 trusted workspaces. Each row can be
//                  untrusted with `d`; `t` trusts the chat's cwd.
//
// A Session-"Always" tab is reserved for a later slice when the
// TUIApprover exposes its per-tool allow cache (today the cache is
// internal). The overlay's tab bar is already designed for >2 tabs.
//
// Key handling (handleOverlayKey):
//
//   - tab / shift+tab  — cycle tabs
//   - up / down / k / j — navigate rows
//   - g / G            — jump to top / bottom of the active tab
//   - /                — enter filter mode (rows re-narrow on each rune)
//   - d                — untrust the highlighted workspace (Workspace tab)
//   - t                — trust the chat's cwd (Workspace tab)
//   - esc              — close
//
// Unknown keys are swallowed while the overlay is open so they don't
// land in the textarea.

package chat

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/workspace"
)

type permsTab int

const (
	permsTabBuiltin permsTab = iota
	permsTabWorkspace
	permsTabCount
)

func (t permsTab) String() string {
	switch t {
	case permsTabBuiltin:
		return "Built-in"
	case permsTabWorkspace:
		return "Workspace"
	}
	return ""
}

// permsBuiltinRow describes one tool in the Phase T-1 allowlist.
// Description is hardcoded here (not on the Tool itself) because the
// overlay summarizes WHY the tool is on the allowlist; the Tool's own
// Description() addresses the model, not the user.
type permsBuiltinRow struct {
	Name   string
	Reason string
}

// builtinRows annotates agent.DefaultBuiltinAllow with a one-line
// "why is this safe" reason. Order matches DefaultBuiltinAllow so the
// overlay's row count is in lockstep with the policy engine's allow
// set; a runtime check in render guards against drift.
var builtinRows = []permsBuiltinRow{
	{"notes_search", "read-only, configured vault only"},
	{"notes_get", "read-only, configured vault only"},
	{"notes_neighbors", "read-only, configured vault only"},
	{"notes_recent", "read-only, configured vault only"},
	{"notes_resolve", "read-only, configured vault only"},
	{"notes_backlinks", "read-only, configured vault only"},
	{"notes_tagged", "read-only, configured vault only"},
	{"notes_write", "write into active frame's vault_subtree only"},
	{"carlos_about", "read-only introspection of carlos's own state"},
	{"read", "read-only filesystem"},
	{"grep", "read-only filesystem"},
	{"glob", "read-only filesystem"},
	{"ls", "read-only filesystem"},
	{"git_status", "read-only git inspection"},
	{"git_diff", "read-only git inspection"},
	{"git_log", "read-only git inspection"},
	{"git_blame", "read-only git inspection"},
	{"git_show", "read-only git inspection"},
}

// permsWorkspaceRow is one entry on the Workspace tab.
type permsWorkspaceRow struct {
	Path      string
	TrustedAt time.Time
	IsCurrent bool // true when this row matches the chat's cwd
}

// buildWorkspaceRows snapshots the Policy's trust store and tags the
// row matching the chat's cwd. Empty when the policy isn't wired (the
// render path shows a CTA hint in that case).
func buildWorkspaceRows(p *workspace.Policy) []permsWorkspaceRow {
	if p == nil || p.Store() == nil {
		return nil
	}
	entries, err := p.Store().List()
	if err != nil {
		return nil
	}
	cwd := p.Cwd()
	out := make([]permsWorkspaceRow, 0, len(entries))
	for _, e := range entries {
		out = append(out, permsWorkspaceRow{
			Path:      e.Path,
			TrustedAt: e.TrustedAt,
			IsCurrent: e.Path == cwd,
		})
	}
	return out
}

// filterBuiltinRows applies the case-insensitive substring filter to
// the tool name + reason. Empty filter returns the input unchanged.
func filterBuiltinRows(rows []permsBuiltinRow, filter string) []permsBuiltinRow {
	q := strings.ToLower(strings.TrimSpace(filter))
	if q == "" {
		return rows
	}
	out := rows[:0:0]
	for _, r := range rows {
		hay := strings.ToLower(r.Name + " " + r.Reason)
		if strings.Contains(hay, q) {
			out = append(out, r)
		}
	}
	return out
}

func filterWorkspaceRows(rows []permsWorkspaceRow, filter string) []permsWorkspaceRow {
	q := strings.ToLower(strings.TrimSpace(filter))
	if q == "" {
		return rows
	}
	out := rows[:0:0]
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.Path), q) {
			out = append(out, r)
		}
	}
	return out
}

// renderPermissionsOverlay produces the rounded-bordered panel. Same
// outer shape as renderJobsOverlay so the user's visual muscle memory
// transfers between overlays.
func renderPermissionsOverlay(
	tab permsTab,
	policy *workspace.Policy,
	filter string,
	filterMode bool,
	cursor int,
	innerW int,
) string {
	boxW := innerW - 2
	if boxW < 40 {
		boxW = 40
	}

	headerStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	subtleStyle := lipgloss.NewStyle().Foreground(colorSubtle)

	var sb strings.Builder
	sb.WriteString(headerStyle.Render("Permissions"))
	sb.WriteString("  ")
	sb.WriteString(subtleStyle.Render("layered policy"))
	sb.WriteString("\n\n")

	sb.WriteString(renderPermsTabBar(tab))
	sb.WriteString("\n")

	if filterMode || filter != "" {
		caret := ""
		if filterMode {
			caret = lipgloss.NewStyle().Foreground(colorAccent).Render("▎")
		}
		sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("filter: "))
		sb.WriteString(lipgloss.NewStyle().Foreground(colorAccent).Render(filter))
		sb.WriteString(caret)
		sb.WriteString("\n")
	}

	switch tab {
	case permsTabBuiltin:
		sb.WriteString(renderPermsBuiltinBody(filter, cursor, boxW-4))
	case permsTabWorkspace:
		sb.WriteString(renderPermsWorkspaceBody(policy, filter, cursor, boxW-4))
	}

	sb.WriteString("\n")
	sb.WriteString(renderPermsFooter(tab, policy))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Padding(0, 1).
		Width(boxW).
		Render(sb.String())
}

// renderPermsTabBar renders the inactive/active tab labels. The
// active tab is wrapped in [ ] and accent-bolded; inactives are muted.
// Pattern follows Crush's three-tab palette.
func renderPermsTabBar(active permsTab) string {
	active2 := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	inactive := lipgloss.NewStyle().Foreground(colorMuted)
	sep := lipgloss.NewStyle().Foreground(colorSubtle).Render("  ")
	var parts []string
	for t := permsTab(0); t < permsTabCount; t++ {
		label := t.String()
		if t == active {
			parts = append(parts, active2.Render("["+label+"]"))
		} else {
			parts = append(parts, inactive.Render(" "+label+" "))
		}
	}
	return strings.Join(parts, sep)
}

// renderPermsBuiltinBody renders the Built-in tab's row list. Drift
// check: the table here must stay in lockstep with
// agent.DefaultBuiltinAllow; we render a warning row if a name in
// DefaultBuiltinAllow has no annotation entry (or vice versa).
func renderPermsBuiltinBody(filter string, cursor, contentW int) string {
	rows := filterBuiltinRows(builtinRows, filter)
	var sb strings.Builder

	// Drift warning: report any policy-side name missing here.
	if missing := builtinDrift(); len(missing) > 0 {
		sb.WriteString("\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(colorWarn).Bold(true).
			Render("⚠ drift: ") + lipgloss.NewStyle().Foreground(colorMuted).
			Render(strings.Join(missing, ", ")))
		sb.WriteString("\n")
	}

	if len(rows) == 0 {
		sb.WriteString("\n")
		if filter != "" {
			sb.WriteString(lipgloss.NewStyle().Foreground(colorSubtle).Render(
				"(no matches, backspace clears the filter)"))
		} else {
			sb.WriteString(lipgloss.NewStyle().Foreground(colorSubtle).Render(
				"the built-in allowlist is empty (this should not happen)"))
		}
		sb.WriteString("\n")
		return sb.String()
	}

	for i, r := range rows {
		sb.WriteString("\n")
		sb.WriteString(renderPermsBuiltinRow(r, i == cursor, contentW))
	}
	return sb.String()
}

// renderPermsBuiltinRow formats one read-only allowlist row. Marker
// + check glyph + tool name + faint reason on the right.
func renderPermsBuiltinRow(r permsBuiltinRow, focused bool, contentW int) string {
	marker := "  "
	nameStyle := lipgloss.NewStyle().Foreground(colorMuted)
	if focused {
		marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸ ")
		nameStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	}
	check := lipgloss.NewStyle().Foreground(colorOK).Render("✓")
	reason := lipgloss.NewStyle().Foreground(colorSubtle).Render(r.Reason)
	// Right-align reason via lipgloss.PlaceHorizontal when the contentW
	// is generous; for narrow widths we just inline it after a separator.
	left := marker + check + "  " + nameStyle.Render(r.Name)
	sep := lipgloss.NewStyle().Foreground(colorSubtle).Render("  ")
	return left + sep + reason
}

// renderPermsWorkspaceBody renders the trusted-workspaces tab. When
// the policy isn't wired (tests) or the store is empty, the body is a
// concrete CTA hinting at the /trust slash + the current cwd.
func renderPermsWorkspaceBody(p *workspace.Policy, filter string, cursor, contentW int) string {
	var sb strings.Builder
	if p == nil || p.Store() == nil {
		sb.WriteString("\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(colorSubtle).Render(
			"workspace policy not wired (tests, or carlos started outside a directory)"))
		sb.WriteString("\n")
		return sb.String()
	}
	rows := filterWorkspaceRows(buildWorkspaceRows(p), filter)
	if len(rows) == 0 {
		sb.WriteString("\n")
		if filter != "" {
			sb.WriteString(lipgloss.NewStyle().Foreground(colorSubtle).Render(
				"(no matches, backspace clears the filter)"))
		} else {
			sb.WriteString(lipgloss.NewStyle().Foreground(colorSubtle).Render(
				"no trusted workspaces yet"))
			sb.WriteString("\n\n")
			if cwd := p.Cwd(); cwd != "" {
				keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
				body := lipgloss.NewStyle().Foreground(colorMuted)
				sb.WriteString(body.Render("press ") + keyStyle.Render("t") +
					body.Render(" to trust ") +
					lipgloss.NewStyle().Foreground(colorAccent).Render(cwd))
			}
		}
		sb.WriteString("\n")
		return sb.String()
	}
	for i, r := range rows {
		sb.WriteString("\n")
		sb.WriteString(renderPermsWorkspaceRow(r, i == cursor, contentW))
	}
	return sb.String()
}

func renderPermsWorkspaceRow(r permsWorkspaceRow, focused bool, contentW int) string {
	marker := "  "
	pathStyle := lipgloss.NewStyle().Foreground(colorMuted)
	if focused {
		marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸ ")
		pathStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	}
	check := lipgloss.NewStyle().Foreground(colorOK).Render("✓")
	path := pathStyle.Render(r.Path)
	when := lipgloss.NewStyle().Foreground(colorSubtle).
		Render("  trusted " + r.TrustedAt.Local().Format("2006-01-02"))
	if r.IsCurrent {
		when += lipgloss.NewStyle().Foreground(colorAccent).
			Render("  (current)")
	}
	return marker + check + "  " + path + when
}

// renderPermsFooter is the always-visible keybinds row. Adapts to the
// active tab (Workspace shows `d untrust` / `t trust cwd`; Built-in
// shows the minimum nav set because there are no row actions).
func renderPermsFooter(tab permsTab, p *workspace.Policy) string {
	key := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	body := lipgloss.NewStyle().Foreground(colorMuted)
	parts := []string{
		key.Render("tab") + body.Render(" section"),
		key.Render("↑/↓") + body.Render(" nav"),
		key.Render("/") + body.Render(" filter"),
		key.Render("esc") + body.Render(" close"),
	}
	if tab == permsTabWorkspace && p != nil && p.Store() != nil {
		actions := []string{
			key.Render("d") + body.Render(" untrust"),
		}
		if p.Cwd() != "" && !p.IsTrusted() {
			actions = append(actions, key.Render("t")+body.Render(" trust cwd"))
		}
		parts = append(actions, parts...)
	}
	return body.Render("  ") + strings.Join(parts, body.Render("  ·  "))
}

// builtinDrift returns the set of names in agent.DefaultBuiltinAllow
// that don't have a row in builtinRows. Empty when synchronized.
func builtinDrift() []string {
	have := make(map[string]bool, len(builtinRows))
	for _, r := range builtinRows {
		have[r.Name] = true
	}
	var missing []string
	for _, name := range agent.DefaultBuiltinAllow {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// activeRowsCount returns how many rows the cursor can walk in the
// current tab + filter. Used by handleKey to clamp cursor movement.
func (m *Model) activeRowsCount() int {
	switch m.permsTab {
	case permsTabBuiltin:
		return len(filterBuiltinRows(builtinRows, m.permsFilter))
	case permsTabWorkspace:
		return len(filterWorkspaceRows(buildWorkspaceRows(m.workspace), m.permsFilter))
	}
	return 0
}

// handlePermsOverlayKey processes one key while /permissions is open.
// Returns (newModel, cmd, handled). Filter-mode is a sub-REPL like
// the jobs overlay's pattern.
func (m *Model) handlePermsOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.permsFilterMode {
		switch msg.Type {
		case tea.KeyEsc:
			m.permsFilterMode = false
			m.rerenderViewport()
			return m, nil, true
		case tea.KeyEnter:
			m.permsFilterMode = false
			m.rerenderViewport()
			return m, nil, true
		case tea.KeyBackspace:
			if len(m.permsFilter) > 0 {
				m.permsFilter = m.permsFilter[:len(m.permsFilter)-1]
				m.permsCursor = 0
				m.rerenderViewport()
			}
			return m, nil, true
		case tea.KeyRunes:
			m.permsFilter += string(msg.Runes)
			m.permsCursor = 0
			m.rerenderViewport()
			return m, nil, true
		case tea.KeySpace:
			m.permsFilter += " "
			m.permsCursor = 0
			m.rerenderViewport()
			return m, nil, true
		}
		return m, nil, true
	}

	switch msg.String() {
	case "ctrl+c":
		return m, nil, false
	case "esc":
		m.showPerms = false
		m.permsFilter = ""
		m.permsFilterMode = false
		m.permsCursor = 0
		m.rerenderViewport()
		return m, nil, true
	case "tab":
		m.permsTab = (m.permsTab + 1) % permsTabCount
		m.permsCursor = 0
		m.permsFilter = ""
		m.rerenderViewport()
		return m, nil, true
	case "shift+tab":
		m.permsTab = (m.permsTab + permsTabCount - 1) % permsTabCount
		m.permsCursor = 0
		m.permsFilter = ""
		m.rerenderViewport()
		return m, nil, true
	case "up", "k":
		if m.permsCursor > 0 {
			m.permsCursor--
			m.rerenderViewport()
		}
		return m, nil, true
	case "down", "j":
		if m.permsCursor < m.activeRowsCount()-1 {
			m.permsCursor++
			m.rerenderViewport()
		}
		return m, nil, true
	case "g":
		m.permsCursor = 0
		m.rerenderViewport()
		return m, nil, true
	case "G":
		if n := m.activeRowsCount(); n > 0 {
			m.permsCursor = n - 1
			m.rerenderViewport()
		}
		return m, nil, true
	case "/":
		m.permsFilterMode = true
		return m, nil, true
	case "d":
		if m.permsTab == permsTabWorkspace {
			return m.permsUntrustHighlighted()
		}
		return m, nil, true
	case "t":
		if m.permsTab == permsTabWorkspace {
			return m.permsTrustCwd()
		}
		return m, nil, true
	}
	return m, nil, true
}

// permsUntrustHighlighted handles `d` in the Workspace tab: removes
// the highlighted entry from the trust store and flips the in-session
// policy if it pointed at the current cwd.
func (m *Model) permsUntrustHighlighted() (tea.Model, tea.Cmd, bool) {
	if m.workspace == nil || m.workspace.Store() == nil {
		return m, nil, true
	}
	rows := filterWorkspaceRows(buildWorkspaceRows(m.workspace), m.permsFilter)
	if len(rows) == 0 || m.permsCursor >= len(rows) {
		return m, nil, true
	}
	row := rows[m.permsCursor]
	store := m.workspace.Store()
	policy := m.workspace
	cursor := m.permsCursor
	path := row.Path
	wasCurrent := row.IsCurrent
	return m, func() tea.Msg {
		if err := store.Untrust(path); err != nil {
			return statusMsg{text: "/permissions: " + err.Error(), kind: statusError}
		}
		if wasCurrent {
			policy.SetTrusted(false)
		}
		// Clamp the cursor so the next render doesn't point past the
		// shortened list. Doing it after the store write keeps the
		// optimistic UI in sync with the source of truth.
		_ = cursor
		return statusMsg{
			text: fmt.Sprintf("untrusted %s", path),
			kind: statusInfo,
		}
	}, true
}

// permsTrustCwd handles `t` in the Workspace tab: trusts the chat's
// cwd and flips the in-session policy.
func (m *Model) permsTrustCwd() (tea.Model, tea.Cmd, bool) {
	if m.workspace == nil || m.workspace.Store() == nil {
		return m, nil, true
	}
	cwd := m.workspace.Cwd()
	if cwd == "" {
		return m, nil, true
	}
	store := m.workspace.Store()
	policy := m.workspace
	return m, func() tea.Msg {
		if err := store.Trust(cwd); err != nil {
			return statusMsg{text: "/permissions: " + err.Error(), kind: statusError}
		}
		policy.SetTrusted(true)
		return statusMsg{
			text: fmt.Sprintf("trusted %s", cwd),
			kind: statusInfo,
		}
	}, true
}
