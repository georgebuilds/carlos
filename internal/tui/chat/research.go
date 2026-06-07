package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/research"
)

// ResearchSpawner is the chat-side seam onto research.SpawnResearch
// (Phase 11 slice 11e). Production wires a thin adapter that calls
// SpawnResearch(ctx, log, engine, question); tests inject a fake that
// returns a canned (agentID, done-channel, error) tuple without
// touching the engine or the event log.
//
// The chat package owns the interface (not internal/research) because
// the chat is the only consumer; pushing the interface down into the
// research package would force every research-package test to satisfy
// the chat's contract too. Same pattern as ResearchEngine above.
type ResearchSpawner interface {
	Spawn(ctx context.Context, question string) (agentID string, done <-chan research.ResearchResult, err error)
}

// WithResearchSpawner wires a ResearchSpawner so the `/research` slash
// command takes the async (sub-agent) path. When nil, /research falls
// through to the slice-11f synchronous goroutine path
// (runResearchCmd). Both paths need a non-nil ResearchEngine; spawner
// alone won't activate the verb.
//
// Production wires a SpawnFunc adapter against the same
// *agent.SQLiteEventLog + *research.Engine the chat already uses.
// Tests inject a fake recorder that asserts the question + delivers a
// synthetic ResearchResult on demand.
func WithResearchSpawner(s ResearchSpawner) Option {
	return func(m *Model) { m.spawner = s }
}

// SpawnFunc is a function-typed ResearchSpawner so callers can wire
// production without declaring a struct. Mirrors http.HandlerFunc /
// io.Closer's ad-hoc adapter pattern.
type SpawnFunc func(ctx context.Context, question string) (string, <-chan research.ResearchResult, error)

// Spawn satisfies [ResearchSpawner].
func (f SpawnFunc) Spawn(ctx context.Context, q string) (string, <-chan research.ResearchResult, error) {
	return f(ctx, q)
}

// ResearchEngine is the seam between the chat Model and the slice-11c
// research orchestrator. The production wiring passes *research.Engine
// (which satisfies this interface naturally); tests inject a fake that
// returns a canned Report.
//
// Kept as an interface (rather than a hard *research.Engine field) so
// the chat package never grows a transport / network dependency the
// tests would need to stub out - the engine itself owns the provider +
// search + fetch wiring, and the chat side only needs Run.
type ResearchEngine interface {
	Run(ctx context.Context, question string) (*research.Report, error)
}

// WithResearchEngine wires a ResearchEngine so the `/research` slash
// command runs against it. When nil, `/research` returns a clean
// "research engine not wired" status echo - useful for the dev-aid
// chat surface that intentionally has no provider hooked up.
//
// The production wire-up (cmd/carlos.runDefault) constructs the engine
// from the same provider + web tools the chat already uses and hands it
// to the chat Model via this option.
func WithResearchEngine(e ResearchEngine) Option {
	return func(m *Model) { m.researchEngine = e }
}

// researchSyncTimeout caps a single /research run. The engine has its
// own ResearchBudget.MaxWallClock (5 min default) which is the inner
// guard; this outer cap keeps a runaway from blocking the chat goroutine
// forever if, e.g., the engine itself hangs before the budget timer
// arms. A 10-minute cap gives the engine room to use its full budget
// plus headroom for the synthesis turn.
const researchSyncTimeout = 10 * time.Minute

// runResearchCmd is the synchronous-for-v0 driver for `/research`. It:
//
//  1. Returns immediately with a status line so the user sees something
//     happen - the engine itself takes 30s-3min in the typical case.
//  2. Spawns a goroutine that calls engine.Run.
//  3. On completion (success or error), writes a single
//     EvtAssistantMessage to the log so the rendered Report (or error
//     line) lands in the transcript via the chat's normal subscription
//     pump - no special-case rendering needed.
//
// Sync execution is the v0 contract; slice 11d makes it a real sub-agent
// so the chat stays interactive while research runs.
func (m *Model) runResearchCmd(question string) tea.Cmd {
	if m.researchEngine == nil {
		return func() tea.Msg {
			return statusMsg{
				text: "/research: research engine not wired (no provider configured?)",
				kind: statusWarn,
			}
		}
	}
	engine := m.researchEngine
	log := m.log
	agentID := m.agentID

	// Fire the goroutine before returning the statusMsg so the user
	// doesn't see "researching…" sit on screen with no work actually
	// in flight if Update is briefly stalled.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), researchSyncTimeout)
		defer cancel()
		report, err := engine.Run(ctx, question)
		var body string
		switch {
		case err != nil && report != nil && (report.Synthesis != "" || len(report.Sources) > 0):
			// Partial result: still useful - render what we got plus the
			// failure line. The engine's own Concerns list will reflect
			// the abort cause; we append a top-level note so the user
			// can see the failure without scrolling.
			body = fmt.Sprintf("research finished with error: %v\n\n%s", err, RenderReportMarkdown(report))
		case err != nil:
			body = fmt.Sprintf("research failed: %v", err)
		default:
			body = RenderReportMarkdown(report)
		}
		payload, perr := json.Marshal(agent.MessagePayload{Text: body})
		if perr != nil {
			// Marshal of a single Text field shouldn't fail; if it did,
			// we have nothing to surface in-chat. Best-effort: emit a
			// fallback short error.
			body = fmt.Sprintf("research: marshal result: %v", perr)
			payload, _ = json.Marshal(agent.MessagePayload{Text: body})
		}
		appendCtx, appendCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer appendCancel()
		_, _ = log.Append(appendCtx, agent.Event{
			AgentID: agentID,
			TS:      time.Now().UTC(),
			Type:    agent.EvtAssistantMessage,
			Payload: payload,
		})
	}()

	return func() tea.Msg {
		return statusMsg{
			text: "researching: " + question + " (may take 1-2 min; result will appear as a 🧢 reply)",
			kind: statusInfo,
		}
	}
}

// researchAsyncTimeout caps the sub-agent context. Same 10-minute
// budget as the sync path (researchSyncTimeout) - the engine's own
// budget is the inner guard; this is the outer "something is hung"
// fence. The chat stays interactive throughout: the goroutine just
// drains the spawn channel and the phase events flow through the
// subscription pump.
const researchAsyncTimeout = 10 * time.Minute

// runResearchAsync is the slice-11e supervisor-aware /research path. It
// kicks off SpawnResearch via the wired ResearchSpawner, returns
// immediately with a status echo, and lets the phase events stream
// into entryResearchProgress rows through the chat's normal
// subscription pump.
//
// The goroutine only exists to keep the sub-agent's context alive
// (cancel-on-deadline) and to surface a fall-back error statusMsg if
// SpawnResearch itself fails before the first event lands. Once the
// engine starts, every observable progress signal comes through the
// event log - no out-of-band channel into the Model - so race-
// conditions on transcript mutation are impossible.
func (m *Model) runResearchAsync(question string) tea.Cmd {
	spawner := m.spawner
	if spawner == nil {
		// Defensive: the dispatchSlash gate already checks this, but
		// keep the method self-contained so future call sites don't
		// have to remember the guard.
		return m.runResearchCmd(question)
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), researchAsyncTimeout)
		defer cancel()
		// Spawn writes the EvtStateChange + initial events itself; we
		// just drain the done channel so the sub-agent's lifetime is
		// observable. The done event triggers the artifact write +
		// final transition inside SpawnResearch's worker; the chat
		// renders the phase events as they flow.
		_, done, err := spawner.Spawn(ctx, question)
		if err != nil {
			// Spawn-time failure (engine nil, log nil, marshal): no
			// EvtResearchPhase events will ever fire, so we surface
			// the failure via the same assistant-message path the
			// sync handler uses. This keeps the user's chat from
			// silently swallowing a wired /research that didn't get
			// off the launchpad.
			payload, perr := json.Marshal(agent.MessagePayload{
				Text: fmt.Sprintf("research failed: %v", err),
			})
			if perr != nil {
				return
			}
			appendCtx, appendCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer appendCancel()
			_, _ = m.log.Append(appendCtx, agent.Event{
				AgentID: m.agentID,
				TS:      time.Now().UTC(),
				Type:    agent.EvtAssistantMessage,
				Payload: payload,
			})
			return
		}
		// Drain the channel so the goroutine doesn't leak. The result
		// is observable via the event stream (phase done event +
		// artifact ref); we don't need to inspect it here.
		if done != nil {
			<-done
		}
	}()

	return func() tea.Msg {
		return statusMsg{
			text: "research started; progress will stream here…",
			kind: statusInfo,
		}
	}
}

// formatResearchProgress renders a ResearchPhasePayload into a single
// transcript line. The output shape is brand-marked with the 🔬
// glyph so it's distinguishable from the assistant's 🧢 turns at a
// glance.
//
// State machine (matches slice-11d engine semantics):
//
//	in-flight:                   🔬 research: <phase>
//	phase done, more to go:      🔬 research: <phase> · <elapsed>
//	final phase done (verify):   🔬 research done · <total elapsed>
//	any phase failed:            🔬 research failed: <err>
//
// The "final phase" check is keyed on the phase name "verify" because
// that's the last entry in the research engine's six-phase machine
// (decompose → search → fetch → read → synthesize → verify). When the
// engine ever grows a new terminal phase we'll need to update this in
// lockstep; the engine's package docstring promises that's the
// canonical list.
func formatResearchProgress(p agent.ResearchPhasePayload) string {
	if p.Done && p.Err != "" {
		return "🔬 research failed: " + truncate(p.Err, 80)
	}
	if p.Done {
		if p.Phase == "verify" {
			return "🔬 research done · " + p.Elapsed.Round(time.Second).String()
		}
		return "🔬 research: " + p.Phase + " · " + p.Elapsed.Round(100*time.Millisecond).String()
	}
	return "🔬 research: " + p.Phase
}

// truncate returns s clipped to maxRunes with a trailing ellipsis if
// any runes were dropped. Used to keep failure messages bounded inside
// a single transcript row.
func truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

// RenderReportMarkdown turns a *research.Report into a human-readable
// markdown document. Mirrors cmd/carlos.renderReportMarkdown so the
// chat-surfaced report has the same shape as the headless CLI's
// stdout output. Exported so cmd/carlos can call into the same
// formatter - the chat package is the lowest-dep place that already
// imports both the agent log and the research types, so it's a
// natural home.
//
// The format is intentionally Markdown rather than ANSI so the
// rendered text reads sensibly both inside the chat viewport (which
// renders plain text but happily passes Markdown through) and when
// piped to a file from `carlos research`.
func RenderReportMarkdown(r *research.Report) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Research report: %s\n\n", r.Question)
	if len(r.Query.Sub) > 0 {
		b.WriteString("## Sub-queries\n\n")
		for _, s := range r.Query.Sub {
			fmt.Fprintf(&b, "- %s\n", s)
		}
		b.WriteString("\n")
	}
	if r.Synthesis != "" {
		b.WriteString("## Synthesis\n\n")
		b.WriteString(r.Synthesis)
		b.WriteString("\n\n")
	}
	if len(r.Sources) > 0 {
		b.WriteString("## Sources\n\n")
		for _, s := range r.Sources {
			title := s.Title
			if title == "" {
				title = "(untitled)"
			}
			fmt.Fprintf(&b, "- **%s** - %s - <%s>\n", s.ID, title, s.URL)
		}
		b.WriteString("\n")
	}
	if len(r.Passages) > 0 {
		b.WriteString("## Passages\n\n")
		for _, p := range r.Passages {
			fmt.Fprintf(&b, "- **[%s]** (relevance %d, source %s): %s\n",
				p.ID, p.Relevance, p.SourceID, p.Text)
		}
		b.WriteString("\n")
	}
	if r.Citations != nil {
		fmt.Fprintf(&b, "## Citation audit\n\n- claims: %d\n- coverage score: %.2f\n- unsupported: %d\n\n",
			r.Citations.ClaimCount, r.Citations.Score, len(r.Citations.Unsupported))
	}
	if r.Verification != nil {
		fmt.Fprintf(&b, "## Verifier\n\n- decision: %s\n- score: %d\n- judge: %s\n",
			r.Verification.Decision, r.Verification.Score, r.Verification.JudgeModel)
		if len(r.Verification.Concerns) > 0 {
			b.WriteString("- concerns:\n")
			for _, c := range r.Verification.Concerns {
				fmt.Fprintf(&b, "  - %s\n", c)
			}
		}
		b.WriteString("\n")
	}
	if len(r.Concerns) > 0 {
		b.WriteString("## Engine concerns\n\n")
		for _, c := range r.Concerns {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## Budget\n\n- provider calls: %d\n- fetched bytes: %d\n- elapsed: %s\n",
		r.Budget.ProviderCalls, r.Budget.FetchedBytes, r.Budget.Elapsed)
	return b.String()
}
