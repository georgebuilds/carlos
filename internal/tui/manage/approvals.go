package manage

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
)

// Approval pane — surfaces Phase 4h's pending-approval queue inside the
// manage TUI. The same agent.ListPendingApprovals API the `carlos
// approvals` CLI uses; user navigates with arrows, accepts with `y`,
// rejects with `r` (which opens a reason-input overlay first).
//
// Sits as a sibling "view" alongside the roster view. The model's
// viewMode field selects which one renders; `A` (capital) toggles.

// viewMode selects which of the manage TUI's panes is currently in
// focus. viewRoster is the existing roster + focus-pane split; the
// approval pane takes over the body when viewMode == viewApprovals.
type viewMode int

const (
	viewRoster viewMode = iota
	viewApprovals
)

// approvalLogReader is the read surface the pane consumes from the
// agent.EventLog. The agent.EventLog interface doesn't expose a typed
// "Pending" method (it lives in the concrete SQLiteEventLog through
// the agent.ListPendingApprovals helper), so the pane takes a
// function-typed dependency to keep tests easy + decouple from the
// concrete log type.
type approvalLister func(ctx context.Context) ([]agent.PendingApproval, error)
type approvalResolver func(ctx context.Context, artifactID, note string, accept bool) error

// approvalsPane is the local state the pane owns: cursor, the latest
// fetched list, the last-refresh time so the pane can show "as of
// HH:MM:SS", and a transient status echo.
type approvalsPane struct {
	cursor   int
	pending  []agent.PendingApproval
	fetched  time.Time
	fetchErr string
}

// fetchApprovalsMsg is delivered to Update when a list-pending call
// completes. The pane swaps in the fresh list + cursor clamp.
type fetchApprovalsMsg struct {
	pending []agent.PendingApproval
	err     error
	t       time.Time
}

// fetchApprovalsCmd returns a tea.Cmd that runs the lister in a
// goroutine and emits a fetchApprovalsMsg. Used both on entering the
// pane and after a successful accept/reject.
func fetchApprovalsCmd(lister approvalLister) tea.Cmd {
	if lister == nil {
		return func() tea.Msg {
			return fetchApprovalsMsg{err: fmtErr("no approval log wired"), t: time.Now()}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		got, err := lister(ctx)
		return fetchApprovalsMsg{pending: got, err: err, t: time.Now()}
	}
}

// applyFetch swaps in a fresh list + clamps the cursor.
func (p *approvalsPane) applyFetch(msg fetchApprovalsMsg) {
	if msg.err != nil {
		p.fetchErr = msg.err.Error()
		return
	}
	p.fetchErr = ""
	p.pending = msg.pending
	p.fetched = msg.t
	if p.cursor >= len(p.pending) {
		p.cursor = len(p.pending) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// selected returns the pending row under the cursor, or (zero, false)
// if the queue is empty.
func (p *approvalsPane) selected() (agent.PendingApproval, bool) {
	if len(p.pending) == 0 {
		return agent.PendingApproval{}, false
	}
	if p.cursor < 0 || p.cursor >= len(p.pending) {
		return agent.PendingApproval{}, false
	}
	return p.pending[p.cursor], true
}

// moveCursor clamps cursor delta into [0, len(pending)).
func (p *approvalsPane) moveCursor(delta int) {
	if len(p.pending) == 0 {
		p.cursor = 0
		return
	}
	p.cursor += delta
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.cursor >= len(p.pending) {
		p.cursor = len(p.pending) - 1
	}
}

// render produces the pane's body (called from view.go when the active
// view is viewApprovals). Width/height come from the manage body box.
//
// Layout:
//
//	header line: "Approvals — N pending — as of HH:MM:SS"
//	(optional fetch-error line in warn color)
//	[for each pending, oldest first]:
//	  > <id-short>  <age>  [<kind>]  <title>
//	     <agent_id_short>  <path>  <size>
//	footer hint: "y accept · r reject · A back · ↑/↓ navigate"
func (p *approvalsPane) render(w, h int) string {
	var sb strings.Builder

	header := lipgloss.NewStyle().Bold(true).Foreground(colorAccent).Render(
		fmt.Sprintf("Approvals — %d pending", len(p.pending)),
	)
	if !p.fetched.IsZero() {
		header += lipgloss.NewStyle().Foreground(colorSubtle).Render(
			"  · as of " + p.fetched.Local().Format("15:04:05"))
	}
	sb.WriteString(header)
	sb.WriteString("\n")

	if p.fetchErr != "" {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorWarn).Render("fetch error: " + p.fetchErr))
		sb.WriteString("\n")
	}

	if len(p.pending) == 0 {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render(
			"\n(no pending approvals — induced skill proposals, plan diffs, and " +
				"research outputs that need review surface here)"))
		return lipgloss.NewStyle().Width(w).Height(h).Render(sb.String())
	}

	sb.WriteString("\n")
	for i, item := range p.pending {
		marker := "  "
		titleStyle := lipgloss.NewStyle().Foreground(colorAgent)
		if i == p.cursor {
			marker = lipgloss.NewStyle().Bold(true).Foreground(colorAccent).Render("▸ ")
			titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
		}
		age := humanAge(time.Since(item.ProposedAt))
		kindBadge := lipgloss.NewStyle().Foreground(colorWarn).Render("[" + item.Ref.Kind + "]")
		idShort := shortID(item.Ref.ID)
		ageStr := lipgloss.NewStyle().Foreground(colorMuted).Render(age)

		line1 := fmt.Sprintf("%s%s  %s  %s  %s",
			marker,
			lipgloss.NewStyle().Foreground(colorSubtle).Render(idShort),
			ageStr,
			kindBadge,
			titleStyle.Render(item.Title),
		)
		sb.WriteString(line1)
		sb.WriteString("\n")

		line2 := lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("       producer=%s  path=%s  size=%d",
				shortID(item.AgentID), item.Ref.Path, item.Ref.Size),
		)
		sb.WriteString(line2)
		sb.WriteString("\n\n")

		// Stop if we've used the available height.
		if lipgloss.Height(sb.String()) >= h-2 {
			sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
				fmt.Sprintf("  …%d more (cursor scroll TBD)", len(p.pending)-i-1)))
			break
		}
	}

	return lipgloss.NewStyle().Width(w).Height(h).Render(sb.String())
}

// (shortID lives in styles.go — shared with the roster.)

// humanAge renders a duration as a short relative string: "5s", "12m",
// "3h", "2d". Matches the roster's age column conventions.
func humanAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// fmtErr is a tiny helper so we don't pull fmt into every file just
// for the no-lister case. (Inline string + Errorf via fmt would also
// work; this keeps the imports tight in tests that mock the lister.)
func fmtErr(s string) error {
	return errString(s)
}

type errString string

func (e errString) Error() string { return string(e) }
