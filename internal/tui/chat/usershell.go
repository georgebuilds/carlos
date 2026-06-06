package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/usershell"
)

// User-shell glue (Phase U S4).
//
// This file is the chat surface's adapter for internal/usershell. It
// owns:
//
//   - "!" prefix detection at submit time
//   - The 4-state composer footer (idle / typing-shell / fg-running /
//     bg-only) per the TUI research's always-visible-help principle
//   - The tea.Cmd pump that turns usershell.Manager Subscribe updates
//     into tea.Msg events the chat Update loop can process
//   - Ctrl+Enter for "submit this as a background job"
//   - Esc for "cancel the running foreground job"
//
// Transcript rendering for completed jobs is S5 — this file handles
// the composer/footer half of the feature.

// userShellPrefix is the single ASCII byte that flips a submission
// from "send to the model" to "run as a shell command". Matches
// codex / opencode convention.
const userShellPrefix = '!'

// (shortIDLen removed — we reuse view.go's existing shortID helper
// which returns the leading 8 chars of a ULID. Compact enough for a
// footer; consistent with how the rest of the chat surface labels
// agents.)

// isShellSubmission reports whether raw is a "!<cmd>" submission
// with a non-empty command body. Bare "!" or "! " is the typing-
// state — handled by the footer for visual feedback, but submit
// rejects it as empty so the user isn't surprised by an "empty
// command" job.
func isShellSubmission(raw string) bool {
	trimmed := strings.TrimLeft(raw, " \t")
	if len(trimmed) < 2 || trimmed[0] != userShellPrefix {
		return false
	}
	return strings.TrimSpace(trimmed[1:]) != ""
}

// hasShellPrefix reports whether raw is at least a "!" (with or
// without a command following it). Used by the footer-state
// detector — a bare "!" still flips the footer into shell-mode so
// the user knows what's about to happen.
func hasShellPrefix(raw string) bool {
	trimmed := strings.TrimLeft(raw, " \t")
	return len(trimmed) >= 1 && trimmed[0] == userShellPrefix
}

// extractShellCommand strips the leading "!" (and any whitespace
// between the bang and the command) and returns the command body.
// Caller has already confirmed via isShellSubmission that the input
// qualifies.
func extractShellCommand(raw string) string {
	trimmed := strings.TrimLeft(raw, " \t")
	if len(trimmed) == 0 || trimmed[0] != userShellPrefix {
		return ""
	}
	return strings.TrimSpace(trimmed[1:])
}

// userShellUpdateMsg carries a single usershell.Update from the
// Manager's Subscribe channel into the chat Update loop. The pump
// goroutine re-arms after each delivery so a stream of updates
// flows through bubbletea's normal Msg dispatch without spawning
// per-update goroutines.
type userShellUpdateMsg struct {
	u usershell.Update
}

// pumpUserShellCmd returns a Cmd that blocks on one Subscribe
// channel read + emits the result as a userShellUpdateMsg. Update
// re-issues this Cmd after handling the message so the loop self-
// sustains while the channel is open.
func pumpUserShellCmd(ch <-chan usershell.Update) tea.Cmd {
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return userShellSubscriptionClosedMsg{}
		}
		return userShellUpdateMsg{u: u}
	}
}

// userShellSubscriptionClosedMsg flags the Subscribe channel as
// drained. The Update loop responds by clearing m.userShellSubCh so
// no further pumps are scheduled. Practically this only fires on
// Manager.Close (chat exit) — at which point the model is also
// about to quit, so the message is largely informational.
type userShellSubscriptionClosedMsg struct{}

// submitUserShellCmd hands cmd to the Manager + clears the textarea.
// The returned Cmd may emit an error message if Submit rejects the
// input (empty command, manager closed). State and output updates
// land asynchronously via the Subscribe pump.
//
// Also records the command in the shell-history file (Phase U S7) so
// ↑/↓ in shell mode can recall it next time. History writes are
// best-effort — a disk error doesn't fail the submit.
func (m *Model) submitUserShellCmd(cmd string, mode usershell.Mode) tea.Cmd {
	if m.usershell == nil {
		return func() tea.Msg {
			return statusMsg{
				text: "shell mode is not wired in this session",
				kind: statusWarn,
			}
		}
	}
	// Phase F-8: intercept `cd <path>` so the cwd persists across
	// foreground jobs AND so the hint check has a reliable trigger.
	// Compound commands and shell metacharacters fall through to the
	// shell so the user's pipeline still runs.
	if handled, msg := m.tryInterceptCdCommand(cmd); handled {
		if m.shellHistory != nil {
			_ = m.shellHistory.Add(cmd)
			m.shellHistory.Reset()
		}
		return statusCmd(msg, statusInfo)
	}
	if m.shellHistory != nil {
		_ = m.shellHistory.Add(cmd)
		m.shellHistory.Reset()
	}
	mgr := m.usershell
	return func() tea.Msg {
		// 2s budget — Submit is non-blocking; the timeout only
		// matters if the underlying DB write hangs.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		job, err := mgr.Submit(ctx, cmd, mode)
		if err != nil {
			return statusMsg{
				text: fmt.Sprintf("shell: %v", err),
				kind: statusWarn,
			}
		}
		modeWord := "fg"
		if mode == usershell.Background {
			modeWord = "bg"
		}
		return statusMsg{
			text: fmt.Sprintf("shell: queued j%s (%s) — %s", shortID(job.ID), modeWord, cmd),
			kind: statusInfo,
		}
	}
}

// cancelForegroundCmd asks the Manager to cancel whatever is in the
// fg slot right now. Used by the Esc / Ctrl+C handler when a fg
// shell job is running. Returns nil when there's nothing to cancel.
func (m *Model) cancelForegroundCmd() tea.Cmd {
	if m.usershell == nil {
		return nil
	}
	mgr := m.usershell
	// Snapshot under no lock — Jobs() returns a copy.
	for _, snap := range mgr.Jobs() {
		if snap.State == usershell.StateRunning && !snap.Backgrounded {
			id := snap.ID
			return func() tea.Msg {
				if err := mgr.Cancel(id); err != nil {
					return statusMsg{
						text: fmt.Sprintf("shell: cancel %s: %v", shortID(id), err),
						kind: statusWarn,
					}
				}
				return statusMsg{
					text: fmt.Sprintf("shell: cancelled j%s", shortID(id)),
					kind: statusInfo,
				}
			}
		}
	}
	return nil
}

// backgroundRunningCmd moves the current fg job to the bg pool.
// Mirrors shell ^Z. Returns nil when there's no fg job.
func (m *Model) backgroundRunningCmd() tea.Cmd {
	if m.usershell == nil {
		return nil
	}
	mgr := m.usershell
	for _, snap := range mgr.Jobs() {
		if snap.State == usershell.StateRunning && !snap.Backgrounded {
			id := snap.ID
			return func() tea.Msg {
				if err := mgr.Background(id); err != nil {
					return statusMsg{
						text: fmt.Sprintf("shell: bg %s: %v", shortID(id), err),
						kind: statusWarn,
					}
				}
				return statusMsg{
					text: fmt.Sprintf("shell: j%s moved to background", shortID(id)),
					kind: statusInfo,
				}
			}
		}
	}
	return nil
}

// userShellFooterState enumerates the four footer modes from the
// research note + roadmap. Order is precedence: typingShell wins
// over fgRunning wins over bgOnly wins over idle.
type userShellFooterState int

const (
	userShellFooterIdle userShellFooterState = iota
	userShellFooterTypingShell
	userShellFooterFgRunning
	userShellFooterBgOnly
)

// userShellFooterContext snapshots the data the footer needs.
// Computed once per render so the multi-branch hint logic stays
// readable.
type userShellFooterContext struct {
	state       userShellFooterState
	input       string // current textarea contents (trimmed-left)
	fgJob       *usershell.Snapshot
	bgCount     int
	queueCount  int
	hasShellMgr bool
}

// computeUserShellFooterContext inspects the textarea + Manager and
// returns the rendering inputs. Pure function — no side effects,
// safe to call per frame.
func (m *Model) computeUserShellFooterContext() userShellFooterContext {
	ctx := userShellFooterContext{
		state:       userShellFooterIdle,
		hasShellMgr: m.usershell != nil,
	}
	if !ctx.hasShellMgr {
		return ctx
	}
	input := strings.TrimLeft(m.ta.Value(), " \t")
	ctx.input = input

	var fg *usershell.Snapshot
	bgCount := 0
	queueCount := 0
	for _, snap := range m.usershell.Jobs() {
		s := snap
		switch s.State {
		case usershell.StateRunning:
			if s.Backgrounded {
				bgCount++
			} else {
				fg = &s
			}
		case usershell.StatePending:
			queueCount++
		}
	}
	ctx.fgJob = fg
	ctx.bgCount = bgCount
	ctx.queueCount = queueCount

	switch {
	case hasShellPrefix(input):
		ctx.state = userShellFooterTypingShell
	case fg != nil:
		ctx.state = userShellFooterFgRunning
	case bgCount > 0:
		ctx.state = userShellFooterBgOnly
	default:
		ctx.state = userShellFooterIdle
	}
	return ctx
}

// renderUserShellFooter produces the per-state hint line. Returns
// the empty string when the footer should fall through to the
// default chat hints (idle state OR no Manager wired). Caller is
// responsible for combining this with the default keybind row +
// status echo.
//
// Color discipline matches the TUI research's "semantic color" rule:
// brand accent for keys + job ids, muted gray for body copy, no
// decorative color.
func renderUserShellFooter(ctx userShellFooterContext) string {
	if !ctx.hasShellMgr || ctx.state == userShellFooterIdle {
		return ""
	}
	keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	jobStyle := lipgloss.NewStyle().Foreground(colorAccent)
	bodyStyle := lipgloss.NewStyle().Foreground(colorMuted)

	switch ctx.state {
	case userShellFooterTypingShell:
		// Footer hint while the user is mid-keystroke.
		if strings.TrimSpace(ctx.input) == "!" {
			return bodyStyle.Render("shell · ") +
				bodyStyle.Render("type a command, or empty for jobs view")
		}
		return bodyStyle.Render("shell · ") +
			keyStyle.Render("enter") + bodyStyle.Render(" run · ") +
			keyStyle.Render("⌃enter") + bodyStyle.Render(" background · ") +
			keyStyle.Render("esc") + bodyStyle.Render(" clear")

	case userShellFooterFgRunning:
		fg := ctx.fgJob
		dur := fg.Duration()
		var sb strings.Builder
		sb.WriteString(jobStyle.Render("j" + shortID(fg.ID)))
		sb.WriteString(bodyStyle.Render(" "))
		sb.WriteString(bodyStyle.Render(truncateOneLine(fg.Command, 30)))
		sb.WriteString(bodyStyle.Render(" · "))
		sb.WriteString(bodyStyle.Render(formatDuration(dur)))
		sb.WriteString(bodyStyle.Render(" · "))
		sb.WriteString(keyStyle.Render("⌃c"))
		sb.WriteString(bodyStyle.Render(" cancel · "))
		sb.WriteString(keyStyle.Render("⌃z"))
		sb.WriteString(bodyStyle.Render(" bg"))
		if ctx.queueCount > 0 {
			sb.WriteString(bodyStyle.Render(fmt.Sprintf(" · %d queued", ctx.queueCount)))
		}
		if ctx.bgCount > 0 {
			sb.WriteString(bodyStyle.Render(fmt.Sprintf(" · %d bg", ctx.bgCount)))
		}
		return sb.String()

	case userShellFooterBgOnly:
		return bodyStyle.Render(fmt.Sprintf("%d bg job", ctx.bgCount)) +
			mutedPlural(ctx.bgCount) +
			bodyStyle.Render(" · ") +
			keyStyle.Render("⌃j") + bodyStyle.Render(" list · ") +
			bodyStyle.Render("!fg j<id> attach")
	}
	return ""
}

// mutedPlural wraps view.go's plural helper in muted color so the
// "s" in "2 bg jobs" stays in the same neutral palette as the rest
// of the user-shell footer copy.
func mutedPlural(n int) string {
	return lipgloss.NewStyle().Foreground(colorMuted).Render(plural(n))
}

// formatDuration emits a compact "<n.n>s" / "<n>m<n>s" form for
// footer rendering. Sub-second durations show as "0.Xs" so a
// just-started job doesn't read as "0s".
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	mins := int(d / time.Minute)
	secs := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%ds", mins, secs)
}

// truncateOneLine clips s to at most max runes, replacing the tail
// with an ellipsis. Single-line only — newlines collapse to spaces
// because we're rendering a one-line footer. max <= 0 returns s
// unchanged.
func truncateOneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if max <= 0 {
		return s
	}
	count := 0
	for range s {
		count++
		if count > max {
			break
		}
	}
	if count <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	keep := max - 1
	i := 0
	idx := 0
	for byteIdx := range s {
		if i == keep {
			idx = byteIdx
			break
		}
		i++
	}
	return s[:idx] + "…"
}
