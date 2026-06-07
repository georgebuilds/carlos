package chat

import (
	"fmt"
	"strings"

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

	// border.GetWidth() is Width set above (w - 2) — the inner area
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

	// Approval prompt OR frame switcher OR heuristic OR jobs overlay
	// OR help overlay is a bordered panel above the input. Compute
	// height first so we can reserve it from the transcript area.
	// They're mutually exclusive — approval is modal (model is
	// waiting), jobs / perms / help / switcher / heuristic are
	// dismiss-on-keypress; precedence: approval > switcher >
	// heuristic > jobs > perms > help.
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
	} else if m.showHeuristic {
		approval = renderHeuristicOverlay(
			m.heuristicPending,
			m.heuristicChecks,
			m.heuristicHelp,
			innerW,
		)
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
	m.vp.Width = innerW
	m.vp.Height = transcriptH
	m.rerenderViewport()

	parts := []string{header, m.vp.View()}
	if approval != "" {
		parts = append(parts, approval)
	}
	if !m.readOnly {
		parts = append(parts, input)
	}
	parts = append(parts, footer)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderInput frames the textarea with a thin separator above so the
// input row reads as visually distinct from the transcript above it.
func (m *Model) renderInput(w int) string {
	sep := lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", w))
	return sep + "\n" + m.ta.View()
}

// renderHeader shows the agent ID + state badge + model name + (Phase F)
// frame pill. State comes from the projection — single source of truth.
// Pill suppressed when no frame is wired (legacy single-shelf mode) so
// the header stays compatible with tests built before Phase F.
func (m *Model) renderHeader(w int) string {
	id := shortID(m.agentID)
	state, model := m.headerState()
	badge := stateBadge(state)
	idStyle := lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	modelStyle := lipgloss.NewStyle().Foreground(colorMuted)

	left := idStyle.Render(id) + " " + badge
	if model != "" {
		left += " " + modelStyle.Render("("+model+")")
	}
	if m.frame.Active != "" {
		left += " " + framePillSep + " " + framePill(m.frame)
		if mode := m.frame.Mode; mode != "" && mode != "solo" {
			left += " " + framePillSep + " " + lipgloss.NewStyle().Foreground(colorSubtle).Render(mode)
		}
	}
	right := lipgloss.NewStyle().Foreground(colorMuted).Render("carlos chat")

	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// framePillSep is the dim middle-dot we use everywhere else in the
// chrome (footer, status echo) so the header stays visually consistent.
var framePillSep = lipgloss.NewStyle().Foreground(colorSubtle).Render("·")

// framePill renders the active frame's glyph + name. Color comes from
// the curated palette in internal/frame.AccentColor.
func framePill(f FrameUI) string {
	return frame.Pill(f.Glyph, f.Active, f.Accent, isNoColor())
}

// isNoColor returns true when the lipgloss color profile is the empty
// (uncoloured) profile, which is how this codebase signals NO_COLOR /
// non-TTY. The frame.Pill helper takes a bool so it stays standalone.
func isNoColor() bool {
	// colorAccent is rendered by the theme package which already honours
	// NO_COLOR — if the accent has no foreground, we're monochrome.
	return colorAccent == ""
}

// headerState reads the projection for the active agent. If the agent
// isn't in the projection yet (no state_change seen), report a placeholder.
func (m *Model) headerState() (agent.State, string) {
	row, ok := m.proj.Get(m.agentID)
	if !ok {
		return agent.StateSpawning, ""
	}
	return row.State, row.Model
}

// stateBadge formats a state as a colored text label. Color choice
// signals priority per SPEC § "what the user monitors for".
//
// Slice 9c: the brackets now wrap a unicode glyph (theme.StateGlyph)
// plus the label. Color encodes priority; shape encodes identity. When
// NO_COLOR strips the foreground, the glyph alone still distinguishes
// states — same accessibility win as manage's roster badges.
func stateBadge(s agent.State) string {
	var color lipgloss.Color
	switch s {
	case agent.StateAwaitingInput, agent.StateBlocked, agent.StateOrphaned:
		color = colorWarn
	case agent.StateRunning, agent.StateCompacting:
		color = colorAgent
	case agent.StateDone:
		color = colorOK
	case agent.StateFailed:
		color = colorWarn
	default:
		color = colorMuted
	}
	return lipgloss.NewStyle().
		Foreground(color).
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
			keyStyle.Render("ctrl-c") + hintStyle.Render(" quit")
	} else {
		hints = keyStyle.Render("enter") + hintStyle.Render(" send  ") +
			keyStyle.Render("shift-enter") + hintStyle.Render(" newline  ") +
			keyStyle.Render("pgup/pgdn") + hintStyle.Render(" scroll  ") +
			keyStyle.Render("ctrl-c") + hintStyle.Render(" quit")
	}

	// User-shell footer hint takes priority over the right-aligned
	// /help tip. Idle state returns empty so we keep the existing
	// tip behavior.
	shellHint := renderUserShellFooter(m.computeUserShellFooterContext())
	// Drop the right-aligned tip when there isn't enough room left
	// after the keybind hints — wrapping it onto a second row pushes
	// the leftover word ("commands") flush-left, which reads worse
	// than just hiding the tip. The hints alone are the discoverable
	// surface; /help is already there for the user who needs it.
	//
	// User-shell hint, when present, replaces the tip — actionable
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
// input separator (see renderInner). Compact layout — three rows
// inside the box so the transcript above stays usable:
//
//	🧢 wants to run `bash`
//	    {"cmd":"ls -la"}
//	    [y] yes   [n] no   [A] always for bash    (esc denies)
//
// Width tracks the inner chat box minus the box's own border
// (Border = 2 cols; Padding = 0 — vertical real estate is precious
// when the transcript shares the screen).
// renderToolCard is the bordered tool-call card. Single header row
// inside a rounded box, in the tool's accent color (warn if errored):
//
//	┌────────────────────────────────────────────────┐
//	│ 🔧 bash · ls -la ~/Desktop · 20 lines          │
//	└────────────────────────────────────────────────┘
//
// Composition: glyph + tool name + middle-dot + one-line input
// preview + right-aligned status suffix (line count / "error" /
// "running…"). The actual output is intentionally hidden — the model
// already saw it; the user gets a summary. An expand keybind is a
// future slice.
func renderToolCard(e transcriptEntry, width int) string {
	glyph := "🔧"
	borderColor := colorTool
	if e.isError {
		glyph = "✗"
		borderColor = colorWarn
	}

	// Inset so the card sits in the same column as conversation
	// text: 4 cells of left indent (matches the avatar gutter on
	// 👤/🧢 messages) and a symmetric 4 cells of right margin.
	// Total visual width of the card = width - sideMargin*2.
	const sideMargin = 4
	totalW := width - sideMargin*2
	if totalW < 30 {
		totalW = 30
	}
	// lipgloss: Width = content area incl. padding; Border adds 2
	// more. Subtract the border to land on the requested totalW.
	boxW := totalW - 2
	// Inside the box: padding 0,1 leaves boxW-2 cells for content.
	contentW := boxW - 2
	if contentW < 20 {
		contentW = 20
	}

	gear := lipgloss.NewStyle().Foreground(borderColor).Render(glyph)
	name := lipgloss.NewStyle().Foreground(borderColor).Bold(true).Render(e.tool)
	sepStyle := lipgloss.NewStyle().Foreground(colorMuted)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)

	head := gear + " " + name

	// Status suffix sits right-aligned.
	status := toolCardStatus(e)
	statusRender := mutedStyle.Render(status)
	statusW := lipgloss.Width(statusRender)

	// One-line input preview between head and status. Cap so it
	// never starves the status suffix of room.
	var preview string
	if e.toolInput != "" {
		maxInputW := contentW - lipgloss.Width(head) - statusW - 6 // 6 = " · " + "  " gap
		if maxInputW >= 10 {
			preview = sepStyle.Render(" · ") + mutedStyle.Render(oneLine(e.toolInput, maxInputW))
		}
	}

	headSection := head + preview
	gap := contentW - lipgloss.Width(headSection) - statusW
	if gap < 1 {
		gap = 1
	}
	line := headSection + strings.Repeat(" ", gap) + statusRender

	rendered := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1).
		Width(boxW).
		Render(line)

	// Prepend the side margin to every row so the card sits indented
	// under the conversation column instead of flush-left. lipgloss's
	// MarginLeft would have worked but adds its own margin char which
	// can interact oddly with the rounded border on some terminals;
	// manual padding is bulletproof.
	pad := strings.Repeat(" ", sideMargin)
	rows := strings.Split(rendered, "\n")
	for i := range rows {
		rows[i] = pad + rows[i]
	}
	return strings.Join(rows, "\n")
}

// toolCardStatus derives the right-aligned status suffix from the
// entry's result state:
//
//	hasResult=false           → "running…"   (event log mid-replay)
//	hasResult=true, isError   → "error"
//	hasResult=true, empty     → "no output"
//	hasResult=true, !empty    → "<N> lines"
func toolCardStatus(e transcriptEntry) string {
	if !e.hasResult {
		return "running…"
	}
	if e.isError {
		return "error"
	}
	trimmed := strings.TrimRight(e.toolResult, "\n")
	if trimmed == "" {
		return "no output"
	}
	n := strings.Count(trimmed, "\n") + 1
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

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
// — no separate doc table to keep in sync. Two-column layout: name +
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
	maxLeft := 0
	rows := make([][2]string, 0, len(slash.Builtins))
	for _, b := range slash.Builtins {
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
// as their first line (commonly the most informative — the bash cmd,
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
	// Visual-cell-aware truncation — char index is close enough for
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
		content = composeTranscript(m.transcript, m.source.Get(m.agentID), m.vp.Width)
	}
	m.vp.SetContent(content)
	if wasAtBottom {
		m.vp.GotoBottom()
	}
}

// renderEmptyState is the welcome panel shown when the transcript is
// empty. Centered cap + greeting + a few example prompts. Vertically
// padded so the content sits roughly in the middle of the viewport.
//
// readOnly tweaks the input hint — a viewer-only surface (manage
// preview, snapshot tests) should not imply input capability.
func renderEmptyState(userName string, width, height int, readOnly bool) string {
	if userName == "" {
		userName = "Boss"
	}
	accent := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	muted := lipgloss.NewStyle().Foreground(colorMuted)
	example := lipgloss.NewStyle().Foreground(colorSubtle).Italic(true)

	cap := accent.Render("🧢")
	greeting := cap + "  " + accent.Render("Hey "+userName+" — what are we working on?")
	hint := muted.Render("type a message below and hit enter")
	if readOnly {
		hint = muted.Render("(viewer mode — transcript is read-only)")
	}

	examples := []string{
		example.Render("• what's on my calendar today?"),
		example.Render("• summarize the last 5 commits"),
		example.Render("• find every TODO in this repo"),
		example.Render("• /help for slash commands"),
	}

	rows := []string{greeting, "", hint, "", ""}
	rows = append(rows, examples...)

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

// composeTranscript renders the transcript entries followed by the
// live assistant text (if any). Width is honored via lipgloss styles
// that wrap at the viewport width.
func composeTranscript(entries []transcriptEntry, liveText string, width int) string {
	var sb strings.Builder
	for i, e := range entries {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(renderEntry(e, width))
	}
	if liveText != "" {
		if len(entries) > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(renderAssistantLive(liveText, width))
	}
	return sb.String()
}

// renderEntry styles one transcript entry. User and assistant turns use
// emoji avatars — 👤 for the user, 🧢 for carlos (the cap is the brand
// mark). Everything else (tool calls, steering, state notes) keeps a
// short text label since they're system-level annotations, not
// conversational turns.
//
// The "avatar : text" format reads the transcript like a chat log
// rather than a debug trace.
func renderEntry(e transcriptEntry, width int) string {
	body := lipgloss.NewStyle().Width(width).MaxWidth(width)
	colon := lipgloss.NewStyle().Foreground(colorMuted).Render(":")
	switch e.kind {
	case entryUserMessage:
		return renderAvatarBlock("👤", colon, e.text, colorUser, width)
	case entryAssistantMessage:
		return renderAvatarBlock("🧢", colon, e.text, colorAgent, width)
	case entryUserShell:
		// Phase U S5 block: $-prompt, output body, status badge.
		// Renderer is in internal/tui/chat/usershell_render.go.
		return body.Render(renderUserShellEntry(e, width))
	case entryToolCall:
		// Bordered tool card (collapsed-by-default). Combines the
		// preceding tool_call + the folded-in tool_result into a
		// single insertion. Format:
		//
		//   ┌─────────────────────────────────────────────┐
		//   │ 🔧 bash · ls -la ~/Desktop · 20 lines       │
		//   └─────────────────────────────────────────────┘
		//
		// The output stays off-screen by default — the model already
		// saw the full bytes; the user gets the summary. Expand
		// keybind is a future slice; for now the card is the whole
		// surface.
		return renderToolCard(e, width)
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
// to the left edge — it reads as a new speaker, not a continuation.
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
		// Empty body — still emit the avatar so the entry's existence
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
// — keeps explicit line breaks in the model's response intact.
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
				// First word on a fresh line — emit it; hard-break
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
				// Word would overflow the current line — flush, then
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
// turn-pair reads as a single conversational beat — including the
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
