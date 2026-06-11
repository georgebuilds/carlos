package chat

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/theme"
	"github.com/georgebuilds/carlos/internal/tui/slash"
)

// View composes header + transcript viewport + footer inside a thin
// brand-accent border. Onboarding uses a thick border + alt-screen; the
// chat surface uses a normal border because most of the visual weight
// is in the transcript itself, and a thick frame would fight it.
//
// Slice-1e-commit-1 is read-only: there is no input line. Subsequent
// commits drop a textarea between the transcript and the footer.
func (m *Model) View() string {
	if m.quitting {
		// Bubbletea will Print() the View on quit; leave a clean line.
		return ""
	}

	w, h := m.width, m.height
	if w == 0 || h == 0 {
		w, h = 100, 30
	}
	if w < minTermWidth || h < minTermHeight {
		return lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("carlos chat needs at least %dx%d. Current: %dx%d.",
				minTermWidth, minTermHeight, w, h))
	}

	border := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(colorAccent).
		Width(w - 2).
		Height(h - 2).
		Padding(0, 1)

	// border.GetWidth() is Width set above (w - 2) - the inner area
	// INCLUDING the outer padding. The 2-col horizontal padding eats
	// columns that children can't render into, so we subtract them
	// before handing the width down. Header/footer used to overflow
	// silently because they were narrow enough to fit anyway; the
	// new approval box exposes the bug because it's full-width.
	innerW := border.GetWidth() - 2
	inner := m.renderInner(innerW, border.GetHeight())
	return border.Render(inner)
}

// renderInner stacks header + transcript viewport + (optional) input +
// footer inside the border's inner box.
func (m *Model) renderInner(innerW, innerH int) string {
	header := m.renderHeader(innerW)
	footer := m.renderFooter(innerW)
	headerH := lipgloss.Height(header)
	footerH := lipgloss.Height(footer)

	var input string
	inputH := 0
	if !m.readOnly {
		// Refresh textarea width on each render so it tracks the box.
		taWidth := innerW - 2
		if taWidth < 20 {
			taWidth = 20
		}
		m.ta.SetWidth(taWidth)
		input = m.renderInput(innerW)
		inputH = lipgloss.Height(input)
	}

	// Approval prompt OR frame switcher OR jobs overlay OR help
	// overlay is a bordered panel above the input. Compute height
	// first so we can reserve it from the transcript area. They're
	// mutually exclusive - approval is modal (model is waiting),
	// jobs / perms / help / switcher are dismiss-on-keypress;
	// precedence: approval > switcher > jobs > perms > help.
	var approval string
	approvalH := 0
	if m.pendingApproval != nil {
		approval = renderApprovalBox(m.pendingApproval, innerW)
		approvalH = lipgloss.Height(approval)
	} else if m.showNewFrame {
		// Phase F-10: new-frame wizard renders in the same slot as
		// the switcher; precedence handled in chat.Update.
		wizH := innerH - headerH - footerH - inputH - 1
		if wizH < 10 {
			wizH = 10
		}
		approval = renderNewFrameOverlay(m, innerW, wizH)
		approvalH = lipgloss.Height(approval)
	} else if m.showFrameSwitcher {
		// Phase F-5: the switcher is full-screen takeover so it gets
		// the whole inner area (minus header/footer/input). We hand
		// it the inner height so it can vertically center the grid.
		switcherH := innerH - headerH - footerH - inputH - 1
		if switcherH < 10 {
			switcherH = 10
		}
		approval = renderFrameSwitcher(
			m.frame,
			m.switcherCursor,
			m.switcherPage,
			innerW,
			switcherH,
			m.switcherHelp,
		)
		approvalH = lipgloss.Height(approval)
	} else if m.showModeSwitcher {
		// Mode picker: same takeover slot as the frame switcher,
		// mutually exclusive with it (chat.Update gates each open).
		switcherH := innerH - headerH - footerH - inputH - 1
		if switcherH < 10 {
			switcherH = 10
		}
		approval = renderModeSwitcher(
			m.frame,
			m.modeSwitcherCursor,
			innerW,
			switcherH,
			m.modeSwitcherHelp,
		)
		approvalH = lipgloss.Height(approval)
	} else if m.showResume {
		// /resume picker shares the takeover slot.
		resumeH := innerH - headerH - footerH - inputH - 1
		if resumeH < 10 {
			resumeH = 10
		}
		approval = renderResumeOverlay(m, innerW, resumeH)
		approvalH = lipgloss.Height(approval)
	} else if m.showJobs && m.usershell != nil {
		approval = renderJobsOverlay(
			m.usershell.Jobs(),
			m.jobsFilter,
			m.jobsFilterMode,
			m.jobsCursor,
			innerW,
		)
		approvalH = lipgloss.Height(approval)
	} else if m.showPerms {
		approval = renderPermissionsOverlay(
			m.permsTab,
			m.workspace,
			m.permsFilter,
			m.permsFilterMode,
			m.permsCursor,
			innerW,
		)
		approvalH = lipgloss.Height(approval)
	} else if m.showHelp {
		approval = renderHelpBox(innerW)
		approvalH = lipgloss.Height(approval)
	} else if m.showFirstTrust && m.workspace != nil {
		approval = renderFirstTrustPrompt(m.workspace.Cwd(), innerW)
		approvalH = lipgloss.Height(approval)
	}

	// Transcript area gets whatever's left.
	transcriptH := innerH - headerH - footerH - inputH - approvalH - 1 // 1 row of breathing room
	if transcriptH < 3 {
		transcriptH = 3
	}

	// Resize viewport + rerender unconditionally each frame. The
	// "only on dimension change" optimization races with the
	// approval box opening/closing: GotoBottom was called from
	// applyEvent with the OLD viewport height, then the layout
	// shrank vp.Height without updating YOffset, leaving the view
	// stuck at top with new entries below the visible area. Calling
	// rerenderViewport every frame is cheap (composeTranscript is
	// O(transcript) and the slice is small) and keeps the "follow
	// the tail" semantics correct.
	//
	// Inline sub-agent panel: when the chat has at least one live
	// child AND innerW clears splitMinWidth, the transcript shrinks
	// to leave room for the right-side panel. Below splitMinWidth
	// the panel collapses to a dim footer line so the transcript
	// keeps the full width.
	panelW := 0
	showSplit := false
	showFallback := false
	if len(m.childrenSnap) > 0 {
		if innerW >= splitMinWidth {
			panelW = panelWidth(innerW)
			showSplit = true
		} else {
			showFallback = true
		}
	}
	transcriptW := innerW
	if showSplit {
		transcriptW = innerW - panelW - 1
		if transcriptW < 30 {
			transcriptW = 30
		}
	}
	m.vp.Width = transcriptW
	m.vp.Height = transcriptH
	m.rerenderViewport()

	transcriptBody := m.vp.View()
	if showSplit {
		panel := renderChildrenPanel(m.childrenSnap, panelW, time.Now())
		sep := childrenPanelSeparator(transcriptH)
		left := lipgloss.NewStyle().Width(transcriptW).Render(transcriptBody)
		right := lipgloss.NewStyle().Width(panelW).Render(panel)
		transcriptBody = lipgloss.JoinHorizontal(lipgloss.Top, left, sep, right)
	}

	parts := []string{header, transcriptBody}
	if approval != "" {
		parts = append(parts, approval)
	}
	if !m.readOnly {
		parts = append(parts, input)
	}
	if showFallback {
		parts = append(parts, renderChildrenFallbackLine(m.childrenSnap, innerW))
	}
	parts = append(parts, footer)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// childrenPanelSeparator paints the single-column dim divider between
// the transcript and the right-side roster. Height tracks the
// transcript so the separator runs the full panel length.
func childrenPanelSeparator(h int) string {
	if h < 1 {
		h = 1
	}
	bar := lipgloss.NewStyle().Foreground(colorSubtle).Render("│")
	rows := make([]string, h)
	for i := range rows {
		rows[i] = bar
	}
	return strings.Join(rows, "\n")
}

// renderInput frames the textarea with a thin separator above so the
// input row reads as visually distinct from the transcript above it.
//
// Slash-mode autocomplete adds two optional bands stacked above the
// separator: the multi-row "hint" panel (alternates + description +
// keybinds), and replaces the textarea row itself with a custom
// inline-ghost renderer so the suggested completion paints in dim
// alongside the user's cursor. Outside slash mode renderInput stays
// at its slice-1e shape - separator + ta.View().
func (m *Model) renderInput(w int) string {
	sep := lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", w))
	if m.slashSuggest.open {
		hint := renderSlashHint(m.slashSuggest, w)
		inputRow := renderSlashInputRow(m, w)
		return strings.Join([]string{hint, sep, inputRow}, "\n")
	}
	return sep + "\n" + m.ta.View()
}

// renderHeader composes the four-item chat-header row:
//
//	<glyph> <id> · 🧠 <model> · <frame-pill> · ⬥ <mode>     carlos chat
//
// The leading glyph is the agent state glyph from theme.StateGlyph,
// colored by state - the prior bracketed "[● running]" label was
// retired because the glyph alone already encodes state and the
// "running" word doubled the cell footprint without adding signal.
// The model gets a 🧠 prefix and loses its parentheses so it reads as
// a labeled chip, not a parenthetical aside. The mode gets a colored
// diamond (⬥) whose color tracks modeCardAccent so the at-a-glance
// posture matches the mode switcher overlay (tight=warn, solo=accent,
// orchestrator=ok).
//
// Items 2-4 are emitted only when their backing fields are non-empty,
// and a `·` separator is inserted only BETWEEN two emitted items - no
// leading or trailing separator, no double separator when an item
// drops out.
//
// Side effect: records the terminal-cell columns of the frame and mode
// pills onto the model so tea.MouseMsg can route header clicks to the
// matching overlay. The columns are absolute (border + padding are
// already accounted for - the chat box's outer border eats 1 cell on
// the left and the inner Padding(0,1) eats another, so the header
// content sits at x=2 in the alt-screen).
func (m *Model) renderHeader(w int) string {
	id := shortID(m.agentID)
	state, model := m.headerState()
	idStyle := lipgloss.NewStyle().Bold(true).Foreground(colorAccent)

	// Reset hitboxes each frame; the writers below populate them when
	// the matching pill renders. headerContentX0 mirrors View()'s outer
	// border (1) + Padding(0,1) (1) = 2-cell left offset.
	const headerContentX0 = 2
	m.framePillColStart, m.framePillColEnd = 0, 0
	m.modePillColStart, m.modePillColEnd = 0, 0

	sep := " " + framePillSep() + " "

	// Item 1: colored state glyph + id (always emitted).
	glyphR := lipgloss.NewStyle().Foreground(stateBadgeColor(state)).Bold(true).Render(theme.StateGlyph(state))
	left := glyphR + " " + idStyle.Render(id)

	// Item 2: 🧠 + model (only when a model is wired).
	if model != "" {
		brain := lipgloss.NewStyle().Foreground(colorMuted).Render("🧠")
		modelStyle := lipgloss.NewStyle().Foreground(colorMuted)
		left += sep + brain + " " + modelStyle.Render(displayModelName(model))
	}

	// Items 3 + 4: frame pill and mode pill (both gated on a wired
	// frame so the legacy single-shelf paths still render cleanly).
	if m.frame.Active != "" {
		left += sep
		m.framePillColStart = headerContentX0 + lipgloss.Width(left)
		left += framePill(m.frame)
		m.framePillColEnd = headerContentX0 + lipgloss.Width(left)

		mode := m.frame.Mode
		if mode == "" {
			mode = frame.ModeSolo
		}
		left += sep
		m.modePillColStart = headerContentX0 + lipgloss.Width(left)
		diamond := lipgloss.NewStyle().Foreground(modeCardAccent(mode)).Bold(true).Render(modeDiamondGlyph)
		modeStyle := lipgloss.NewStyle().Foreground(colorSubtle)
		if mode != frame.ModeSolo {
			// Non-solo modes get the muted color so the label reads as
			// "non-default posture" without fighting the diamond's
			// accent.
			modeStyle = lipgloss.NewStyle().Foreground(colorMuted)
		}
		left += diamond + " " + modeStyle.Render(mode)
		m.modePillColEnd = headerContentX0 + lipgloss.Width(left)
	}
	right := lipgloss.NewStyle().Foreground(colorMuted).Render("carlos chat")

	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// modeDiamondGlyph is the black-medium-diamond (U+2B25) that precedes
// the mode label in the header. Picked over ◆ (BLACK DIAMOND) because
// it renders at single-cell width across the terminals we support
// (lipgloss / runewidth tags U+2B25 as narrow); the larger ◆ trips
// some terminals into emitting 2 cells which would drift the mode
// pill's hitbox start out from under the user's cursor.
const modeDiamondGlyph = "⬥"

// framePillSep returns the dim middle-dot used between items in the
// header row (and other chrome surfaces). Rendered fresh on every
// call so it tracks the live palette: an earlier var-at-init-time
// version cached the rendering when `colorSubtle` was still the
// zero-value lipgloss.Color, which meant the dot shipped to the
// terminal without ANSI styling for the entire process lifetime
// regardless of what ApplyPalette later set the subtle slot to.
func framePillSep() string {
	return lipgloss.NewStyle().Foreground(colorSubtle).Render("·")
}

// framePill renders the active frame as "<accent-glyph> <muted-name>"
// for the chat header. The frame's accent stays on the glyph (which
// is what tells the user at a glance "which frame am I in") while the
// name itself is rendered in the same muted grey as the model and
// mode labels so the four header items share one typographic
// register. The inline picker (in onboarding) still calls
// frame.Pill directly when it wants the accent on the name too.
func framePill(f FrameUI) string {
	glyph := f.Glyph
	if glyph == "" {
		glyph = frame.DefaultGlyphFor(f.Active)
	}
	accentGlyph := lipgloss.NewStyle().Foreground(frame.AccentColor(f.Accent)).Render(glyph)
	nameMuted := lipgloss.NewStyle().Foreground(colorMuted).Render(f.Active)
	return accentGlyph + " " + nameMuted
}

// isNoColor returns true when the lipgloss color profile is the empty
// (uncoloured) profile, which is how this codebase signals NO_COLOR /
// non-TTY. The frame.Pill helper takes a bool so it stays standalone.
func isNoColor() bool {
	// colorAccent is rendered by the theme package which already honours
	// NO_COLOR - if the accent has no foreground, we're monochrome.
	return colorAccent == ""
}

// headerState reads the projection for the active agent. If the agent
// isn't in the projection yet (no state_change seen), report a placeholder.
//
// Model resolution: the projection's Model field reflects what was
// recorded at agent creation; mid-session /model swaps update the
// runtime's liveDispatch but never re-emit a state_change event the
// projection would apply. We therefore prefer FrameUI.Identity() —
// which the runtime updates in lockstep with the model swap — when
// it's wired. Falls back to the projection's stored model for the
// dev-aid / test paths where Identity is nil.
func (m *Model) headerState() (agent.State, string) {
	state := agent.StateSpawning
	var model string
	if row, ok := m.proj.Get(m.agentID); ok {
		state = row.State
		model = row.Model
	}
	// Identity wins over the projection's stored model even when the
	// projection has no row yet (e.g. pre-backfill). This lets the
	// runtime surface the freshly-chosen model in the header on the
	// very first render after construction without waiting for a
	// state_change event to land first.
	if m.frame.Identity != nil {
		if _, live := m.frame.Identity(); live != "" {
			model = live
		}
	}
	return state, model
}

// displayModelName trims the OpenRouter vendor prefix off the model id
// so the chat header shows "gemini-3.5-flash" instead of the noisier
// "google/gemini-3.5-flash". The bare id ("claude-opus-4-5", "gpt-5",
// "llama3:latest", …) is left untouched. OpenRouter is the only
// provider whose model ids carry a "<vendor>/" prefix today, so a
// single check on "/" presence is enough and stays consistent across
// the five providers without us having to thread the provider name
// through the projection. Authoritative model id is kept in the
// AgentRow / Loop config; this helper is render-only.
func displayModelName(model string) string {
	if i := strings.IndexByte(model, '/'); i >= 0 && i < len(model)-1 {
		return model[i+1:]
	}
	return model
}

// stateBadgeColor maps an agent state to the foreground color the
// header glyph paints with. Same priority encoding as the legacy
// bracketed badge (warn for blocked / failed / awaiting / orphaned,
// agent neutral-light for the active running path, ok for done,
// muted for anything else). Color carries priority; the theme glyph
// itself carries identity, so a NO_COLOR terminal still distinguishes
// states by shape - the same accessibility win the previous design
// got from its label.
func stateBadgeColor(s agent.State) lipgloss.Color {
	switch s {
	case agent.StateAwaitingInput, agent.StateBlocked, agent.StateOrphaned:
		return colorWarn
	case agent.StateRunning, agent.StateCompacting:
		return colorAgent
	case agent.StateDone:
		return colorOK
	case agent.StateFailed:
		return colorWarn
	default:
		return colorMuted
	}
}

// stateBadge renders the legacy "[<glyph> <label>]" badge that the
// resume overlay's session cards still use. The chat header dropped
// it in favor of a bare colored glyph (the label was redundant once
// the glyph alone carried the state), but the session card has more
// vertical room and benefits from the explicit word.
func stateBadge(s agent.State) string {
	return lipgloss.NewStyle().
		Foreground(stateBadgeColor(s)).
		Bold(true).
		Render("[" + theme.StateGlyph(s) + " " + s.String() + "]")
}

// renderFooter is the footer band. Up to two rows:
//
//	[ status echo (if any)                                    ]   ← row 1
//	[ keybind hints                  type /help for commands ]    ← row 2
//
// The status row shows slash-command echoes / errors and clears on the
// next keystroke (handled in Update). The keybind row is always present.
//
// Phase U S4: when a usershell.Manager is wired AND the user is in any
// of the three user-shell footer states (typing-shell, fg-running, or
// bg-only), the right-aligned tip slot is replaced with the user-shell
// hint line so the actionable next-keystroke information always
// dominates the footer.
func (m *Model) renderFooter(w int) string {
	keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(colorMuted)
	var hints string
	if m.readOnly {
		hints = keyStyle.Render("pgup/pgdn") + hintStyle.Render(" scroll  ") +
			keyStyle.Render("shift+drag") + hintStyle.Render(" select  ") +
			keyStyle.Render("ctrl-c") + hintStyle.Render(" quit")
	} else {
		hints = keyStyle.Render("enter") + hintStyle.Render(" send  ") +
			keyStyle.Render("shift-enter") + hintStyle.Render(" newline  ") +
			keyStyle.Render("pgup/pgdn") + hintStyle.Render(" scroll  ") +
			keyStyle.Render("shift+drag") + hintStyle.Render(" select  ") +
			keyStyle.Render("ctrl-c") + hintStyle.Render(" quit")
	}

	// User-shell footer hint takes priority over the right-aligned
	// /help tip. Idle state returns empty so we keep the existing
	// tip behavior.
	shellHint := renderUserShellFooter(m.computeUserShellFooterContext())
	// Drop the right-aligned tip when there isn't enough room left
	// after the keybind hints - wrapping it onto a second row pushes
	// the leftover word ("commands") flush-left, which reads worse
	// than just hiding the tip. The hints alone are the discoverable
	// surface; /help is already there for the user who needs it.
	//
	// User-shell hint, when present, replaces the tip - actionable
	// "you can press X right now" beats "type /help" every time.
	var tip string
	if shellHint != "" {
		tip = shellHint
	} else {
		tip = lipgloss.NewStyle().Foreground(colorSubtle).Render(footerTip(m.readOnly))
	}
	hintsW := lipgloss.Width(hints)
	tipW := lipgloss.Width(tip)
	var row string
	switch {
	case hintsW+tipW+2 <= w:
		row = hints + strings.Repeat(" ", w-hintsW-tipW) + tip
	default:
		row = hints
	}

	if m.status != "" {
		statusStyle := lipgloss.NewStyle().Foreground(statusColor(m.statusKind))
		return statusStyle.Render(m.status) + "\n" + row
	}
	// Phase F-8 cwd-hint footer: dim line above the keybind row when
	// the chat has detected an in-band cd into another frame's
	// territory and the user hasn't muted with Ctrl+L.
	if m.footerHint != "" {
		hintLine := lipgloss.NewStyle().Foreground(colorSubtle).Render(m.footerHint)
		return hintLine + "\n" + row
	}
	return row
}

// renderApprovalBox returns a bordered, accent-colored panel for a
// pending tool-call approval. Lives between the transcript and the
// input separator (see renderInner). Compact layout - three rows
// inside the box so the transcript above stays usable:
//
//	🧢 wants to run `bash`
//	    {"cmd":"ls -la"}
//	    [y] yes   [n] no   [A] always for bash    (esc denies)
//
// Width tracks the inner chat box minus the box's own border
// (Border = 2 cols; Padding = 0 - vertical real estate is precious
// when the transcript shares the screen).
// renderErrorCard paints a chatglue-surfaced loop / provider error
// as a bordered warn-color card so it visually matches the tool-card
// idiom (✗ glyph + label + brief detail) instead of leaking through
// the regular avatar/markdown renderer prefixed with "carlos: …".
//
// Layout:
//
//	┌───────────────────────────────────────────────────────────┐
//	│ ✗ openrouter · http2: timeout awaiting response header    │
//	└───────────────────────────────────────────────────────────┘
//
// The first colon-separated segment of the error text is treated as
// the "source" label (the head, e.g. "openrouter", "persist
// assistant turn", "loop"); the rest becomes the body preview. This
// is purely surface dressing — the full text remains on disk in the
// event log for triage.
func renderErrorCard(e transcriptEntry, width int) string {
	return renderErrorCardGroup([]transcriptEntry{e}, width)
}

// renderErrorCardGroup renders one or more error entries inside a
// single rounded-border box, separating consecutive rows with a thin
// horizontal rule. A single-entry group is visually identical to the
// pre-grouping renderErrorCard output — the border style, color, glyph,
// and content composition are unchanged.
//
// Group form (2+):
//
//	┌──────────────────────────────────────────────────┐
//	│ ✗ openrouter · HTTP 400: No models provided      │
//	├──────────────────────────────────────────────────┤
//	│ ✗ supervisor · spawn refused, frame mode 'solo'  │
//	└──────────────────────────────────────────────────┘
//
// Rationale: a flurry of provider / supervisor errors back-to-back was
// previously a stack of independent boxes that consumed N×3 vertical
// rows. The group packs N rows into N + 2 (one border top + one
// border bottom + N content rows + N-1 separators), which on three
// errors shaves four rows of chrome.
func renderErrorCardGroup(es []transcriptEntry, width int) string {
	if len(es) == 0 {
		return ""
	}
	const sideMargin = 4
	totalW := width - sideMargin*2
	if totalW < 30 {
		totalW = 30
	}
	boxW := totalW - 2
	contentW := boxW - 2
	if contentW < 20 {
		contentW = 20
	}

	lines := make([]string, 0, len(es)*2-1)
	for i, e := range es {
		if i > 0 {
			lines = append(lines, errorCardSeparator(contentW))
		}
		lines = append(lines, errorCardInnerLine(e, contentW))
	}
	body := strings.Join(lines, "\n")

	rendered := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorWarn).
		Padding(0, 1).
		Width(boxW).
		Render(body)

	pad := strings.Repeat(" ", sideMargin)
	rows := strings.Split(rendered, "\n")
	for i := range rows {
		rows[i] = pad + rows[i]
	}
	return strings.Join(rows, "\n")
}

// errorCardInnerLine composes one content row of an error card —
// glyph + label + optional " · detail". Width-budgeted so the caller
// can pack it into a multi-row group or a single-row card.
func errorCardInnerLine(e transcriptEntry, contentW int) string {
	label, detail := splitErrorHead(e.text)
	if label == "" {
		label = "error"
	}
	glyphStyle := lipgloss.NewStyle().Foreground(colorWarn).Bold(true)
	nameStyle := lipgloss.NewStyle().Foreground(colorWarn).Bold(true)
	sepStyle := lipgloss.NewStyle().Foreground(colorMuted)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)

	head := glyphStyle.Render("✗") + " " + nameStyle.Render(label)
	var preview string
	if detail != "" {
		maxInputW := contentW - lipgloss.Width(head) - 4
		if maxInputW >= 10 {
			preview = sepStyle.Render(" · ") + mutedStyle.Render(oneLine(detail, maxInputW))
		}
	}
	return head + preview
}

// errorCardSeparator paints the thin horizontal rule between two
// grouped error rows. Uses the muted color so the rule reads as a
// quiet divider, not a second border — color emphasis stays on the
// outer warn-tinted box.
func errorCardSeparator(contentW int) string {
	if contentW < 1 {
		contentW = 1
	}
	return lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", contentW))
}

// splitErrorHead picks a short "head" label out of a wrapped error
// chain. We take everything before the last colon-space pair as the
// source (e.g. "openrouter") and everything after as the body
// preview. Falls back to (text, "") when there's no clear split, so
// a short error still renders something readable.
func splitErrorHead(text string) (label, detail string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	// Walk left-to-right collecting colon-separated segments; the
	// last segment is the actual error body, the segment immediately
	// before is the closest source label.
	parts := strings.Split(text, ": ")
	if len(parts) == 1 {
		return text, ""
	}
	label = parts[len(parts)-2]
	detail = parts[len(parts)-1]
	return label, detail
}

// Tool-call entries — single or grouped runs — render via
// renderToolStrip in activity_strip.go. The bordered tool card was
// retired in favor of a single-line indented "activity strip" so a
// six-call context-load preamble no longer eats fifteen vertical
// lines of transcript. See activity_strip.go for the rendering rules.

func renderApprovalBox(req *ApprovalRequest, innerW int) string {
	if req == nil {
		return ""
	}
	// Width passed to lipgloss includes padding but excludes border.
	// We use 0 padding, so content width = boxW. Total rendered =
	// boxW + 2 (border). Subtract 2 from innerW so the box fits.
	boxW := innerW - 2
	if boxW < 20 {
		boxW = 20
	}
	// Content area for the wrapped input: leave 4 cols of left
	// indent so the JSON sits under the head's tool name.
	contentW := boxW - 4
	if contentW < 10 {
		contentW = 10
	}

	toolStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	argsStyle := lipgloss.NewStyle().Foreground(colorTool)
	head := "🧢 wants to run " + toolStyle.Render("`"+req.Tool+"`")

	const maxInputRows = 4
	args := clampLines(string(req.Input), contentW, maxInputRows)
	args = indentLines(argsStyle.Render(args), "    ")

	keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(colorMuted)
	prompt := "    " + keyStyle.Render("[y]") + hintStyle.Render(" yes   ") +
		keyStyle.Render("[n]") + hintStyle.Render(" no   ") +
		keyStyle.Render("[A]") + hintStyle.Render(" always for "+req.Tool) +
		"    " + hintStyle.Render("(esc denies)")

	body := lipgloss.JoinVertical(lipgloss.Left, head, args, prompt)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Width(boxW).
		Render(body)
}

// renderHelpBox is the slice-9d slash-command help panel. Built by
// reading slash.Builtins so new commands surface here automatically
// - no separate doc table to keep in sync. Two-column layout: name +
// args hint on the left, description on the right.
//
// Closes on any keypress (handled in Update). The footer hint at the
// bottom of the box tells the user how to dismiss.
func renderHelpBox(innerW int) string {
	boxW := innerW - 2
	if boxW < 30 {
		boxW = 30
	}
	contentW := boxW - 2 // account for box padding 0,1
	if contentW < 20 {
		contentW = 20
	}

	header := lipgloss.NewStyle().
		Foreground(colorAccent).Bold(true).
		Render("slash commands")

	nameStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	argsStyle := lipgloss.NewStyle().Foreground(colorMuted)
	descStyle := lipgloss.NewStyle().Foreground(colorSubtle)

	// Compute the widest "/<name> <args>" so descriptions align in
	// a column. Cap at 1/3 of contentW so a future verbose verb
	// can't push descriptions off-screen.
	//
	// Display order: alphabetical by verb. slash.Builtins is the
	// curated reading order used elsewhere (autocomplete priority,
	// status hint), but for an at-a-glance vocabulary lookup users
	// scan A→Z faster than they scan a curated list. Sort a local
	// copy so the package-level slice isn't mutated.
	sorted := make([]slash.Spec, len(slash.Builtins))
	copy(sorted, slash.Builtins)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})
	maxLeft := 0
	rows := make([][2]string, 0, len(sorted))
	for _, b := range sorted {
		left := "/" + b.Name
		if b.ArgsHint != "" {
			left += " " + b.ArgsHint
		}
		w := lipgloss.Width(left)
		if w > maxLeft {
			maxLeft = w
		}
		rows = append(rows, [2]string{left, b.Description})
	}
	if maxLeft > contentW/3 {
		maxLeft = contentW / 3
	}

	out := []string{header, ""}
	for _, r := range rows {
		left := r[0]
		// Split the left column into name + args so we can style
		// the name in accent and the args in muted, all aligned to
		// maxLeft cells.
		var leftRender string
		if i := strings.IndexByte(left, ' '); i > 0 {
			leftRender = nameStyle.Render(left[:i]) + argsStyle.Render(left[i:])
		} else {
			leftRender = nameStyle.Render(left)
		}
		pad := maxLeft - lipgloss.Width(left)
		if pad < 1 {
			pad = 1
		}
		out = append(out, leftRender+strings.Repeat(" ", pad)+"  "+descStyle.Render(r[1]))
	}

	out = append(out, "")
	out = append(out, lipgloss.NewStyle().Foreground(colorMuted).Italic(true).
		Render("press any key to dismiss"))

	body := strings.Join(out, "\n")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Padding(0, 1).
		Width(boxW).
		Render(body)
}

// oneLine collapses s to a single line at most maxW visual cells,
// trimming on newline boundaries first so multi-line tool inputs read
// as their first line (commonly the most informative - the bash cmd,
// the file path). Returns the trimmed text with "…" suffix if cut.
func oneLine(s string, maxW int) string {
	if maxW < 4 {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if lipgloss.Width(s) <= maxW {
		return s
	}
	// Visual-cell-aware truncation - char index is close enough for
	// the ASCII-heavy JSON shapes tool inputs use.
	cut := maxW - 1
	if cut > len(s) {
		cut = len(s)
	}
	return s[:cut] + "…"
}

// previewLines returns the first maxRows lines of s, each soft-wrapped
// at width cols. Excess rows replaced with "… N more lines" so a
// `bash ls /` result doesn't paint a hundred rows in the transcript.
// The model already saw the full bytes; this is the user's preview.
func previewLines(s string, width, maxRows int) string {
	if width < 4 {
		width = 4
	}
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	all := strings.Split(s, "\n")
	out := make([]string, 0, maxRows+1)
	for _, ln := range all {
		if len(out) >= maxRows {
			remaining := len(all) - maxRows
			out = append(out, fmt.Sprintf("… %d more line%s", remaining, plural(remaining)))
			return strings.Join(out, "\n")
		}
		// Wrap long lines so a 4 KiB bash one-liner doesn't blow out
		// the row width.
		for lipgloss.Width(ln) > width {
			cut := width
			if cut > len(ln) {
				cut = len(ln)
			}
			out = append(out, ln[:cut])
			ln = ln[cut:]
			if len(out) >= maxRows {
				remaining := len(all) - maxRows + 1 // current ln continuing
				out = append(out, fmt.Sprintf("… %d more line%s", remaining, plural(remaining)))
				return strings.Join(out, "\n")
			}
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// plural is a tiny helper so the preview footer reads "1 more line"
// vs "5 more lines" naturally.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// indentLines prepends prefix to every line in s. Used to nest the
// input + prompt rows under the tool-name head inside the approval
// box without introducing extra empty rows.
func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}

// clampLines truncates s to at most maxRows visual rows after soft-
// wrapping each \n-separated line at width. Excess rows are replaced
// with a single muted "(… truncated)" footer so the box stays bounded.
func clampLines(s string, width, maxRows int) string {
	if width < 1 {
		width = 1
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, maxRows+1)
	for _, ln := range lines {
		if len(out) >= maxRows {
			out = append(out, lipgloss.NewStyle().Foreground(colorMuted).Render("(… truncated)"))
			return strings.Join(out, "\n")
		}
		// lipgloss.Width counts visual cells; bufio-ish slicing on
		// rune index is close enough for ASCII-heavy tool inputs.
		// JSON tool inputs rarely include CJK; if they do, the wrap
		// is imperfect but never wrong (no garbled half-glyphs).
		for lipgloss.Width(ln) > width {
			cut := width
			if cut > len(ln) {
				cut = len(ln)
			}
			out = append(out, ln[:cut])
			ln = ln[cut:]
			if len(out) >= maxRows {
				out = append(out, lipgloss.NewStyle().Foreground(colorMuted).Render("(… truncated)"))
				return strings.Join(out, "\n")
			}
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// footerTip is the right-aligned line in the footer. Read-only mode
// surfaces a clear "read-only" marker; input mode surfaces the
// slash-command discoverability tip per the brief's "footer with
// keybind hints + slash-command discoverability".
func footerTip(readOnly bool) string {
	if readOnly {
		return "read-only"
	}
	return "type /help for commands"
}

func statusColor(k statusKind) lipgloss.Color {
	switch k {
	case statusError:
		return colorWarn
	case statusWarn:
		return colorWarn
	default:
		return colorAccent
	}
}

// rerenderViewport composes the transcript + live assistant text into
// the viewport's content. Called on every event-application, every
// text tick, and every View frame.
//
// Follow-the-tail behavior: if the viewport was at the bottom before
// content changed, we snap to the new bottom so streaming responses
// auto-scroll into view. If the user has scrolled up (pgup/wheel),
// we preserve their YOffset so they can read past content without
// the next textTick yanking them back down. Standard `tail -f` /
// `less +F` idiom.
func (m *Model) rerenderViewport() {
	if m.vp.Width == 0 {
		return
	}
	wasAtBottom := m.vp.AtBottom()
	var content string
	if len(m.transcript) == 0 && m.source.Get(m.agentID) == "" {
		// Fresh session or post-/clear. Render a centered welcome
		// instead of an empty viewport so the first frame feels
		// inviting and the user knows what to do.
		content = renderEmptyState(m.userName, m.vp.Width, m.vp.Height, m.readOnly)
	} else {
		var thinking string
		if m.isThinking() {
			thinking = renderThinkingRow(m.thinkingTick, m.thinkingElapsed(), m.vp.Width)
		}
		md := m.ensureMarkdown(m.vp.Width)
		content = composeTranscript(m.transcript, m.source.Get(m.agentID), thinking, md, m.childrenSnap, m.vp.Width)
	}
	m.vp.SetContent(content)
	if wasAtBottom {
		m.vp.GotoBottom()
	}
}

// ensureMarkdown returns the glamour renderer sized for width,
// rebuilding it whenever the viewport width changes. Returns nil on
// any setup error so the assistant-message renderer falls back to the
// plain avatar block — markdown is a nicety, not a hard requirement.
func (m *Model) ensureMarkdown(width int) *glamour.TermRenderer {
	if m.markdown != nil && m.markdownWidth == width {
		return m.markdown
	}
	r, err := newMarkdownRenderer(width)
	if err != nil {
		m.markdown = nil
		m.markdownWidth = width
		return nil
	}
	m.markdown = r
	m.markdownWidth = width
	return r
}

// renderEmptyState is the welcome panel shown when the transcript is
// empty. Centered cap + greeting + a few example prompts. Vertically
// padded so the content sits roughly in the middle of the viewport.
//
// readOnly tweaks the input hint - a viewer-only surface (manage
// preview, snapshot tests) should not imply input capability.
func renderEmptyState(userName string, width, height int, readOnly bool) string {
	if userName == "" {
		userName = "Boss"
	}
	accent := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	muted := lipgloss.NewStyle().Foreground(colorMuted)
	example := lipgloss.NewStyle().Foreground(colorSubtle).Italic(true)

	cap := accent.Render("🧢")
	greeting := cap + "  " + accent.Render("Hey "+userName+" - what are we working on?")
	hint := muted.Render("type a message below and hit enter")
	if readOnly {
		hint = muted.Render("(viewer mode - transcript is read-only)")
	}

	examples := []string{
		example.Render("• what's on my calendar today?"),
		example.Render("• summarize the last 5 commits"),
		example.Render("• find every TODO in this repo"),
		example.Render("• /help for slash commands"),
	}

	rows := []string{greeting, "", hint, "", ""}
	rows = append(rows, examples...)
	rows = append(rows, "", "", renderBetaBadge())

	// Center each row within the viewport width so the panel reads
	// as a single composed block rather than left-flushed text.
	for i, r := range rows {
		rows[i] = lipgloss.PlaceHorizontal(width, lipgloss.Center, r)
	}
	body := strings.Join(rows, "\n")

	// Vertical padding so the welcome sits ~40% from the top
	// (golden-ratio feel without being chrome-heavy on short windows).
	if height > len(rows)+2 {
		topPad := (height - len(rows)) / 3
		if topPad > 0 {
			body = strings.Repeat("\n", topPad) + body
		}
	}
	return body
}

// renderBetaBadge paints a small bordered chip under the example
// prompts that flags carlos as in-beta and points users at the
// GitHub issue tracker. The chip's foreground is colorWarn (the
// same yellow the approval surface uses for "pay attention") and
// the border is colorSubtle so the box reads as informational, not
// alarming. Composed as a single styled block so PlaceHorizontal
// centers it cleanly inside the surrounding column.
func renderBetaBadge() string {
	label := lipgloss.NewStyle().Foreground(colorWarn).Bold(true).Render("BETA")
	body := lipgloss.NewStyle().Foreground(colorMuted).Render(
		"found a bug? please open an issue at ",
	)
	url := lipgloss.NewStyle().Foreground(colorAccent).Underline(true).Render(
		"github.com/georgebuilds/carlos/issues",
	)
	line := label + lipgloss.NewStyle().Foreground(colorSubtle).Render("  ·  ") + body + url
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorSubtle).
		Padding(0, 2).
		Render(line)
}

// composeTranscript renders the transcript entries followed by the
// live assistant text (if any) and, if non-empty, an activity
// indicator row signalling "carlos is thinking between turns". Width
// is honored via lipgloss styles that wrap at the viewport width.
//
// snaps is the live sub-agent roster the agent-card body line consults
// to surface "running {tool}" while a sub-agent is in flight. nil or
// empty falls back to the static objective; only the agent-card
// renderer reads it.
//
// The thinking row is mutually exclusive with liveText in practice
// (isThinking returns false when source.Get returns text) but we
// don't enforce that here, the caller already decides. composeTranscript
// just renders what it's asked to.
func composeTranscript(entries []transcriptEntry, liveText, thinkingRow string, md *glamour.TermRenderer, snaps []ChildSnapshot, width int) string {
	var sb strings.Builder
	// Two-or-more consecutive entries of the same groupable kind
	// (entryToolCall, entryError) get folded into a single bordered
	// group with internal separators — Bootstrap list-group style.
	// Solo entries take the existing single-card path so the visual
	// language for one tool / one error is unchanged.
	//
	// Conversational turns (👤 and 🧢 avatar rows) get an extra leading
	// blank line so the transcript reads as a paced exchange instead of
	// a single dense block. The rule applies to the very first message
	// as well: the transcript opens with one blank line before the
	// first user/assistant entry to give it breathing room from the
	// surrounding chat chrome.
	wrote := false
	for i := 0; i < len(entries); {
		kind := entries[i].kind
		runEnd := i + 1
		// Sub-agent tool calls render as their own bordered card and
		// must not chain with other tool calls in a strip. They take
		// the single-entry path below regardless of neighbors.
		isAgentLead := kind == entryToolCall && entries[i].isAgent
		if isGroupableKind(kind) && !isAgentLead {
			for runEnd < len(entries) && entries[runEnd].kind == kind {
				// Hitting an agent entry hard-breaks the run so the
				// strip never absorbs sub-agent invocations. The next
				// outer loop iteration picks the agent entry up as its
				// own card and resumes strip grouping after it.
				if kind == entryToolCall && entries[runEnd].isAgent {
					break
				}
				runEnd++
			}
		}
		sb.WriteString(transcriptSeparator(wrote, wantsLeadingBlankLine(kind)))
		switch {
		case runEnd-i >= 2 && kind == entryToolCall:
			sb.WriteString(renderToolStrip(entries[i:runEnd], width))
		case runEnd-i >= 2 && kind == entryError:
			sb.WriteString(renderErrorCardGroup(entries[i:runEnd], width))
		default:
			sb.WriteString(renderEntry(entries[i], md, snaps, width))
			runEnd = i + 1 // single-entry path: advance by one
		}
		wrote = true
		i = runEnd
	}
	if liveText != "" {
		// Live streaming assistant text surfaces a 🧢 avatar too, so it
		// gets the same conversational-turn breathing room as a
		// committed assistant message.
		sb.WriteString(transcriptSeparator(wrote, true))
		sb.WriteString(renderAssistantLive(liveText, width))
		wrote = true
	}
	if thinkingRow != "" {
		// The thinking row is the agent's "I'm working on it" pulse -
		// visually a sibling of an assistant turn (same conversational
		// slot, same speaker). Give it the same blank-line breathing
		// room so it doesn't crowd whatever the user just sent.
		sb.WriteString(transcriptSeparator(wrote, true))
		sb.WriteString(thinkingRow)
	}
	return sb.String()
}

// wantsLeadingBlankLine reports whether an entry of this kind should
// be preceded by a blank line. Today that's just the two
// avatar-bearing turn types (user + assistant); tool strips, error
// cards, state notes, slash echoes, and shell rows all chain back-to-
// back with a single-line separator so the transcript stays compact
// when the agent is doing a long run of internal work.
func wantsLeadingBlankLine(k entryKind) bool {
	switch k {
	case entryUserMessage, entryAssistantMessage:
		return true
	}
	return false
}

// transcriptSeparator returns the right separator string for the next
// chunk in composeTranscript. The four states form a 2×2 matrix on
// (priorContentWritten, nextChunkWantsBlankLine):
//
//	prior=false  blank=false  →  ""        (first entry, no leading newline)
//	prior=false  blank=true   →  "\n"      (first entry is a turn, open with blank line)
//	prior=true   blank=false  →  "\n"      (normal one-line separator)
//	prior=true   blank=true   →  "\n\n"    (next turn after prior content gets breathing room)
//
// Centralising the logic here keeps composeTranscript readable and
// gives a single test target for the spacing contract.
func transcriptSeparator(priorContent, wantsBlank bool) string {
	if !priorContent {
		if wantsBlank {
			return "\n"
		}
		return ""
	}
	if wantsBlank {
		return "\n\n"
	}
	return "\n"
}

// isGroupableKind reports whether consecutive entries of the given
// kind should be folded into a single bordered group. Only tool calls
// and errors qualify today — user/assistant turns are conversational
// content (own avatar, own markdown rendering) and don't benefit from
// being boxed together.
func isGroupableKind(k entryKind) bool {
	switch k {
	case entryToolCall, entryError:
		return true
	}
	return false
}

// renderEntry styles one transcript entry. User and assistant turns use
// emoji avatars - 👤 for the user, 🧢 for carlos (the cap is the brand
// mark). Everything else (tool calls, steering, state notes) keeps a
// short text label since they're system-level annotations, not
// conversational turns.
//
// The "avatar : text" format reads the transcript like a chat log
// rather than a debug trace. snaps is the live sub-agent roster that
// the agent-card body line consults for the "running {tool}" signal;
// nil or empty falls back to the static objective.
func renderEntry(e transcriptEntry, md *glamour.TermRenderer, snaps []ChildSnapshot, width int) string {
	body := lipgloss.NewStyle().Width(width).MaxWidth(width)
	colon := lipgloss.NewStyle().Foreground(colorMuted).Render(":")
	switch e.kind {
	case entryUserMessage:
		return renderAvatarBlock("👤", colon, e.text, colorUser, width)
	case entryAssistantMessage:
		return renderAssistantMarkdown(e.text, width, md)
	case entryUserShell:
		// Phase U S5 block: $-prompt, output body, status badge.
		// Renderer is in internal/tui/chat/usershell_render.go.
		return body.Render(renderUserShellEntry(e, width))
	case entryToolCall:
		// Sub-agent calls peel off into their own bordered card,
		// spawning another carlos is a heavyweight action that
		// deserves more visual weight than the compact strip.
		if e.isAgent {
			return renderAgentCard(e, width, snaps)
		}
		// Activity strip (Concept A): single indented line in place of
		// the legacy bordered card. Composition lives in
		// activity_strip.go; a solo entry is just a 1-entry "group" so
		// the layout code stays in one place.
		return renderToolStrip([]transcriptEntry{e}, width)
	case entryToolResult:
		// Legacy entry kind. Pre-tool-card transcripts replayed from
		// disk may still hit this path. Render as an inline preview
		// for back-compat; new transcripts fold tool_result into
		// entryToolCall via Model.findLatestToolCall.
		arrowColor := colorMuted
		if e.isError {
			arrowColor = colorWarn
		}
		arrowStyle := lipgloss.NewStyle().Foreground(arrowColor)
		const maxRows = 5
		preview := previewLines(e.text, width-6, maxRows)
		if preview == "" {
			return body.Render(arrowStyle.Render("  ↪ (empty)"))
		}
		out := make([]string, 0, maxRows+1)
		for _, ln := range strings.Split(preview, "\n") {
			out = append(out, arrowStyle.Render("  ↪ ")+lipgloss.NewStyle().Foreground(arrowColor).Render(ln))
		}
		return body.Render(strings.Join(out, "\n"))
	case entrySteering:
		prefix := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("steer")
		return body.Render(prefix + " " + e.text)
	case entryStateChange:
		return body.Render(lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render("· " + e.text))
	case entrySystemNote:
		return body.Render(lipgloss.NewStyle().Foreground(colorWarn).Italic(true).Render("! " + e.text))
	case entrySlashEcho:
		prefix := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("›")
		text := lipgloss.NewStyle().Foreground(colorAccent).Render(e.text)
		return body.Render(prefix + " " + text)
	case entryError:
		return renderErrorCard(e, width)
	case entryResearchProgress:
		// Phase 11 slice 11e: live progress line for a /research sub-
		// agent. e.text is pre-formatted by formatResearchProgress so
		// the renderer only owns coloring. Failed runs use the warn
		// palette (red); in-flight + done use the muted-italic look
		// state-change rows use, since the row is a system annotation
		// rather than a conversational turn.
		color := colorMuted
		if e.isError {
			color = colorWarn
		}
		return body.Render(lipgloss.NewStyle().Foreground(color).Italic(true).Render(e.text))
	}
	return e.text
}

// renderAvatarBlock renders a chat message with a fixed-width avatar
// gutter: the avatar + ": " starts the first line; continuation lines
// indent by the same visual width so the body text aligns under
// itself. Without this, a wrapped assistant response shows row 2 flush
// to the left edge - it reads as a new speaker, not a continuation.
//
// Width math: avatar (typically 2 cells for an emoji) + ": " = 4
// visual cells. The text body wraps to width - indent; subsequent
// lines get `indent` spaces prepended so the gutter is preserved.
func renderAvatarBlock(avatar, colon, text string, textColor lipgloss.Color, width int) string {
	prefixPlain := avatar + ": "
	indent := lipgloss.Width(prefixPlain)
	wrapW := width - indent
	if wrapW < 10 {
		wrapW = 10
	}

	lines := wordWrap(text, wrapW)
	if len(lines) == 0 {
		// Empty body - still emit the avatar so the entry's existence
		// is visible. Shouldn't happen in practice (chat doesn't seal
		// empty turns) but the defensive render avoids a vanishing row.
		return avatar + colon
	}

	style := lipgloss.NewStyle().Foreground(textColor)
	prefix := avatar + colon + " "
	pad := strings.Repeat(" ", indent)

	out := make([]string, len(lines))
	out[0] = prefix + style.Render(lines[0])
	for i := 1; i < len(lines); i++ {
		out[i] = pad + style.Render(lines[i])
	}
	return strings.Join(out, "\n")
}

// wordWrap splits s into lines that visually fit width cells. Word
// boundaries are preserved where possible; a single word longer than
// width is hard-broken at the byte level (fine for ASCII-heavy chat
// content; CJK falls back to imperfect-but-never-garbled wrap).
//
// Embedded "\n" in s start a new paragraph that gets its own wrap pass
// - keeps explicit line breaks in the model's response intact.
func wordWrap(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, paragraph := range strings.Split(s, "\n") {
		if paragraph == "" {
			out = append(out, "")
			continue
		}
		var line strings.Builder
		for _, word := range strings.Fields(paragraph) {
			ww := lipgloss.Width(word)
			cur := lipgloss.Width(line.String())
			switch {
			case cur == 0:
				// First word on a fresh line - emit it; hard-break
				// only if it alone exceeds width.
				for ww > width {
					cut := width
					if cut > len(word) {
						cut = len(word)
					}
					out = append(out, word[:cut])
					word = word[cut:]
					ww = lipgloss.Width(word)
				}
				if ww > 0 {
					line.WriteString(word)
				}
			case cur+1+ww <= width:
				line.WriteByte(' ')
				line.WriteString(word)
			default:
				// Word would overflow the current line - flush, then
				// start fresh with the same hard-break logic.
				out = append(out, line.String())
				line.Reset()
				for ww > width {
					cut := width
					if cut > len(word) {
						cut = len(word)
					}
					out = append(out, word[:cut])
					word = word[cut:]
					ww = lipgloss.Width(word)
				}
				if ww > 0 {
					line.WriteString(word)
				}
			}
		}
		if line.Len() > 0 {
			out = append(out, line.String())
		}
	}
	return out
}

// renderAssistantLive is the streaming-assistant counterpart of
// renderEntry's entryAssistantMessage case. Same 🧢: <text> shape so a
// turn-pair reads as a single conversational beat - including the
// gutter so wrapped continuation lines align under the body, not
// flush-left.
func renderAssistantLive(text string, width int) string {
	colon := lipgloss.NewStyle().Foreground(colorMuted).Render(":")
	return renderAvatarBlock("🧢", colon, text, colorAgent, width)
}

// shortID truncates a ULID to the leading 8 chars for the header
// display. Full IDs are still available via inspection of the log.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
