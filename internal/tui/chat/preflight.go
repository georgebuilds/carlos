package chat

// preflight.go - the interactive Clarify -> Brief -> Proceed pre-flight
// for `/research` (roadmap slice 11k, TUI only).
//
// Today `/research <q>` fans out the six-phase pipeline immediately.
// 11k inserts an interactive pre-flight BEFORE the pipeline runs:
//
//  1. Clarify: the model proposes 0-2 short clarifying questions. The
//     user answers each in the composer (Enter advances). A specific
//     question yields zero questions and the flow skips to the brief.
//  2. Brief: the question plus answers fold into a one-paragraph BRIEF,
//     preloaded into the composer for the user to review and EDIT.
//  3. Proceed: Enter on the brief launches the existing research path
//     with the refined brief as the question. Esc cancels with a
//     status line; nothing runs.
//
// Both model calls (clarify, brief) run as a tea.Cmd so Update never
// blocks; a lightweight thinking affordance animates while we wait. The
// pre-flight is TUI-only: the headless `carlos research` path never
// constructs a Model, so it naturally skips this.
//
// Design language (sandlot sketchbook, per George): dashed rules, a dim
// italic corner tag, NO left color stripe / no bordered panel. No
// em-dashes in any user-facing string.

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/research"
)

// preflightMode is the pre-flight state machine. preflightOff is the
// resting state; the two active modes own the keyboard while up.
type preflightMode int

const (
	preflightOff preflightMode = iota
	// preflightClarify presents one clarifying question at a time; the
	// composer captures the answer, Enter advances.
	preflightClarify
	// preflightBrief shows the editable brief preloaded into the
	// composer; Enter launches research, Esc cancels.
	preflightBrief
)

// preflightTimeout caps a single clarify/brief model call. These are
// short single-shot completions; a minute is generous headroom and
// keeps a hung provider from parking the pre-flight forever.
const preflightTimeout = 60 * time.Second

// WithPreflight wires the provider + model the `/research` pre-flight
// uses for its Clarify -> Brief model calls (slice 11k). When unset (or
// when the research engine itself is unwired), `/research` skips the
// pre-flight and dispatches straight through the existing path. The
// production wire-up passes the same provider + model the chat's own
// dispatch uses.
func WithPreflight(provider research.Preflight) Option {
	return func(m *Model) {
		if provider.Provider != nil {
			m.preflight = &provider
		}
	}
}

// clarifyResultMsg carries the outcome of the async ClarifyQuestions
// call back into Update.
type clarifyResultMsg struct {
	question  string
	questions []string
	err       error
}

// briefResultMsg carries the outcome of the async Brief call back into
// Update.
type briefResultMsg struct {
	question string
	brief    string
	err      error
}

// parseResearchArgs splits the `/research` argument string into the
// research question and a noClarify flag. The "--no-clarify" token (in
// any position) is stripped from the question and flips the flag; the
// remaining tokens are rejoined as the question. This lets the user opt
// out of the pre-flight inline: `/research --no-clarify <q>`.
func parseResearchArgs(args string) (question string, noClarify bool) {
	fields := strings.Fields(args)
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		if f == "--no-clarify" {
			noClarify = true
			continue
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " "), noClarify
}

// preflightActive reports whether the pre-flight owns the keyboard +
// the takeover slot. True in either active mode OR while an async
// clarify/brief call is in flight (so the thinking affordance shows and
// stray keys are swallowed).
func (m *Model) preflightActive() bool {
	return m.preflightMode != preflightOff || m.preflightAwaiting
}

// startPreflight kicks off the pre-flight for question. It resets any
// stale state, records the original question, marks the call in flight,
// and returns the clarify command batched with a text tick so the
// thinking affordance animates while we wait. Callers gate on a non-nil
// m.preflight before calling.
func (m *Model) startPreflight(question string) tea.Cmd {
	m.preflightMode = preflightOff
	m.preflightAwaiting = true
	m.preflightQuestion = question
	m.preflightQuestions = nil
	m.preflightIndex = 0
	m.preflightAnswers = nil
	m.preflightBriefDraft = ""
	m.status = ""
	m.rerenderViewport()
	return tea.Batch(m.clarifyCmd(question), scheduleTextTick())
}

// clarifyCmd runs ClarifyQuestions off the Update goroutine and returns
// a clarifyResultMsg. The provider call is the only blocking work; the
// timeout fences a hung provider.
func (m *Model) clarifyCmd(question string) tea.Cmd {
	pf := m.preflight
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), preflightTimeout)
		defer cancel()
		qs, err := pf.ClarifyQuestions(ctx, question)
		return clarifyResultMsg{question: question, questions: qs, err: err}
	}
}

// briefCmd runs Brief off the Update goroutine and returns a
// briefResultMsg.
func (m *Model) briefCmd(question string, answers []string) tea.Cmd {
	pf := m.preflight
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), preflightTimeout)
		defer cancel()
		brief, err := pf.Brief(ctx, question, answers)
		return briefResultMsg{question: question, brief: brief, err: err}
	}
}

// handleClarifyResult processes the clarify call outcome. On error OR
// zero questions it proceeds straight to the brief (clarification is a
// best-effort sharpening step, not a gate). Otherwise it enters clarify
// mode and presents the first question.
func (m *Model) handleClarifyResult(msg clarifyResultMsg) tea.Cmd {
	if msg.err != nil || len(msg.questions) == 0 {
		// Skip clarify: go straight to the brief with no answers. A
		// clarify failure should never block the user from researching.
		m.preflightAnswers = nil
		return m.requestBrief()
	}
	m.preflightAwaiting = false
	m.preflightMode = preflightClarify
	m.preflightQuestions = msg.questions
	m.preflightIndex = 0
	m.preflightAnswers = make([]string, 0, len(msg.questions))
	m.resetComposer()
	m.rerenderViewport()
	return nil
}

// requestBrief marks the brief call in flight and returns the brief
// command batched with a text tick for the thinking affordance.
func (m *Model) requestBrief() tea.Cmd {
	m.preflightMode = preflightOff
	m.preflightAwaiting = true
	m.resetComposer()
	m.rerenderViewport()
	return tea.Batch(m.briefCmd(m.preflightQuestion, m.preflightAnswers), scheduleTextTick())
}

// handleBriefResult enters brief-edit mode with the draft preloaded
// into the composer. An errored brief is still recoverable: we fall
// back to the original question text so the user always has something
// to edit rather than an empty box.
func (m *Model) handleBriefResult(msg briefResultMsg) tea.Cmd {
	m.preflightAwaiting = false
	m.preflightMode = preflightBrief
	draft := strings.TrimSpace(msg.brief)
	if msg.err != nil || draft == "" {
		draft = strings.TrimSpace(m.preflightQuestion)
	}
	m.preflightBriefDraft = draft
	m.resetComposer()
	m.ta.SetValue(draft)
	m.ta.CursorEnd()
	m.rerenderViewport()
	return nil
}

// cancelPreflight tears down all pre-flight state and clears the
// composer. Idempotent. Returns a status command so the user sees the
// cancel land.
func (m *Model) cancelPreflight() tea.Cmd {
	m.resetPreflight()
	m.resetComposer()
	m.rerenderViewport()
	return func() tea.Msg {
		return statusMsg{text: "research pre-flight canceled", kind: statusInfo}
	}
}

// resetPreflight zeroes the state machine without touching the
// composer. Used by cancel + by the launch path (which clears state and
// then dispatches the real research command).
func (m *Model) resetPreflight() {
	m.preflightMode = preflightOff
	m.preflightAwaiting = false
	m.preflightQuestion = ""
	m.preflightQuestions = nil
	m.preflightIndex = 0
	m.preflightAnswers = nil
	m.preflightBriefDraft = ""
}

// handlePreflightKey routes keys while the pre-flight owns the keyboard.
// Returns (model, cmd, handled); handled=false only for ctrl+c so the
// quit path stays reachable, mirroring the other overlays. While a
// model call is in flight (preflightAwaiting) every key except ctrl+c
// is swallowed so stray input never leaks into the next stage.
func (m *Model) handlePreflightKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if msg.String() == "ctrl+c" {
		return m, nil, false
	}
	if msg.String() == "esc" {
		return m, m.cancelPreflight(), true
	}
	if m.preflightAwaiting {
		// A call is in flight: swallow everything (except the ctrl+c /
		// esc handled above) so input can't race the result.
		return m, nil, true
	}
	switch m.preflightMode {
	case preflightClarify:
		return m.handleClarifyKey(msg)
	case preflightBrief:
		return m.handleBriefKey(msg)
	}
	return m, nil, true
}

// handleClarifyKey captures the answer to the current question. Enter
// records the trimmed composer value (which may be empty if the user
// skips the question) and advances; after the last question it requests
// the brief. All other keys feed the textarea so the user can type the
// answer.
func (m *Model) handleClarifyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if msg.String() == "enter" {
		answer := strings.TrimSpace(m.ta.Value())
		m.preflightAnswers = append(m.preflightAnswers, answer)
		m.preflightIndex++
		if m.preflightIndex >= len(m.preflightQuestions) {
			return m, m.requestBrief(), true
		}
		m.resetComposer()
		m.rerenderViewport()
		return m, nil, true
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	m.rerenderViewport()
	return m, cmd, true
}

// handleBriefKey edits the brief draft in the composer. Enter confirms
// and launches research with the edited brief; an empty brief is
// refused with a status nudge rather than launching a blank run. All
// other keys feed the textarea.
func (m *Model) handleBriefKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if msg.String() == "enter" {
		brief := strings.TrimSpace(m.ta.Value())
		if brief == "" {
			return m, func() tea.Msg {
				return statusMsg{text: "brief is empty; type a brief or press esc to cancel", kind: statusWarn}
			}, true
		}
		return m, m.launchResearchFromPreflight(brief), true
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	m.rerenderViewport()
	return m, cmd, true
}

// launchResearchFromPreflight tears down the pre-flight and dispatches
// the existing research path with the refined brief as the question.
// Mirrors the dispatch in chat.Update's `case "research"`: async path
// when a spawner is wired, sync path otherwise.
func (m *Model) launchResearchFromPreflight(brief string) tea.Cmd {
	m.resetPreflight()
	m.resetComposer()
	m.rerenderViewport()
	if m.spawner != nil {
		return m.runResearchAsync(brief)
	}
	return m.runResearchCmd(brief)
}

// renderPreflightOverlay paints the pre-flight into the takeover slot.
// Three faces: a thinking affordance while a call is in flight, the
// clarify question prompt, and the editable brief. Sandlot language:
// dashed rules, dim italic corner tag, no left stripe.
func renderPreflightOverlay(m *Model, innerW, innerH int) string {
	const indent = "  "
	contentW := innerW - len(indent)
	if contentW < 24 {
		contentW = 24
	}

	dim := lipgloss.NewStyle().Foreground(colorMuted)
	tagStyle := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	accent := lipgloss.NewStyle().Foreground(colorAccent)

	tag := "research pre-flight"
	ruleW := contentW - lipgloss.Width(tag) - 1
	if ruleW < 1 {
		ruleW = 1
	}
	top := dim.Render(strings.Repeat("┄", ruleW)) + " " + tagStyle.Render(tag)

	rows := []string{indent + top}

	if m.preflightAwaiting {
		dots := renderThinkingDots(m.thinkingTick)
		label := tagStyle.Render("thinking")
		rows = append(rows, indent, indent+dots+"   "+label)
		rows = append(rows, indent, indent+renderPreflightFooter(preflightOff))
		return strings.Join(rows, "\n")
	}

	switch m.preflightMode {
	case preflightClarify:
		total := len(m.preflightQuestions)
		idx := m.preflightIndex
		if idx >= total {
			idx = total - 1
		}
		counter := tagStyle.Render(progressLabel(idx+1, total))
		question := ""
		if idx >= 0 && idx < total {
			question = m.preflightQuestions[idx]
		}
		rows = append(rows,
			indent,
			indent+accent.Render("clarify")+"  "+counter,
			indent+truncateRight(question, contentW),
			indent+dim.Render(strings.Repeat("┄", contentW)),
			indent+dim.Render("answer below, or leave blank to skip"),
		)
		rows = append(rows, indent, indent+renderPreflightFooter(preflightClarify))
	case preflightBrief:
		rows = append(rows,
			indent,
			indent+accent.Render("brief")+"  "+tagStyle.Render("review and edit, then confirm"),
			indent+dim.Render(strings.Repeat("┄", contentW)),
			indent+dim.Render("edit the brief in the composer below"),
		)
		rows = append(rows, indent, indent+renderPreflightFooter(preflightBrief))
	}

	return strings.Join(rows, "\n")
}

// progressLabel renders the "question n of m" counter for the clarify
// step. Reuses the package's itoa shim (suggest.go) so this file pulls
// no new imports.
func progressLabel(n, total int) string {
	return "question " + itoa(n) + " of " + itoa(total)
}

// renderPreflightFooter paints the keybind hint row for the current
// stage. The clarify stage advances on Enter; the brief stage confirms
// on Enter; both cancel on Esc.
func renderPreflightFooter(mode preflightMode) string {
	switch mode {
	case preflightClarify:
		return footerKey("enter") + footerLabel(" next") +
			footerSep() + footerKey("esc") + footerLabel(" cancel")
	case preflightBrief:
		return footerKey("enter") + footerLabel(" run research") +
			footerSep() + footerKey("esc") + footerLabel(" cancel")
	}
	return footerKey("esc") + footerLabel(" cancel")
}
