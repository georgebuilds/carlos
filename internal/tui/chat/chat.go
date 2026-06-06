package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/memory"
	"github.com/georgebuilds/carlos/internal/schedule"
	"github.com/georgebuilds/carlos/internal/theme"
	"github.com/georgebuilds/carlos/internal/tui/slash"
	"github.com/georgebuilds/carlos/internal/usershell"
)

// Brand palette — package-level vars populated by [ApplyPalette].
//
// Prior to Phase 9 slice 9a these were inline literals mirrored from
// internal/tui/onboarding. They are now sourced from
// [internal/theme.Palette] so a single env/config edit recolors every
// TUI surface in lockstep. The package-var shape is preserved so the
// dozens of callsites that read e.g. `colorAccent` stay unchanged;
// only the construction moves.
//
// init() seeds with autodetect defaults so tests that don't call
// ApplyPalette still see colors. main() overrides at startup with the
// user-configured palette.
var (
	colorAccent lipgloss.Color
	colorMuted  lipgloss.Color
	colorUser   lipgloss.Color
	colorAgent  lipgloss.Color
	colorTool   lipgloss.Color
	colorWarn   lipgloss.Color
	colorOK     lipgloss.Color
	colorSubtle lipgloss.Color
)

func init() {
	ApplyPalette(theme.Load(theme.Options{}))
}

// ApplyPalette wires a freshly-loaded [theme.Palette] into the chat
// package's color vars. Call once at startup from cmd/carlos after the
// user config is loaded. Idempotent and safe to call again on config
// reload (no concurrency guarantee — TUI startup is single-threaded).
func ApplyPalette(p theme.Palette) {
	colorAccent = p.Accent
	colorMuted = p.Muted
	colorUser = p.User
	colorAgent = p.Agent
	colorTool = p.Tool
	colorWarn = p.Warn
	colorOK = p.OK
	colorSubtle = p.Subtle
}

// Minimum terminal size. Chat is a working surface, not a poster — the
// floor is lower than onboarding's 80x24 because nothing breaks below
// it; the experience just gets cramped. Below 60x16 we refuse to render
// to avoid corrupt output.
const (
	minTermWidth  = 60
	minTermHeight = 16
)

// transcriptEntry is one line item in the rendered message log. Built
// from event-log rows during replay + live updates; the order is
// chronological (matches event seq order).
//
// subAgentID is the collapse key for [entryResearchProgress] rows
// (Phase 11 slice 11e): multiple EvtResearchPhase events for the same
// research sub-agent fold into a single entry that mutates in place
// rather than appending a fresh row per phase. Empty for every other
// entry kind.
type transcriptEntry struct {
	kind       entryKind
	ts         time.Time
	text       string
	tool       string // tool name for ToolCall/ToolResult
	toolInput  string // raw JSON input the model sent (for the tool card preview)
	toolResult string // captured output; set when the matching EvtToolResult lands
	hasResult  bool   // true once the EvtToolResult has been folded in (distinguishes "no output" from "still running")
	isError    bool   // tool_result was an error (rejection or tool err)
	subAgentID string // collapse key for entryResearchProgress (slice 11e)

	// User-shell (Phase U S5) fields. Active when kind == entryUserShell.
	// shellJobID is the collapse key so EvtUserShellEnd folds into the
	// row created by EvtUserShellStart instead of appending a fresh row.
	// shellOutput streams in via the Manager Subscribe pump until the
	// End event lands the canonical inline-truncated copy.
	shellJobID       string
	shellCommand     string
	shellOutput      string
	shellExitCode    int
	shellDuration    time.Duration
	shellRunning     bool // true between Start and End
	shellCancelled   bool
	shellBackgrounded bool
	shellTruncated   int // bytes dropped from the inline output
	shellFailErr     string
}

type entryKind int

const (
	entryUserMessage entryKind = iota
	entryAssistantMessage
	entryToolCall
	entryToolResult
	entrySteering
	entryStateChange
	entrySystemNote
	// entryResearchProgress is the live progress line for a /research
	// sub-agent. Slice 11e renders one entry per sub-agent (keyed by
	// AgentID); each EvtResearchPhase event for that sub-agent rewrites
	// the entry in place. The done event with no err transitions the
	// row to a "research done · <elapsed>" summary; an err transitions
	// it to a warn-colored "research failed: <err>".
	entryResearchProgress
	// entryUserShell is a "!cmd"-prefixed shell command (Phase U S5).
	// Created by EvtUserShellStart with shellRunning=true; updated in
	// place when EvtUserShellEnd lands AND when the Manager's
	// Subscribe pump streams an output chunk. Renders as a styled
	// block: prompt line, monospace output body, status badge.
	entryUserShell
)

// Model is the bubbletea Model for the single-agent chat view.
//
// Lifecycle:
//
//	New(log, agentID, source)        → returned
//	Init()                           returns a Cmd batch that:
//	                                   - backfills the transcript via Read()
//	                                   - opens a live Subscribe channel
//	                                   - starts a low-Hz text ticker
//	Update routes WindowSizeMsg, KeyMsg, eventMsg, etc.
//	View composes header + transcript viewport + footer.
//
// This Slice-1e-commit-1 shape is read-only: there is no input line.
// Subsequent commits add textarea + slash dispatch + alt-screen polish.
type Model struct {
	log     agent.EventLog
	agentID string
	source  TextSource

	// Replay-derived state. Apply on every event so the projection stays
	// current; transcript is the rendered side-effect.
	proj       *agent.Projection
	transcript []transcriptEntry

	// Bubbles components.
	vp viewport.Model
	ta textarea.Model

	// Live subscription handle. nil until subscribeCmd resolves.
	subCh     <-chan agent.Event
	subCancel func()

	// Layout.
	width  int
	height int

	// readOnly disables the textarea + submit path. Useful for snapshot
	// tests that don't want to assert on the textarea's cursor state,
	// and for any future "transcript-only" surface (e.g. a read replica
	// of another agent's transcript inside manage mode).
	readOnly bool

	// status is a transient line shown in the footer (above the
	// keybind row). Used by slash dispatch to echo "/foo (handler
	// pending)" and similar. Cleared on the next keystroke.
	status     string
	statusKind statusKind

	// quitting is set on ctrl-c; View can short-circuit.
	quitting bool

	// openManage is the slice-7g cross-screen signal. /agents sets
	// this + quits; the caller (cmd/carlos) reads it after Run
	// returns and relaunches as the manage TUI. Closes the loop on
	// the slash command without needing an in-process model swap
	// (that's a future unified-TUI slice).
	openManage bool

	// approver is the optional TUIApprover bridging agent.Run's
	// synchronous Approver.ApproveToolCall to the TUI's async
	// overlay. nil when chat runs without tool dispatch (the dev-aid
	// + tests). When non-nil, Init starts a pump goroutine that
	// turns Requests() into approvalRequestMsg events.
	approver *TUIApprover

	// pendingApproval is the in-flight tool-call awaiting the user's
	// y/N/Always decision. When non-nil, the overlay renders above
	// the footer + keystroke routing intercepts y/n/A before the
	// textarea sees them.
	pendingApproval *ApprovalRequest

	// showHelp is the slice-9d help-overlay state. Toggled by /help
	// (open) and any keypress while open (close). Renders a panel
	// listing every slash command with its description; the panel
	// sits in the same slot as the approval overlay.
	showHelp bool

	// userName addresses the user in the empty-state greeting.
	// Defaults to "Boss" — matches carlos's brand voice. Override
	// via WithUserName(cfg.UserName).
	userName string

	// researchEngine drives the `/research` slash command (slice 11f).
	// Nullable: the dev-aid chat surface has no provider hooked up and
	// runs with engine=nil, which makes /research return a clean "not
	// wired" status echo rather than panic. The production wire-up
	// (cmd/carlos.runDefault) constructs the engine + injects via
	// WithResearchEngine.
	researchEngine ResearchEngine

	// spawner is the slice-11e sub-agent driver. When both spawner AND
	// researchEngine are wired, `/research` takes the async path
	// (runResearchAsync) and the chat stays interactive while phase
	// events stream into entryResearchProgress rows. When spawner is
	// nil, `/research` falls through to the slice-11f synchronous path
	// (runResearchCmd) so behavior degrades gracefully.
	spawner ResearchSpawner

	// summarizer is the slice-9j `/compact` driver. When nil, /compact
	// echoes a "not configured" statusMsg rather than failing — the
	// chat surface still works without an LLM-backed summarizer.
	// Production wires memory.LLMSummarizer; tests inject a fake.
	summarizer memory.Summarizer

	// usershell is the Phase U "!"-prefix driver. When nil, the chat
	// rejects "!cmd" submissions with a "not wired" status echo and
	// the four-state footer collapses to its idle branch.
	// Production wires a usershell.Manager scoped to the chat
	// session; tests inject either a real Manager with a fake runner
	// or leave it nil.
	usershell      *usershell.Manager
	userShellSubCh <-chan usershell.Update
	userShellUnsub func()
}

type statusKind int

const (
	statusInfo statusKind = iota
	statusWarn
	statusError
)

// Option configures a Model at construction time.
type Option func(*Model)

// WithReadOnly disables input handling — the chat view becomes a pure
// transcript reader. Used by snapshot tests and any future view that
// shows another agent's transcript without owning input.
func WithReadOnly() Option {
	return func(m *Model) { m.readOnly = true }
}

// WithTUIApprover wires a TUIApprover so tool-call prompts render
// in-chat instead of running silently (AutoApprover) or on stdin
// (the `please` flow). cmd/carlos.runDefault constructs the
// approver, hands the same instance to both chatglue.Config.Approver
// and this option, and the chat Model owns the y/N/Always overlay.
func WithTUIApprover(a *TUIApprover) Option {
	return func(m *Model) { m.approver = a }
}

// WithUserName personalizes the empty-state greeting. Empty string is
// fine — the default "Boss" is the carlos voice; pass the user's real
// name from onboarding config to make the first frame more familiar.
func WithUserName(name string) Option {
	return func(m *Model) {
		if name != "" {
			m.userName = name
		}
	}
}

// WithUserShell attaches a usershell.Manager so "!cmd" submissions
// run as shell commands. Nil (or omitting this option entirely)
// leaves the feature dormant — the composer treats "!ls" like any
// other text and the footer collapses to its idle branch.
// cmd/carlos.runDefault constructs the Manager scoped to the chat
// session's event log; tests pass either a real Manager with a
// fake runner or skip the option entirely.
func WithUserShell(mgr *usershell.Manager) Option {
	return func(m *Model) { m.usershell = mgr }
}

// New constructs a chat Model bound to the given event log + agent. The
// TextSource is required (pass NewMemTextSource() if you have nothing
// else); Slice 1f will plug a real streaming source in.
func New(log agent.EventLog, agentID string, source TextSource, opts ...Option) *Model {
	if source == nil {
		source = NewMemTextSource()
	}
	vp := viewport.New(0, 0)
	vp.MouseWheelEnabled = true

	ta := textarea.New()
	ta.Placeholder = "type a message — enter to send, shift-enter for newline"
	ta.Prompt = "│ "
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.SetHeight(3)
	ta.Focus()
	// Carry-over default textarea binds enter to insert-newline. We need
	// enter as a submit and shift-enter (or ctrl-j on terminals that
	// can't disambiguate shift-enter) to insert a newline.
	ta.KeyMap.InsertNewline.SetKeys("shift+enter", "ctrl+j")

	m := &Model{
		log:      log,
		agentID:  agentID,
		source:   source,
		proj:     agent.NewProjection(),
		vp:       vp,
		ta:       ta,
		userName: "Boss",
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Run is the convenience entry point for callers that just want to
// drop into the chat surface: full-screen alt-screen + mouse cell
// motion (so the viewport's scroll wheel works) + the same Program
// lifecycle pattern onboarding uses.
//
// Returns the final model + the program error; callers can ignore the
// model for now (we don't carry state out). The OpenManageRequested
// helper is the typed read of the post-exit flag for slice 7g.
func (m *Model) Run() (tea.Model, error) {
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	return p.Run()
}

// OpenManageRequested returns true if the user exited via /agents and
// the caller should relaunch into the manage TUI.
func (m *Model) OpenManageRequested() bool { return m.openManage }

// Init kicks off backfill + subscription + the text ticker, plus the
// textarea cursor blink if we're accepting input.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		backfillCmd(m.log, m.agentID),
		subscribeCmd(m.log, m.agentID),
		scheduleTextTick(),
	}
	if !m.readOnly {
		cmds = append(cmds, textarea.Blink)
	}
	if m.approver != nil {
		cmds = append(cmds, approvalPumpCmd(m.approver.Requests()))
	}
	if m.usershell != nil {
		ch, unsub := m.usershell.Subscribe()
		m.userShellSubCh = ch
		m.userShellUnsub = unsub
		cmds = append(cmds, pumpUserShellCmd(ch))
	}
	return tea.Batch(cmds...)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.MouseMsg:
		// Mouse / trackpad scrolling. bubbletea forwards every
		// MouseMsg here (WithMouseCellMotion is set in Run); we
		// hand them to the viewport which already has
		// MouseWheelEnabled=true and knows how to map wheel-up /
		// wheel-down into YOffset adjustments. Returning a non-nil
		// cmd is unusual for the viewport's mouse path, but the
		// signature is the same as KeyMsg routing so we forward it
		// just in case a future bubbles version does emit one.
		//
		// rerenderViewport's wasAtBottom check handles the follow-
		// tail interaction: scroll up → wasAtBottom becomes false →
		// subsequent textTicks stop snapping to bottom until the
		// user scrolls back down.
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		// Help overlay dismisses on the next keystroke. ctrl-c falls
		// through to the normal quit path; everything else just
		// closes the panel without affecting the textarea.
		if m.showHelp {
			if msg.String() == "ctrl+c" {
				// fall through to the quit handler below
			} else {
				m.showHelp = false
				m.rerenderViewport()
				return m, nil
			}
		}
		// Approval overlay intercepts y/n/A before the textarea sees
		// them. Other keys (esp. ctrl-c) still flow through so the
		// user can cancel the session even while a prompt is active.
		if m.pendingApproval != nil {
			switch msg.String() {
			case "y", "Y":
				return m, m.resolveApproval(ApprovalAllow)
			case "n", "N", "esc":
				return m, m.resolveApproval(ApprovalDeny)
			case "a", "A":
				return m, m.resolveApproval(ApprovalAllowAlways)
			case "ctrl+c":
				// Treat ctrl-c during the prompt as a Deny + quit.
				// chatglue's loop unblocks cleanly via the Deny.
				_ = m.resolveApproval(ApprovalDeny)
				m.quitting = true
				if m.subCancel != nil {
					m.subCancel()
				}
				return m, tea.Quit
			default:
				// Swallow other keys while overlay is active so the
				// textarea doesn't accumulate stray input.
				return m, nil
			}
		}
		switch msg.String() {
		case "ctrl+c":
			// Ctrl+C means: cancel the running fg shell job if there
			// is one (mirrors terminal SIGINT semantics); otherwise
			// quit the chat. The TUI research note flags ctrl+c as
			// terminal-reserved for cancel and we honor that — the
			// chat-quit path keeps it as the second-class meaning so
			// the user can still exit, just with no fg job parked.
			if cmd := m.cancelForegroundCmd(); cmd != nil {
				return m, cmd
			}
			m.quitting = true
			if m.subCancel != nil {
				m.subCancel()
			}
			if m.userShellUnsub != nil {
				m.userShellUnsub()
			}
			return m, tea.Quit
		case "ctrl+z":
			// Mirror shell ^Z: background the running fg job. No-op
			// if nothing is running in the fg slot.
			if cmd := m.backgroundRunningCmd(); cmd != nil {
				return m, cmd
			}
			return m, nil
		case "pgup", "pgdown", "home", "end":
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		case "enter":
			if m.readOnly {
				return m, nil
			}
			return m, m.submit()
		case "ctrl+@":
			// Ctrl+Enter on terminals that emit NUL (mac Terminal,
			// many xterms). Treat as "submit as background shell
			// job" — only when the input starts with "!", else
			// fall through to the textarea so it stays a no-op.
			if m.readOnly {
				return m, nil
			}
			if hasShellPrefix(m.ta.Value()) {
				return m, m.submitBackgroundShell()
			}
		}
		// Default route: textarea owns the keystroke when input is enabled.
		// In read-only mode we send arrow keys/etc to the viewport so the
		// user can still scroll.
		if m.readOnly {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
		// Any non-submit keystroke clears a stale status echo so the
		// footer reflects current state.
		if m.status != "" {
			m.status = ""
		}
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		return m, cmd

	case backfillMsg:
		for _, ev := range msg.events {
			m.applyEvent(ev)
		}
		m.rerenderViewport()
		return m, nil

	case subscriptionReady:
		m.subCh = msg.ch
		m.subCancel = msg.cancel
		return m, pumpEventCmd(msg.ch)

	case eventMsg:
		m.applyEvent(msg.ev)
		m.rerenderViewport()
		return m, m.repumpCmd()

	case approvalRequestMsg:
		// Stash the request; overlay renders on next View. Force
		// scroll-to-bottom regardless of prior YOffset — the user
		// definitely wants to see the conversation that just led the
		// model to ask for this tool. Re-pump so any further requests
		// (rare but possible if the model paused and resumed mid-
		// stream) queue behind this one.
		m.pendingApproval = msg.req
		m.rerenderViewport()
		m.vp.GotoBottom()
		return m, approvalPumpCmd(m.approver.Requests())

	case textTickMsg:
		// Re-render so live assistant text from the TextSource appears.
		m.rerenderViewport()
		return m, scheduleTextTick()

	case errMsg:
		m.transcript = append(m.transcript, transcriptEntry{
			kind: entrySystemNote,
			ts:   time.Now().UTC(),
			text: fmt.Sprintf("error: %v", msg.err),
		})
		m.rerenderViewport()
		return m, nil

	case statusMsg:
		m.status = msg.text
		m.statusKind = msg.kind
		return m, nil

	case userShellUpdateMsg:
		// S5: stream live output chunks into the matching transcript
		// entry. State-only updates (no Output bytes) just re-arm the
		// pump — the transcript row was created by EvtUserShellStart
		// and will be sealed by EvtUserShellEnd via applyEvent.
		if len(msg.u.Output) > 0 {
			if idx := m.findUserShellEntry(msg.u.JobID); idx != -1 {
				m.transcript[idx].shellOutput += string(msg.u.Output)
				m.rerenderViewport()
			}
		}
		if m.userShellSubCh != nil {
			return m, pumpUserShellCmd(m.userShellSubCh)
		}
		return m, nil

	case userShellSubscriptionClosedMsg:
		m.userShellSubCh = nil
		return m, nil
	}
	return m, nil
}

// repumpCmd re-arms the event pump after each delivered event so the
// chat keeps draining the subscription channel.
// findLatestToolCall returns the index of the most recent
// entryToolCall for toolName that hasn't yet had its result folded in
// (hasResult=false). Returns -1 when none exist. Used by the
// EvtToolResult handler to merge the result into the call entry so the
// transcript renders one bordered tool card instead of two rows.
func (m *Model) findLatestToolCall(toolName string) int {
	for i := len(m.transcript) - 1; i >= 0; i-- {
		e := m.transcript[i]
		if e.kind == entryToolCall && e.tool == toolName && !e.hasResult {
			return i
		}
	}
	return -1
}

func (m *Model) repumpCmd() tea.Cmd {
	if m.subCh == nil {
		return nil
	}
	return pumpEventCmd(m.subCh)
}

// resolveApproval pushes the user's decision back to the TUIApprover,
// clears the overlay state, and returns nil (the next render will
// drop the overlay panel). Safe to call with m.pendingApproval == nil
// (no-op) so the ctrl-c-during-overlay path can call it defensively.
func (m *Model) resolveApproval(d ApprovalDecision) tea.Cmd {
	if m.pendingApproval == nil || m.approver == nil {
		return nil
	}
	m.approver.Reply(m.pendingApproval, d)
	m.pendingApproval = nil
	return nil
}

// applyEvent folds one event into the projection AND the transcript.
//
// Projection errors surface as system-note transcript entries (drift
// detection should be loud), but DO NOT block the transcript update
// for the same event — the two concerns are independent. A heartbeat
// arriving for an agent the projection doesn't recognize shouldn't
// also stop the chat from rendering the user's next message just
// because the projection's row check failed.
func (m *Model) applyEvent(ev agent.Event) {
	if err := m.proj.Apply(ev); err != nil {
		m.transcript = append(m.transcript, transcriptEntry{
			kind: entrySystemNote,
			ts:   ev.TS,
			text: fmt.Sprintf("projection error on seq=%d (%s): %v", ev.Seq, ev.Type, err),
		})
		// fallthrough — still render the entry below if applicable.
	}
	switch ev.Type {
	case agent.EvtUserMessage:
		var p agent.MessagePayload
		_ = json.Unmarshal(ev.Payload, &p)
		m.transcript = append(m.transcript, transcriptEntry{
			kind: entryUserMessage,
			ts:   ev.TS,
			text: p.Text,
		})
	case agent.EvtAssistantMessage:
		var p agent.MessagePayload
		_ = json.Unmarshal(ev.Payload, &p)
		m.transcript = append(m.transcript, transcriptEntry{
			kind: entryAssistantMessage,
			ts:   ev.TS,
			text: p.Text,
		})
	case agent.EvtToolCall:
		var tc agent.ToolCall
		_ = json.Unmarshal(ev.Payload, &tc)
		m.transcript = append(m.transcript, transcriptEntry{
			kind:      entryToolCall,
			ts:        ev.TS,
			tool:      tc.Name,
			toolInput: string(tc.Input),
		})
	case agent.EvtToolResult:
		// Fold the result back into the most recent matching
		// entryToolCall instead of creating a separate row. The
		// transcript renders the pair as ONE bordered tool card
		// (collapsed by default; future slice adds expand toggle).
		var tr agent.ToolResult
		_ = json.Unmarshal(ev.Payload, &tr)
		idx := m.findLatestToolCall(tr.Name)
		if idx == -1 {
			// Defensive: result without a matching call (e.g. replay
			// of an event log truncated mid-turn). Fall back to a
			// standalone card so the result isn't silently dropped.
			m.transcript = append(m.transcript, transcriptEntry{
				kind:       entryToolCall,
				ts:         ev.TS,
				tool:       tr.Name,
				toolResult: string(tr.Output),
				hasResult:  true,
				isError:    tr.IsError,
			})
			break
		}
		m.transcript[idx].toolResult = string(tr.Output)
		m.transcript[idx].hasResult = true
		m.transcript[idx].isError = tr.IsError
	case agent.EvtSteering:
		var p agent.MessagePayload
		_ = json.Unmarshal(ev.Payload, &p)
		m.transcript = append(m.transcript, transcriptEntry{
			kind: entrySteering,
			ts:   ev.TS,
			text: p.Text,
		})
	case agent.EvtStateChange:
		// Render only spawn — transitions show up in the header badge.
		var sp agent.StateChangePayload
		_ = json.Unmarshal(ev.Payload, &sp)
		if sp.Kind == agent.StateChangeCreated && sp.Created != nil {
			m.transcript = append(m.transcript, transcriptEntry{
				kind: entryStateChange,
				ts:   ev.TS,
				text: fmt.Sprintf("agent %s spawned (model=%s)", sp.Created.ID, sp.Created.Model),
			})
		}
	case agent.EvtSessionReset:
		// Conversational fresh-start. Drop the rendered transcript;
		// the same event tells chatglue.buildHistory to start its
		// projection from the next event so the model also forgets.
		m.transcript = nil
	case agent.EvtResearchPhase:
		// Phase 11 slice 11e: collapse all phase events for a single
		// research sub-agent into one entryResearchProgress row that
		// mutates in place. Collapse key: ev.AgentID (the sub-agent's
		// id; the parent chat agent never owns research_phase events,
		// so this never collides with anything else).
		//
		// Replay-safety: a fresh chat load that walks the historic
		// event stream lands on the Done event last, so the final
		// state for each sub-agent settles correctly even without any
		// special replay logic.
		var p agent.ResearchPhasePayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			m.transcript = append(m.transcript, transcriptEntry{
				kind: entrySystemNote,
				ts:   ev.TS,
				text: fmt.Sprintf("research_phase: bad payload on seq=%d: %v", ev.Seq, err),
			})
			return
		}
		text := formatResearchProgress(p)
		entry := transcriptEntry{
			kind:       entryResearchProgress,
			ts:         ev.TS,
			text:       text,
			subAgentID: ev.AgentID,
			isError:    p.Done && p.Err != "",
		}
		idx := -1
		for i := range m.transcript {
			if m.transcript[i].kind == entryResearchProgress &&
				m.transcript[i].subAgentID == ev.AgentID {
				idx = i
				break
			}
		}
		if idx == -1 {
			m.transcript = append(m.transcript, entry)
		} else {
			m.transcript[idx] = entry
		}
	case agent.EvtUserShellStart:
		var p usershell.StartPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			m.transcript = append(m.transcript, transcriptEntry{
				kind: entrySystemNote,
				ts:   ev.TS,
				text: fmt.Sprintf("user_shell_start: bad payload on seq=%d: %v", ev.Seq, err),
			})
			return
		}
		m.transcript = append(m.transcript, transcriptEntry{
			kind:              entryUserShell,
			ts:                ev.TS,
			shellJobID:        p.JobID,
			shellCommand:      p.Command,
			shellRunning:      true,
			shellBackgrounded: p.Background,
		})
	case agent.EvtUserShellEnd:
		var p usershell.EndPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			m.transcript = append(m.transcript, transcriptEntry{
				kind: entrySystemNote,
				ts:   ev.TS,
				text: fmt.Sprintf("user_shell_end: bad payload on seq=%d: %v", ev.Seq, err),
			})
			return
		}
		idx := m.findUserShellEntry(p.JobID)
		if idx == -1 {
			// Orphan end (replay-truncated log): synthesize a row
			// so the user sees SOMETHING rather than silently
			// dropping the event.
			m.transcript = append(m.transcript, transcriptEntry{
				kind:              entryUserShell,
				ts:                ev.TS,
				shellJobID:        p.JobID,
				shellCommand:      "(unknown — start event missing)",
				shellOutput:       p.OutputInline,
				shellExitCode:     p.ExitCode,
				shellDuration:     p.Duration,
				shellCancelled:    p.Cancelled,
				shellBackgrounded: p.Backgrounded,
				shellTruncated:    p.TruncatedBytes,
				shellFailErr:      p.FailErrMsg,
			})
			return
		}
		m.transcript[idx].shellRunning = false
		m.transcript[idx].shellExitCode = p.ExitCode
		m.transcript[idx].shellDuration = p.Duration
		m.transcript[idx].shellCancelled = p.Cancelled
		m.transcript[idx].shellBackgrounded = p.Backgrounded
		m.transcript[idx].shellTruncated = p.TruncatedBytes
		m.transcript[idx].shellFailErr = p.FailErrMsg
		// Prefer the canonical inline output from the End event over
		// whatever streamed during the run — the End event's copy is
		// what the model sees, so the transcript should match.
		if p.OutputInline != "" {
			m.transcript[idx].shellOutput = p.OutputInline
		}
	}
}

// findUserShellEntry returns the transcript index for the running
// user-shell block matching jobID, or -1 if none. Used both by the
// EvtUserShellEnd case (fold final state into the running block) and
// by the Subscribe-pump path that streams live output chunks.
func (m *Model) findUserShellEntry(jobID string) int {
	for i := len(m.transcript) - 1; i >= 0; i-- {
		if m.transcript[i].kind == entryUserShell && m.transcript[i].shellJobID == jobID {
			return i
		}
	}
	return -1
}

// submit captures the current textarea value and routes it to the
// correct handler. Pre-check order is load-bearing:
//
//  1. Trim. Whitespace-only input is a no-op (don't write a row).
//  2. Slash check via internal/tui/slash.Parse. Slash commands NEVER
//     become model-bound messages; they are TUI directives.
//  3. Otherwise, append a user_message event.
//
// The textarea is cleared on every non-empty submit, regardless of
// path, so the user sees the input area reset to a fresh prompt.
//
// All side-effects run inside the returned tea.Cmd so this function
// stays testable in isolation.
func (m *Model) submit() tea.Cmd {
	raw := strings.TrimSpace(m.ta.Value())
	if raw == "" {
		return nil
	}

	// Phase U: "!cmd" submissions short-circuit to the user-shell
	// path before slash or model routing.
	if isShellSubmission(raw) {
		m.ta.Reset()
		return m.submitUserShellCmd(extractShellCommand(raw), usershell.Foreground)
	}

	m.ta.Reset()

	cmd, err := slash.Parse(raw)
	if err == nil {
		return m.dispatchSlash(cmd)
	}
	if !errors.Is(err, slash.ErrNotSlash) {
		// slash.Parse only returns ErrNotSlash today, but surface any
		// future shape defensively rather than treating malformed
		// slash input as a model message.
		return func() tea.Msg {
			return errMsg{err: fmt.Errorf("slash parse: %w", err)}
		}
	}
	return m.appendUserMessage(raw)
}

// submitBackgroundShell extracts the "!cmd" body and submits it as
// a Background job. Wired to Ctrl+Enter (ctrl+@). The textarea is
// cleared on this path so the user can keep typing.
func (m *Model) submitBackgroundShell() tea.Cmd {
	raw := strings.TrimSpace(m.ta.Value())
	if !isShellSubmission(raw) {
		return nil
	}
	m.ta.Reset()
	return m.submitUserShellCmd(extractShellCommand(raw), usershell.Background)
}

// dispatchSlash routes a parsed slash command. For Slice 1e, the only
// commands wired to real behavior are `/exit` (quit) and `/clear`
// (drop the in-memory transcript without touching the log, per SPEC:
// "the conversation persists in the event log"). Everything else echoes
// a status line so the user can see the verb was recognized. Slice 1f
// (or whichever later slice owns each verb) wires the rest.
func (m *Model) dispatchSlash(c slash.Command) tea.Cmd {
	switch c.Name {
	case "exit", "quit", "q":
		m.quitting = true
		if m.subCancel != nil {
			m.subCancel()
		}
		return tea.Quit
	case "clear":
		// Drop the visual transcript immediately so the user sees
		// the screen empty on the very next render. The session-
		// reset event is the durable signal: chatglue's history
		// projection drops everything before this event so the
		// MODEL also forgets, and a chat reload picks up the same
		// reset marker on backfill. Without the marker, the model
		// would keep talking about the old conversation on the
		// next "hi" — exactly the bug we hit in field testing.
		m.transcript = nil
		m.rerenderViewport()
		log, ok := m.log.(*agent.SQLiteEventLog)
		agentID := m.agentID
		return func() tea.Msg {
			if ok {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_, err := log.Append(ctx, agent.Event{
					AgentID: agentID,
					TS:      time.Now().UTC(),
					Type:    agent.EvtSessionReset,
					Payload: []byte("{}"), // schema: payload NOT NULL
				})
				if err != nil {
					return statusMsg{text: "clear: append reset failed: " + err.Error(), kind: statusWarn}
				}
			}
			return statusMsg{text: "conversation cleared (history reset for model too)", kind: statusInfo}
		}
	case "help":
		// Slice 9d: full overlay panel instead of the one-line echo.
		// Any keystroke (including /help again) dismisses it.
		m.showHelp = true
		m.rerenderViewport()
		return nil
	case "model":
		return func() tea.Msg { return statusMsg{text: slashModelLine(c.Args), kind: statusInfo} }
	case "agents":
		// Slice 7g: hand off to the manage TUI. We can't run two
		// bubbletea Programs simultaneously, so chat quits + the
		// caller (cmd/carlos) reads OpenManageRequested() and
		// relaunches into manage.Model. The unified single-program
		// TUI that hosts both is a future slice.
		m.openManage = true
		m.quitting = true
		if m.subCancel != nil {
			m.subCancel()
		}
		return tea.Quit
	case "schedule":
		// Phase 8b: /schedule list|add|rm edits ~/.carlos/config.yaml
		// directly. The daemon picks up the change on its next
		// SIGHUP / reload (no need to be running for the slash
		// commands themselves to work).
		return func() tea.Msg { return statusMsg{text: handleScheduleSlash(c.Args), kind: statusInfo} }
	case "research":
		// Phase 11 slice 11f: synchronous-for-v0 entry point into the
		// research orchestrator engine. The handler returns a status
		// line immediately + spawns a goroutine that calls
		// engine.Run; on completion an EvtAssistantMessage lands in
		// the log and surfaces as a 🧢 transcript entry via the
		// normal subscription pump.
		//
		// Phase 11 slice 11e: when a ResearchSpawner is ALSO wired,
		// prefer the async path. SpawnResearch runs the engine as a
		// real sub-agent so the chat stays interactive; phase events
		// stream into an in-place entryResearchProgress row via the
		// subscription pump + applyEvent.
		q := strings.TrimSpace(c.Args)
		if q == "" {
			return func() tea.Msg {
				return statusMsg{text: "usage: /research <question>", kind: statusWarn}
			}
		}
		if m.researchEngine == nil {
			return func() tea.Msg {
				return statusMsg{
					text: "/research: research engine not wired (no provider configured?)",
					kind: statusWarn,
				}
			}
		}
		if m.spawner != nil {
			return m.runResearchAsync(q)
		}
		return m.runResearchCmd(q)
	case "compact":
		// Phase 9 slice 9j: Claude Code parity. Summarize the chat's
		// conversation and reset the model's context to the summary,
		// freeing space for new turns. /clear is for "forget
		// everything"; /compact is for "remember the gist, drop the
		// details". When no summarizer is configured the verb echoes a
		// friendly hint rather than failing.
		if m.summarizer == nil {
			return func() tea.Msg {
				return statusMsg{
					text: "compact requires an LLM-backed summarizer; not configured",
					kind: statusWarn,
				}
			}
		}
		return m.runCompactCmd()
	}
	if _, ok := slash.Lookup(c.Name); ok {
		return func() tea.Msg {
			return statusMsg{
				text: fmt.Sprintf("slash command: /%s (handler pending — slice 1f)", c.Name),
				kind: statusInfo,
			}
		}
	}
	return func() tea.Msg {
		return statusMsg{
			text: fmt.Sprintf("unknown command: /%s — try /help", c.Name),
			kind: statusWarn,
		}
	}
}

// handleScheduleSlash is the chat-side handler for the /schedule slash
// verb (Phase 8b). It returns a one-line status string suitable for
// the status bar. Three forms:
//
//	/schedule list                              → enumerate configured schedules
//	/schedule add "<when>" <prompt words...>    → append a new schedule
//	/schedule rm <name>                         → remove by name
//
// Edits happen against ~/.carlos/config.yaml via the existing atomic
// writer (config.Save). The daemon picks up the change on its next
// SIGHUP / IPC reload — neither is needed for /schedule itself to work,
// since the slash command only edits config.
func handleScheduleSlash(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return "usage: /schedule list | add \"<when>\" <prompt...> | rm <name>"
	}
	verb, rest, _ := strings.Cut(args, " ")
	cfgPath := config.DefaultPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return "schedule: load config: " + err.Error()
	}
	switch verb {
	case "list":
		if len(cfg.Schedules) == 0 {
			return "no schedules configured"
		}
		names := make([]string, 0, len(cfg.Schedules))
		for _, s := range cfg.Schedules {
			names = append(names, s.Name+"("+s.Spec+")")
		}
		return "schedules: " + strings.Join(names, ", ")
	case "add":
		when, prompt, found := splitWhenPrompt(strings.TrimSpace(rest))
		if !found {
			return `schedule add: usage — /schedule add "<when>" <prompt...>`
		}
		sch, err := schedule.ParseNatural(when)
		if err != nil {
			return "schedule add: " + err.Error()
		}
		sch.Prompt = prompt
		sch.Name = autoSlugName(prompt)
		if err := sch.Validate(); err != nil {
			return "schedule add: " + err.Error()
		}
		cfg.Schedules = append(cfg.Schedules, sch)
		if err := config.Save(cfgPath, cfg); err != nil {
			return "schedule add: save config: " + err.Error()
		}
		return fmt.Sprintf("added %q → %s", sch.Name, sch.Spec)
	case "rm":
		name := strings.TrimSpace(rest)
		if name == "" {
			return "schedule rm: name required"
		}
		out := cfg.Schedules[:0]
		removed := false
		for _, s := range cfg.Schedules {
			if s.Name == name {
				removed = true
				continue
			}
			out = append(out, s)
		}
		if !removed {
			return "schedule rm: no schedule named " + name
		}
		cfg.Schedules = out
		if err := config.Save(cfgPath, cfg); err != nil {
			return "schedule rm: save config: " + err.Error()
		}
		return "removed " + name
	default:
		return `usage: /schedule list | add "<when>" <prompt...> | rm <name>`
	}
}

// splitWhenPrompt extracts the quoted "<when>" prefix and the remaining
// prompt from /schedule add input. Accepts either:
//
//	"every weekday at 9am" summarize my unread Slack DMs
//	every-weekday-at-9am   summarize my unread Slack DMs  (no quotes; first token = when)
//
// Returns ("", "", false) if the prompt is empty.
func splitWhenPrompt(s string) (when string, prompt string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	if strings.HasPrefix(s, "\"") {
		end := strings.Index(s[1:], "\"")
		if end < 0 {
			return "", "", false
		}
		when = s[1 : end+1]
		prompt = strings.TrimSpace(s[end+2:])
	} else {
		when, prompt, _ = strings.Cut(s, " ")
	}
	when = strings.TrimSpace(when)
	prompt = strings.TrimSpace(prompt)
	if when == "" || prompt == "" {
		return "", "", false
	}
	return when, prompt, true
}

// autoSlugName mirrors cmd/carlos.autoScheduleName: a short slug of
// the prompt + a 4-digit timestamp suffix. Duplicated here rather than
// imported to keep the chat package independent of cmd/carlos.
func autoSlugName(prompt string) string {
	var b strings.Builder
	prev := byte(0)
	for i := 0; i < len(prompt) && b.Len() < 20; i++ {
		c := prompt[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			b.WriteByte(c)
		default:
			if prev != '-' && b.Len() > 0 {
				b.WriteByte('-')
				c = '-'
			} else {
				continue
			}
		}
		prev = c
	}
	slug := strings.TrimRight(b.String(), "-")
	if slug == "" {
		slug = "sched"
	}
	return slug + "-" + fmt.Sprintf("%04d", time.Now().UnixNano()%10000)
}

// slashHelpLine returns a compact one-line summary of the slash
// vocabulary, suitable for the status line. The full /help screen is
// a later-slice concern.
func slashHelpLine() string {
	names := make([]string, 0, len(slash.Builtins))
	for _, b := range slash.Builtins {
		names = append(names, "/"+b.Name)
	}
	return "available: " + strings.Join(names, " ")
}

// slashModelLine handles `/model [provider:model]`. Phase 2e scope:
// with no args, list configured providers + their default models from
// the user's config. With args, acknowledge — actual mid-session
// switching needs Phase 3's spawn/provider plumbing in the chat TUI's
// own loop (which doesn't exist yet; the dev-aid chat view is read-
// only on the event log, no provider attached). For headless
// `carlos please`, use the `--provider` / `--model` flags instead.
func slashModelLine(arg string) string {
	if arg != "" {
		return fmt.Sprintf("/model: mid-session switching wires in Phase 3 (chat loop). For now, restart with carlos please --provider <name> --model <id>. (requested: %s)", arg)
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return "/model: no config loaded (" + err.Error() + ")"
	}
	if len(cfg.Providers) == 0 {
		return "/model: no providers configured — run `carlos onboard`"
	}
	parts := make([]string, 0, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		mark := " "
		if name == cfg.DefaultProvider {
			mark = "*"
		}
		model := pc.DefaultModel
		if model == "" {
			model = "(no default)"
		}
		parts = append(parts, fmt.Sprintf("%s%s=%s", mark, name, model))
	}
	return "configured: " + strings.Join(parts, "  ") + "   (* = default)"
}

// appendUserMessage writes a user_message event to the log. The event
// flows back through the subscription → eventMsg path and lands in the
// transcript via applyEvent — so we do NOT touch the local transcript
// here. Single source of truth: render only what the log has accepted.
func (m *Model) appendUserMessage(text string) tea.Cmd {
	agentID := m.agentID
	log := m.log
	return func() tea.Msg {
		payload, err := json.Marshal(agent.MessagePayload{Text: text})
		if err != nil {
			return errMsg{err: fmt.Errorf("marshal user_message: %w", err)}
		}
		ev := agent.Event{
			AgentID: agentID,
			TS:      time.Now().UTC(),
			Type:    agent.EvtUserMessage,
			Payload: payload,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := log.Append(ctx, ev); err != nil {
			return errMsg{err: fmt.Errorf("append user_message: %w", err)}
		}
		// The subscription pump will deliver the event back to us;
		// returning nil means "no follow-up message".
		return nil
	}
}

