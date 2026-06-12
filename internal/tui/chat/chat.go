package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/clipboard"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/memory"
	"github.com/georgebuilds/carlos/internal/schedule"
	"github.com/georgebuilds/carlos/internal/theme"
	"github.com/georgebuilds/carlos/internal/tui/slash"
	"github.com/georgebuilds/carlos/internal/tui/termscrub"
	"github.com/georgebuilds/carlos/internal/usershell"
	"github.com/georgebuilds/carlos/internal/workspace"
)

// Brand palette - package-level vars populated by [ApplyPalette].
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

	// glamourStyle is the markdown-renderer style we hand to glamour
	// at TermRenderer construction. Set at boot from the same
	// theme.Variant the rest of the palette is built from, so the
	// renderer never has to query the terminal at runtime (which is
	// the bug behind the v0.7.1 "weird characters appear in the
	// textarea after a long thinking pause" report — glamour's
	// WithAutoStyle invokes termenv background-color detection,
	// which fires an OSC 11 query against the terminal; in tabbed
	// Ghostty the response arrived after the alt-screen was up and
	// was read as keystrokes by the textarea).
	glamourStyle = "dark"
)

func init() {
	ApplyPalette(theme.Load(theme.Options{}))
}

// ApplyPalette wires a freshly-loaded [theme.Palette] into the chat
// package's color vars. Call once at startup from cmd/carlos after the
// user config is loaded. Idempotent and safe to call again on config
// reload (no concurrency guarantee - TUI startup is single-threaded).
func ApplyPalette(p theme.Palette) {
	colorAccent = p.Accent
	colorMuted = p.Muted
	colorUser = p.User
	colorAgent = p.Agent
	colorTool = p.Tool
	colorWarn = p.Warn
	colorOK = p.OK
	colorSubtle = p.Subtle
	glamourStyle = glamourStyleFor(p.Variant)
}

// glamourStyleFor maps carlos's theme variant to a glamour standard
// style name. Pinning the style at boot (rather than letting glamour
// auto-detect at every renderer construction) avoids the OSC 11
// background-color query that termenv triggers under WithAutoStyle.
// "notty" is a deliberate choice for NO_COLOR / non-TTY environments
// because it skips ANSI styling entirely.
func glamourStyleFor(v theme.Variant) string {
	switch v {
	case theme.Light:
		return "light"
	case theme.Dark:
		return "dark"
	default:
		return "notty"
	}
}

// Minimum terminal size. Chat is a working surface, not a poster - the
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
	isSkill    bool   // call invokes a skill (tool=="skill_use"); strip renders with 📚 chip instead of the generic 🔧
	skillName  string // skill name parsed from the skill_use input JSON; empty when isSkill=false or input is malformed
	subAgentID string // collapse key for entryResearchProgress (slice 11e)

	// Sub-agent fields (tool=="agent"). When isAgent is true the entry
	// renders as its own bordered card via renderAgentCard instead of
	// folding into the activity strip with other tool calls. The
	// objective is parsed best-effort from the agent tool's input JSON;
	// a parse failure still tags isAgent=true so the row is peeled out
	// of the strip and the user sees a card with an empty body.
	isAgent        bool      // call spawns a sub-agent (tool=="agent")
	agentObjective string    // objective parsed from the agent tool input JSON; empty on parse failure
	toolCalledAt   time.Time // ev.TS of the EvtToolCall for elapsed display
	toolResultAt   time.Time // ev.TS of the matching EvtToolResult; zero while running

	// User-shell (Phase U S5) fields. Active when kind == entryUserShell.
	// shellJobID is the collapse key so EvtUserShellEnd folds into the
	// row created by EvtUserShellStart instead of appending a fresh row.
	// shellOutput streams in via the Manager Subscribe pump until the
	// End event lands the canonical inline-truncated copy.
	shellJobID        string
	shellCommand      string
	shellOutput       string
	shellExitCode     int
	shellDuration     time.Duration
	shellRunning      bool // true between Start and End
	shellCancelled    bool
	shellBackgrounded bool
	shellTruncated    int // bytes dropped from the inline output
	shellFailErr      string

	// attachments are the composer-chip payloads carried by a user
	// message (slice I-1). text keeps the raw markers; renderEntry
	// substitutes them with sigil+nickname chips at paint time via
	// displayChips, so replayed history shows the chip, not ‹p:ID›.
	attachments []agent.Attachment
}

// queuedUserMessage is one parked mid-turn submit: the raw message
// text (chip markers intact) plus the attachments serialized from the
// composer at submit time, so a queued chip message survives the wait
// with its payloads bound.
type queuedUserMessage struct {
	text string
	atts []agent.Attachment
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
	// entrySlashEcho is the persistent in-transcript echo for slash
	// commands whose output describes the session itself (/whoami so
	// far; /mode, /frame, /capabilities could move here later). The
	// pre-existing footer status row is a fleeting one-liner that
	// some terminals + theme combinations effectively swallow; an
	// inline transcript row stays scrollable and is impervious to
	// status-bar rendering quirks. Rendered with a "›" prefix in
	// the accent color so it reads as a user-initiated echo, not an
	// error or model reply.
	entrySlashEcho
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
	// entryError is a chatglue-surfaced loop or provider error
	// (network blip, persist failure, model 5xx). chatglue emits
	// these as EvtAssistantMessage events tagged with the
	// chatglue.ErrorEventPrefix marker; applyEvent reroutes them
	// here so they render in a bordered warn-color card instead of
	// leaking through the regular assistant-turn renderer prefixed
	// with "carlos: …" as a normal-looking reply.
	entryError
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

	// typeRevealed is the typewriter reveal cursor (slice 9b): how many
	// runes of the live TextSource buffer are visible right now.
	// Advanced with catch-up pacing by advanceTypewriter on every
	// textTickMsg; reset to 0 when the buffer empties (turn sealed).
	typeRevealed int

	// Replay-derived state. Apply on every event so the projection stays
	// current; transcript is the rendered side-effect.
	proj       *agent.Projection
	transcript []transcriptEntry

	// Bubbles components.
	vp viewport.Model
	ta textarea.Model

	// composer wraps ta with inline-chip (attachment) bookkeeping -
	// slice I-1. Holds a POINTER to ta, so the existing m.ta call
	// sites stay the single source of truth for the value + cursor.
	// nil in tests that build a bare Model{}; every Composer method
	// is nil-receiver safe.
	composer *Composer

	// clip is the image-clipboard probe behind the ctrl+v intercept
	// (slice I-3). New defaults it to clipboard.System(), which is
	// lazy and headless-safe; tests inject a clipboard.Fake via
	// WithClipboard. nil (bare Model, explicit override) disables the
	// probe entirely so ctrl+v stays a pure text paste.
	clip clipboard.Reader

	// visionProbe answers "can the CURRENT provider read images?" -
	// the slice-I-3 capability gate. Resolved live on every render
	// (never cached at boot) so /model swaps and frame switches flip
	// the image-chip warn treatment immediately. nil assumes vision:
	// the gate is cosmetic and the chatglue bridge degrades safely
	// server-side either way. Wired by cmd/carlos via WithVisionProbe.
	visionProbe func() bool

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

	// startupNotices are informational lines surfaced once at startup
	// (e.g. "recovered 2 orphaned agent(s)", "mcp: registered 5
	// tool(s)"). Rendered as a small info banner in the footer above
	// the keybind row so the user sees boot-time results inside the TUI
	// rather than on stderr (which would corrupt the alt-screen frame).
	// Nil/empty renders nothing. Set via WithStartupNotices.
	startupNotices []string

	// diag is a best-effort sink for rare diagnostics that would
	// otherwise be lost (e.g. a non-fatal prune error in /resume).
	// Defaults to io.Discard — NEVER os.Stderr, since writing to stderr
	// corrupts the live alt-screen frame. Set via WithDiagWriter.
	diag io.Writer

	// firstRenderHook fires exactly once, when the first View()
	// composes — the closest in-process proxy for "first bubbletea
	// frame" (the renderer writes the frame to the terminal right
	// after View returns). Consumed (nil-ed) on fire. Set via
	// WithFirstRenderHook; cmd/carlos uses it for the CARLOS_BOOT_TRACE
	// first_frame checkpoint (slice 9f). nil = no-op.
	firstRenderHook func()

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
	// Defaults to "Boss" - matches carlos's brand voice. Override
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
	// echoes a "not configured" statusMsg rather than failing - the
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

	// Phase U S6: jobs overlay (Ctrl+J). When true, a bordered
	// palette panel sits above the input listing every shell job
	// grouped by state. jobsCursor is the highlighted row in the
	// flattened list; jobsFilter mirrors the picker pattern with
	// "/" entering filter-mode.
	showJobs       bool
	jobsCursor     int
	jobsFilter     string
	jobsFilterMode bool

	// Phase U S7: separate shell-history file walked via ↑/↓ when
	// the composer is in shell mode (input starts with "!"). nil
	// disables - composer falls back to letting the textarea
	// handle arrow keys natively (cursor up/down within
	// multi-line input).
	shellHistory *usershell.History

	// Chat-input history: ↑/↓ walk previously-submitted user
	// messages (sourced from the in-memory transcript so it composes
	// with backfill / resume). chatHistoryCursor == -1 means "not
	// walking"; 0+ is an index into chatHistoryEntries(). On first
	// ↑ the current composer text is stashed in chatHistoryDraft so
	// stepping past the most-recent entry back ↓ restores it. See
	// chat_history.go.
	chatHistoryCursor int
	chatHistoryDraft  string

	// Phase T-2: workspace-trust policy for /trust + /untrust +
	// /trusts slash commands. nil means trust isn't wired (tests,
	// the headless `please` path); the slashes echo a "not wired"
	// status when missing. The Policy is shared with the
	// LayeredApprover in the chat path, so SetTrusted updates
	// here reach the next tool-call decision.
	workspace *workspace.Policy

	// Phase T-3: /permissions overlay state. showPerms toggles via
	// the slash; permsTab is the active tab (Built-in / Workspace);
	// permsCursor is the highlighted row within the tab + filter;
	// permsFilter / permsFilterMode mirror the jobs-overlay sub-REPL.
	showPerms       bool
	permsTab        permsTab
	permsCursor     int
	permsFilter     string
	permsFilterMode bool

	// Phase F: per-frame view + switch hook. frame is the resolved
	// active frame for this session; surface in the header pill and
	// served read-only to /frame. frame.SwitchActive is a callback
	// the slash dispatch uses to persist a switch; nil means /frame
	// switch echoes "not wired" rather than failing. The full
	// mid-session provider/model swap is its own slice - for now a
	// switch updates the persisted active and prints a hint to
	// restart for the new provider/model to take effect.
	frame FrameUI

	// Phase F-5: takeover frame switcher overlay (Ctrl+F). When
	// showFrameSwitcher is true the chat content dims and a 3x2 grid
	// of tiles renders above the transcript. switcherCursor is the
	// focused tile (index into frame.Available); switcherPage flips
	// between visible windows when more frames exist than tiles. The
	// in-overlay ? toggle expands switcherHelp into a verbose
	// keymap line.
	showFrameSwitcher bool
	switcherCursor    int
	switcherPage      int
	switcherHelp      bool

	// Mode switcher overlay (Ctrl+O). Mirrors the frame switcher but
	// renders a single row of 3 cards (tight / solo / orchestrator).
	// modeSwitcherCursor is the focused card index; modeSwitcherHelp
	// toggles the verbose footer the same way switcherHelp does.
	showModeSwitcher   bool
	modeSwitcherCursor int
	modeSwitcherHelp   bool

	// Header pill click hitboxes. Re-computed by renderHeader every
	// frame; consumed by the tea.MouseMsg branch so clicking the
	// frame pill opens the frame switcher and clicking the mode pill
	// opens the mode switcher. Empty (both 0) means the pill wasn't
	// rendered this frame and clicks at that cell are no-ops.
	// Columns are 0-indexed terminal cells in the View output.
	framePillColStart int
	framePillColEnd   int
	modePillColStart  int
	modePillColEnd    int

	// Phase F-8 cwd-hint footer state. footerHint is the text shown
	// when an in-band `!cd` lands the user in a path that matches a
	// non-active frame's cwd_hints. hintsLocked is set by Ctrl+L for
	// the rest of the session so the hint stops bothering the user
	// on repeated cd's. hintSeen tracks "once per unrecognized path"
	// so the hint doesn't redraw on every `!ls` after the cd.
	footerHint  string
	hintsLocked bool
	hintSeen    map[string]bool

	// Phase F-10: new-frame wizard overlay. When showNewFrame is true
	// a form panel takes over the switcher slot so the user can compose
	// a fresh Frame and persist it via FrameUI.AddFrame.
	showNewFrame    bool
	newFrame        frame.Frame
	newFrameField   int
	newFrameAccent  int  // index into frame.AccentPalette
	newFrameCopy    bool // true = copy personal; false = blank
	newFrameGlyphEd bool // user touched the glyph field
	newFrameError   string

	// Phase T-2 follow-on: first-launch trust prompt.
	showFirstTrust      bool
	firstTrustDismissed bool

	// queuedCmds carries tea.Cmds queued by overlay handlers that need
	// to run on the next Update tick.
	queuedCmds []tea.Cmd

	// queuedUserMessages holds user messages submitted while the
	// assistant was mid-turn (streaming, tool-calling, or in the
	// in-flight Spawning / Running / Compacting projection states).
	// FIFO. submit() pushes here when assistantBusy() is true instead
	// of silently dropping the input; flushQueuedUserMessage pops one
	// entry per assistant-idle tick so each queued message gets its
	// own turn. Each entry carries the raw text (chip markers intact)
	// plus the chip attachments serialized at submit time.
	queuedUserMessages []queuedUserMessage

	// Inline sub-agent panel: childrenView is the supervisor-scoped
	// reader, nil disables the panel entirely; childrenSnap is the
	// latest 250ms snapshot.
	childrenView ChildrenView
	childrenSnap []ChildSnapshot

	// slashSuggest is the live autocomplete state for the composer
	// when the value starts with "/". Refreshed on every keystroke
	// in Update; consumed by renderInput (inline ghost text on the
	// input row) and renderInner (thin hint band above the
	// separator). Zero value = closed; refresh derives "open" from
	// the textarea value so we never go out of sync.
	slashSuggest slashSuggest

	// mentionSuggest is the @file-mention sibling of slashSuggest
	// (slice I-4): live fuzzy file completion while the cursor sits in
	// an @token. Refreshed alongside slashSuggest via refreshSuggests;
	// consumed by renderInput (hint band) + handleMentionSuggestKey.
	mentionSuggest mentionSuggest

	// mentionIdx / mentionVaultIdx are the lazily-built candidate file
	// indexes behind mention autocomplete (cwd tier and @vault/ tier).
	// nil until the first "@" keystroke; refreshed on a short TTL. See
	// mention.go for the walk bounds.
	mentionIdx      *mentionIndex
	mentionVaultIdx *mentionIndex

	// mentionRoot overrides the cwd-tier walk root; "" (production)
	// means os.Getwd at index-build time. Tests point it at a fixture
	// tree.
	mentionRoot string

	// vaultPath is the configured Obsidian vault root for the @vault/
	// mention tier. "" disables the tier (the "vault/" query prefix
	// then falls through to plain cwd completion). Set via
	// WithVaultPath from cfg.Vault.Path.
	vaultPath string

	// thinkingTick advances on every textTickMsg so the "carlos is
	// thinking" activity indicator at the bottom of the transcript
	// animates. We don't reset it on state transitions — modular
	// arithmetic on the frame index keeps the animation phase stable
	// across the start/stop of waits, which avoids a visible "jump"
	// when the indicator reappears mid-conversation.
	thinkingTick int

	// Markdown renderer for assistant messages. Lazily built on the
	// first render that needs it and rebuilt when the viewport width
	// changes. nil means "fall back to plain rendering"; see
	// internal/tui/chat/markdown.go for the rationale.
	markdown       *glamour.TermRenderer
	markdownWidth  int

	// mouseOff toggles bubbletea's mouse capture. When TRUE we've
	// emitted tea.DisableMouse so the terminal owns the cursor again
	// and the user can drag-select transcript text for copy. Off
	// trade-off: the viewport's mouse-wheel scroll stops working
	// (the alt-screen ignores wheel events without capture). The
	// keybind to flip this is Alt+M; without it, users on Ghostty
	// (and most modern terminals where Shift+drag does NOT pass
	// through) had no way to copy carlos's responses.
	mouseOff bool

	// /resume picker state. showResume gates the takeover overlay;
	// resumeSessions is the list loaded from the SQLite log on
	// open; resumeCursor is the focused card; resumeSelected is
	// the picked session id (empty until Enter, populated when the
	// outer loop should swap in that agent id). All defined in
	// overlay_resume.go but lives here so the Model owns the only
	// authoritative state slot.
	showResume     bool
	resumeSessions []resumeSession
	resumeCursor   int
	resumeSelected string
}

// FrameUI is the Phase F display + switch contract the chat Model
// consumes. Pulled out of internal/frame so chat doesn't have to know
// about Config - it just needs the active frame's render fields plus
// the list of names to offer in /frame list.
type FrameUI struct {
	// Active is the name of the frame this session resolved to. Empty
	// disables the header pill (legacy single-shelf mode).
	Active string
	// Glyph is the single-character symbol painted in Accent in the
	// header pill. Empty falls back to internal/frame.DefaultGlyphFor.
	Glyph string
	// Accent is one of the curated palette names; empty disables color.
	Accent string
	// Available is the ordered list of frame names for /frame list.
	Available []string
	// SwitchActive persists a frame switch (writes config.yaml). When
	// nil, /frame switch echoes "not wired"; in the production wire-up
	// cmd/carlos passes a closure that calls config.Save.
	SwitchActive func(name string) error
	// Capabilities maps capability name (e.g. "calendar") to the
	// backend selected in this frame (e.g. "caldav"). Populated from
	// the active frame's Capabilities config at boot. Surfaced by the
	// /capabilities slash; empty map is fine and prints a hint.
	Capabilities map[string]string
	// Mode is the orchestrator-mode of the active frame: one of
	// "solo", "tight", "orchestrator". Empty falls back to "solo" at
	// render time. Surfaced in the chat header pill and as the answer
	// to /mode (no arg). The /mode <name> form rewrites the active
	// frame's mode through the SwitchMode hook.
	Mode string
	// SwitchMode persists a mode change on the active frame. nil
	// makes /mode <name> echo "not wired" rather than failing.
	SwitchMode func(mode string) error
	// MatchCwd resolves a cwd to a frame name when one of the
	// configured frames' cwd_hints matches the path. Returns "" when
	// nothing matches or when the match is the active frame. Used by
	// the Phase F-8 in-band `cd` interception to surface a footer
	// hint suggesting Ctrl+F. nil disables the hint entirely.
	MatchCwd func(cwd string) string
	// AddFrame appends a new frame to the user's config and persists
	// the change. Wired by the Phase F-10 new-frame wizard (Ctrl+F →
	// switcher → "+ new frame" tile, or `n` while the switcher is
	// open, or `/frame new [name]`). nil makes the wizard echo "not
	// wired" rather than failing.
	AddFrame func(f frame.Frame) error
	// RefreshAvailable returns the current authoritative list of
	// frame names from the live config. When non-nil the frame
	// switcher calls it on every open so a frame created via the
	// wizard, the slash command, or an out-of-band config edit shows
	// up without an app restart. Old behavior (snapshot the list at
	// boot) is preserved when this is nil — handy for tests that
	// don't want to wire the hook.
	RefreshAvailable func() []string
	// PersonalTemplate returns the field bundle the new-frame wizard
	// uses when the user picks "copy personal" on the start-from
	// toggle. Returning a zero Frame is fine - the wizard treats that
	// the same as "blank". nil is also fine; the wizard hides the
	// copy-personal option and falls back to blank.
	PersonalTemplate func() frame.Frame
	// Identity returns the current provider + model strings for
	// /whoami. Surfaced as the third line of the slash echo so users
	// can confirm the live-swap actually flipped the dispatch.
	Identity func() (provider, model string)
	// LookupFrame returns the render fields (glyph, accent, mode,
	// capabilities) for a frame name. frameSwitchCmd calls this after
	// a successful SwitchActive so the chat's in-process FrameUI
	// reflects the new frame's settings without waiting for the next
	// session. nil disables the refresh - Mode + Capabilities stay
	// what they were until the next restart.
	LookupFrame func(name string) (FrameUIUpdate, bool)
	// SwitchModel swaps the provider + model used for the next assistant
	// turn. Wired by /model <provider:model>. The runtime closure
	// rebuilds the chatglue.Loop with the new dispatch and atomically
	// swaps it so the user's NEXT message goes through the new model.
	// nil makes /model echo "not wired"; provider empty means "keep the
	// current provider, just change the model". Returns the resolved
	// (provider, model) pair so the slash echo can surface the actual
	// applied identity (handy when /model was passed bare).
	SwitchModel func(provider, model string) (string, string, error)
	// ModelCompletions returns the suggestion list for /model
	// autocomplete. For an empty partial it returns the configured
	// provider list with a ":" suffix. For "<provider>:" or
	// "<provider>:<frag>" it returns models known for that provider
	// (default model, plus cached OpenRouter catalog when available).
	// The empty list cleanly disables the popup; tests + the dev-aid
	// loop can leave this nil without further wiring.
	ModelCompletions func(partial string) []string
	// SkillsCatalog returns a lightweight projection of the loaded
	// skill library for the active frame: name + description +
	// optional backend. Wired by /skills list. Nil leaves the slash
	// echoing "not wired"; the skill_use tool still works because
	// it's tool-side state, not chat-side.
	SkillsCatalog func() []SkillCatalogEntry
}

// SkillCatalogEntry is the minimal projection of a skill the
// /skills list echo needs. Keeps the chat package free of an
// internal/skills import (mirrors the SkillSummary pattern in
// internal/agent/sysprompt.go).
type SkillCatalogEntry struct {
	Name        string
	Description string
	Backend     string
}

// FrameUIUpdate carries the post-switch render fields the chat refreshes
// in place when a frame switch succeeds. Sent through FrameUI.LookupFrame.
type FrameUIUpdate struct {
	Glyph        string
	Accent       string
	Mode         string
	Capabilities map[string]string
}

type statusKind int

const (
	statusInfo statusKind = iota
	statusWarn
	statusError
)

// Option configures a Model at construction time.
type Option func(*Model)

// WithReadOnly disables input handling - the chat view becomes a pure
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
// fine - the default "Boss" is the carlos voice; pass the user's real
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
// leaves the feature dormant - the composer treats "!ls" like any
// other text and the footer collapses to its idle branch.
// cmd/carlos.runDefault constructs the Manager scoped to the chat
// session's event log; tests pass either a real Manager with a
// fake runner or skip the option entirely.
func WithUserShell(mgr *usershell.Manager) Option {
	return func(m *Model) { m.usershell = mgr }
}

// WithShellHistory wires the shell-mode ↑/↓ history walker. Pair
// with WithUserShell; nil disables shell history without affecting
// any other behavior. cmd/carlos.runDefault constructs a History
// rooted at ~/.carlos/shell-history.
func WithShellHistory(h *usershell.History) Option {
	return func(m *Model) { m.shellHistory = h }
}

// WithWorkspacePolicy attaches the Phase T-2 workspace-trust policy so
// the /trust + /untrust + /trusts slash commands can flip the in-
// session trust state. The Policy should be the same instance wired
// into the LayeredApprover; setting trust here also updates the
// approver's view for the very next tool call. nil leaves the
// commands echoing "not wired".
func WithWorkspacePolicy(p *workspace.Policy) Option {
	return func(m *Model) { m.workspace = p }
}

// WithFrame surfaces the Phase F active-frame state in the chat header
// and powers the /frame slash command. When ui.Active is empty the
// header pill is suppressed entirely (legacy single-shelf mode).
func WithFrame(ui FrameUI) Option {
	return func(m *Model) { m.frame = ui }
}

// WithClipboard replaces the image-clipboard probe behind the ctrl+v
// intercept (slice I-3). New defaults to clipboard.System(); tests
// inject a clipboard.Fake for deterministic image pastes. Passing nil
// disables the probe entirely - ctrl+v degrades to the textarea's
// stock text paste.
func WithClipboard(r clipboard.Reader) Option {
	return func(m *Model) { m.clip = r }
}

// WithVisionProbe wires the live "can the current provider read
// images?" answer for the slice-I-3 capability gate. The probe is
// called at render time (not cached), so the production closure
// should read the CURRENT dispatch under its own lock - that way
// /model swaps and frame switches update the image-chip warn
// treatment without restarting the chat. nil (the default) assumes
// vision and never warns.
func WithVisionProbe(probe func() bool) Option {
	return func(m *Model) { m.visionProbe = probe }
}

// WithVaultPath wires the configured Obsidian vault root (cfg.Vault.
// Path) into @file mention autocomplete's opt-in "@vault/..." tier
// (slice I-4). An empty path (no vault configured) leaves the tier
// off; the "vault/" query prefix then completes from the cwd like any
// other text.
func WithVaultPath(path string) Option {
	return func(m *Model) { m.vaultPath = path }
}

// WithChildrenView wires the inline sub-agent panel. The chat polls
// cv.Snapshot at ~250ms while at least one child is live; the panel
// appears on the right when the inner width clears splitMinWidth and
// disappears as soon as the snapshot is empty. nil leaves the chat in
// its single-stack layout.
func WithChildrenView(cv ChildrenView) Option {
	return func(m *Model) { m.childrenView = cv }
}

// WithStartupNotices surfaces boot-time informational lines inside the
// TUI instead of on stderr (which would corrupt the alt-screen frame).
// Each notice is one short line such as "recovered 2 orphaned agent(s)"
// or "mcp: registered 5 tool(s)"; they render as a small info banner in
// the footer above the keybind row. A nil or empty slice renders
// nothing. cmd/carlos.runDefault collects these from startup recovery /
// mcp registration and passes them here.
func WithStartupNotices(notices []string) Option {
	return func(m *Model) { m.startupNotices = notices }
}

// WithDiagWriter routes rare best-effort diagnostics (e.g. a non-fatal
// /resume prune error) to w. The default when unset is io.Discard;
// callers must NOT pass os.Stderr — writing to stderr corrupts the live
// alt-screen frame. A test harness or a file sink is the intended target.
func WithDiagWriter(w io.Writer) Option {
	return func(m *Model) {
		if w != nil {
			m.diag = w
		}
	}
}

// WithFirstRenderHook registers fn to run exactly once, at the end of
// the first View() call — i.e. as the first frame is composed, just
// before bubbletea's renderer writes it to the terminal. The hook is
// consumed on fire, so later Views (and chat⇄manage re-entries on a
// fresh Model) cost a single nil-check. cmd/carlos wires the slice-9f
// boot trace's first_frame checkpoint through this. A nil fn is a no-op.
func WithFirstRenderHook(fn func()) Option {
	return func(m *Model) { m.firstRenderHook = fn }
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
	ta.Placeholder = "type a message - enter to send, shift-enter for newline"
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
		log:               log,
		agentID:           agentID,
		source:            source,
		proj:              agent.NewProjection(),
		vp:                vp,
		ta:                ta,
		userName:          "Boss",
		chatHistoryCursor: -1,
		diag:              io.Discard,
	}
	// Wire AFTER the literal so the pointer targets the heap field
	// (the Model lives behind a *Model everywhere downstream).
	m.composer = NewComposer(&m.ta)
	// Image-paste probe (slice I-3): the real system clipboard by
	// default. Safe everywhere - clipboard.System() initializes
	// lazily on the first ctrl+v and degrades to "no image" on
	// headless sessions. Tests override with WithClipboard.
	m.clip = clipboard.System()
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
	// Mouse capture starts ON so the trackpad / mousewheel scrolls
	// the transcript out of the box (the field default users expect
	// from any modern TUI). Selection-for-copy/paste is a press
	// away: Alt+M releases capture so the terminal owns the cursor
	// for click-and-drag selection. Modern terminals (Ghostty,
	// iTerm2, WezTerm, macOS Terminal) also pass Shift+drag
	// through capture as a force-selection override, so users on
	// those don't even need the toggle. The footer carries a
	// permanent alt+m hint so the toggle is always discoverable.
	// WithFilter installs the global termscrub input filter so leaked
	// terminal escape remnants (mouse reports, DSR/DA/OSC replies,
	// bracketed-paste markers) are scrubbed out of every KeyRunes burst
	// before the textarea ever sees them. The post-update Scrub in
	// Update stays as defense-in-depth for sequences the parser splits
	// across reads and reassembles in the textarea value.
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(),
		tea.WithFilter(termscrub.FilterTerminalLeaks))
	return p.Run()
}

// OpenManageRequested returns true if the user exited via /agents and
// the caller should relaunch into the manage TUI.
func (m *Model) OpenManageRequested() bool { return m.openManage }

// Init kicks off backfill + subscription + the text ticker, plus the
// textarea cursor blink if we're accepting input.
func (m *Model) Init() tea.Cmd {
	m.initFirstTrustPrompt()
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
	if m.childrenView != nil {
		m.childrenSnap = m.childrenView.Snapshot()
		cmds = append(cmds, scheduleChildrenTick())
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
		// Header pill clicks: a left-button press on the rendered
		// frame pill or mode pill opens the matching switcher. The
		// hitbox columns are populated by renderHeader on the prior
		// frame; row 1 is the header line (border at row 0). When
		// the user has toggled mouse capture off via Alt+M we never
		// see clicks at all, so no opt-out is needed here.
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress && msg.Y == 1 {
			switch {
			case m.frame.Active != "" && msg.X >= m.framePillColStart && msg.X < m.framePillColEnd && m.framePillColEnd > m.framePillColStart:
				m.openFrameSwitcher()
				return m, nil
			case m.frame.Active != "" && msg.X >= m.modePillColStart && msg.X < m.modePillColEnd && m.modePillColEnd > m.modePillColStart:
				m.openModeSwitcher()
				return m, nil
			}
		}
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
		// Phase F-10: new-frame wizard sits above the switcher. While
		// it's up, every key (except ctrl+c) routes here so the form
		// edits aren't double-handled by the switcher.
		if m.showNewFrame {
			next, cmd, handled := m.handleNewFrameKey(msg)
			if handled {
				return next, cmd
			}
		}
		// Phase F-5: takeover frame switcher takes precedence over the
		// jobs / permissions overlays so Ctrl+F always reaches it. The
		// approval overlay is still modal above this - the model is
		// waiting and a frame switch shouldn't interrupt that.
		if m.showFrameSwitcher {
			next, cmd, handled := m.handleFrameSwitcherKey(msg)
			if handled {
				return next, cmd
			}
		}
		// Mode switcher: same modal precedence as the frame switcher so
		// nav keys reach the card-picker without the composer eating
		// them. ctrl+c still falls through.
		if m.showModeSwitcher {
			next, cmd, handled := m.handleModeSwitcherKey(msg)
			if handled {
				return next, cmd
			}
		}
		// /resume picker: same modal precedence as the frame switcher
		// so the user can navigate cards with ↑↓ + commit with Enter
		// without the composer eating keystrokes.
		if m.showResume {
			next, cmd, handled := m.handleResumeKey(msg)
			if handled {
				return next, cmd
			}
		}
		// Phase U S6: jobs overlay intercepts navigation + action
		// keys while open. Ctrl+C still falls through so the user
		// can quit even while browsing jobs.
		if m.showJobs {
			next, cmd, handled := m.handleJobsOverlayKey(msg)
			if handled {
				return next, cmd
			}
		}
		// Phase T-3: /permissions overlay. Same routing pattern:
		// the overlay swallows nav + action keys, ctrl+c falls
		// through to the quit handler.
		if m.showPerms {
			next, cmd, handled := m.handlePermsOverlayKey(msg)
			if handled {
				return next, cmd
			}
		}
		// First-launch trust prompt: y / n / esc. The handler queues
		// any tea.Cmd it needs to run; we drain queuedCmds below.
		if m.showFirstTrust {
			if m.handleFirstTrustKey(msg.String()) {
				var cmd tea.Cmd
				if len(m.queuedCmds) > 0 {
					cmd = tea.Batch(m.queuedCmds...)
					m.queuedCmds = nil
				}
				return m, cmd
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
			// terminal-reserved for cancel and we honor that - the
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
			// job" - only when the input starts with "!", else
			// fall through to the textarea so it stays a no-op.
			if m.readOnly {
				return m, nil
			}
			if hasShellPrefix(m.ta.Value()) {
				return m, m.submitBackgroundShell()
			}
		case "ctrl+j":
			// Phase U S6: toggle the jobs overlay. No-op when no
			// usershell manager is wired.
			if m.usershell != nil {
				m.showJobs = !m.showJobs
				if m.showJobs {
					m.jobsCursor = 0
					m.jobsFilter = ""
					m.jobsFilterMode = false
				}
				m.rerenderViewport()
				return m, nil
			}
		case "ctrl+f":
			// Phase F-5: toggle the takeover frame switcher. No-op
			// when frames aren't wired (legacy single-shelf mode);
			// the slash echo from /frame already covers that path.
			if m.frame.Active != "" {
				m.openFrameSwitcher()
				return m, nil
			}
		case "ctrl+o":
			// Mode switcher takeover. Ctrl+M would have been the
			// obvious mnemonic but it shares the keyCR byte with
			// Enter on macOS Terminal - see overlay_modes.go header
			// for the full rationale. No-op when frames aren't wired
			// since the mode field lives on the active frame.
			if m.frame.Active != "" {
				m.openModeSwitcher()
				return m, nil
			}
		case "ctrl+l":
			// Phase F-8: mute cwd-hint footer for the rest of the
			// session. Idempotent - second press just keeps the lock
			// on. No-op when no hint is wired.
			if m.frame.Active != "" && m.frame.MatchCwd != nil {
				m.lockCwdHints()
				m.rerenderViewport()
				return m, nil
			}
		case "alt+m":
			// Toggle bubbletea's mouse capture. Off lets the terminal
			// own the mouse again so users can drag-select carlos's
			// replies and copy them with Cmd+C / Ctrl+Shift+C — which
			// Ghostty (and any modern terminal that doesn't pass
			// Shift+drag through) otherwise blocks while capture is
			// on. On restores the viewport's mouse-wheel scroll. The
			// toggle is paired with a footer hint (renderFooter) so
			// the user knows which state they're in.
			m.mouseOff = !m.mouseOff
			m.rerenderViewport()
			if m.mouseOff {
				return m, tea.DisableMouse
			}
			return m, tea.EnableMouseCellMotion
		case "up":
			// Phase U S7: in shell mode, ↑ walks shell history
			// instead of moving the textarea cursor. Outside shell
			// mode the textarea handles it natively.
			if !m.readOnly && m.shellHistory != nil && hasShellPrefix(m.ta.Value()) {
				if prev := m.shellHistory.Prev(); prev != "" {
					m.ta.SetValue("!" + prev)
					m.ta.CursorEnd()
				}
				return m, nil
			}
			// Chat-input history: terminal-style recall of prior
			// user messages. Engages only when slash autocomplete
			// isn't owning the arrow (handled later by
			// handleSlashSuggestKey when the match list has 2+
			// entries) and the composer is single-line so the
			// textarea's native multi-line cursor nav still wins
			// when the user is mid-compose.
			if m.chatHistoryShouldEngage() &&
				!(m.slashSuggest.open && len(m.slashSuggest.matches) > 1) &&
				!(m.mentionSuggest.open && len(m.mentionSuggest.matches) > 1) {
				if m.chatHistoryUp() {
					return m, nil
				}
			}
		case "down":
			if !m.readOnly && m.shellHistory != nil && hasShellPrefix(m.ta.Value()) {
				next := m.shellHistory.Next()
				if next == "" {
					m.ta.SetValue("!")
				} else {
					m.ta.SetValue("!" + next)
				}
				m.ta.CursorEnd()
				return m, nil
			}
			if m.chatHistoryShouldEngage() &&
				!(m.slashSuggest.open && len(m.slashSuggest.matches) > 1) &&
				!(m.mentionSuggest.open && len(m.mentionSuggest.matches) > 1) {
				if m.chatHistoryDown() {
					return m, nil
				}
			}
		}
		// Slash-mode autocomplete intercepts Tab / ↑↓ / Esc before the
		// textarea sees them. Placed after the shell-history branches so
		// "!" mode keeps owning ↑↓; placed before the textarea route so
		// Tab actually completes instead of inserting whitespace.
		if cmd, handled := m.handleSlashSuggestKey(msg.String()); handled {
			return m, cmd
		}
		// @file mention autocomplete (slice I-4) mirrors the slash
		// intercept: Tab attaches the highlighted file as a ◇ chip,
		// ↑↓ navigate the match list, Esc dismisses. Mutually
		// exclusive with slash mode (a "/" value never mentions), so
		// ordering after the slash handler is belt-and-braces only.
		if cmd, handled := m.handleMentionSuggestKey(msg.String()); handled {
			return m, cmd
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
		// footer reflects current state. The startup-notice banner is
		// dismissed the same way: once the user starts typing they've
		// seen it, so reclaim the footer space.
		if m.status != "" {
			m.status = ""
		}
		if len(m.startupNotices) > 0 {
			m.startupNotices = nil
		}
		// Image paste (slice I-3): ctrl+v probes the clipboard for an
		// IMAGE before the textarea's own paste binding (a TEXT read
		// via atotto) can see the key. An image becomes one ▣ chip
		// backed by a content-addressed artifact; no image (headless
		// session included) falls through so text paste behaves
		// exactly as before. An artifact-write failure consumes the
		// key with a status-line error and inserts nothing - the
		// clipboard still holds the image, so the paste is never
		// silently lost.
		if msg.String() == "ctrl+v" && m.handleImagePaste() {
			m.refreshSuggests()
			return m, nil
		}
		// Large-paste clipping (slice I-2): bracketed paste delivers
		// the whole body as ONE KeyRunes msg with Paste=true (termscrub
		// passes it through untouched). Above the clip threshold the
		// paste becomes a single inline chip - attachment stored, only
		// the marker enters the textarea - whether the cursor is at the
		// end, mid-text, or inside an open slash-suggest band (the
		// refresh below keeps the band honest either way). Below the
		// threshold it falls through to the textarea and inserts as
		// plain text, exactly the pre-I-2 behavior.
		if msg.Paste && msg.Type == tea.KeyRunes && m.composer != nil {
			if pasted := normalizePaste(string(msg.Runes)); shouldClipPaste(pasted) {
				m.composer.InsertChip(agent.Attachment{
					Kind:     agent.AttachmentPaste,
					Nickname: clipNickname(pasted),
					Content:  pasted,
				})
				m.refreshSuggests()
				return m, nil
			}
		}
		// Chip atomicity (slice I-1): when the cursor is adjacent to an
		// inline chip, backspace/delete remove the whole chip and ←/→
		// hop over it - one keypress each, the chip behaves as a single
		// grapheme. Handled keys never reach the textarea.
		if m.composer.HandleKey(msg) {
			m.refreshSuggests()
			return m, nil
		}
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		// Defense-in-depth scrub of terminal escape remnants that
		// leak into the textarea when the terminal flushes a buffered
		// control sequence during a bubbletea state transition (e.g.
		// right after an alt+m toggle or while the alt-screen is
		// tearing down). The global WithFilter (termscrub) catches
		// clean single-burst leaks before they reach the model; this
		// post-update pass catches sequences the input parser splits
		// across reads and reassembles in the textarea value. termscrub
		// owns the patterns now and covers more than SGR mouse reports:
		// X11 mouse, DSR cursor-position and DA device-attributes
		// replies, OSC 10/11 color replies, and bracketed-paste markers.
		// Strip them in-place so the composer just shows whatever the
		// user actually typed.
		if cleaned := termscrub.Scrub(m.ta.Value()); cleaned != m.ta.Value() {
			m.ta.SetValue(cleaned)
			m.ta.CursorEnd()
		}
		// Re-derive chip refs from the (possibly edited) value and snap
		// the cursor out of any marker interior so the next keystroke
		// can't split a chip. No-op without live attachments.
		m.composer.Sync()
		// Refresh the slash-mode suggest state after every textarea
		// edit so the ghost text + hint band track what the user just
		// typed. refresh is a no-op when the input doesn't start with
		// "/", so the cost on the non-slash path is a single prefix
		// check.
		m.refreshSuggests()
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
		// Drain one queued mid-turn user message if the event we just
		// processed left the assistant idle. assistant_message and
		// chatglue's error-as-assistant-message events both transition
		// out of the in-flight projection state, so this naturally
		// fires when the turn ends without us having to switch on
		// event type. Returns nil when the queue is empty or the
		// assistant is still busy (e.g. mid-tool), so the common path
		// is a no-op.
		flush := m.flushQueuedUserMessage()
		return m, tea.Batch(flush, m.repumpCmd())

	case approvalRequestMsg:
		// Stash the request; overlay renders on next View. Force
		// scroll-to-bottom regardless of prior YOffset - the user
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
		// Also re-poll the children view at the same low cadence so a
		// fresh spawn surfaces the inline panel without a dedicated
		// supervisor event. The faster 250ms tick takes over once at
		// least one child is live and stops on the next empty snapshot.
		m.thinkingTick++
		m.advanceTypewriter()
		if m.childrenView != nil && len(m.childrenSnap) == 0 {
			snap := m.childrenView.Snapshot()
			if len(snap) > 0 {
				m.childrenSnap = snap
				m.rerenderViewport()
				return m, tea.Batch(scheduleTextTick(), scheduleChildrenTick())
			}
		}
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
		// pump - the transcript row was created by EvtUserShellStart
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

	case childrenTickMsg:
		if m.childrenView == nil {
			return m, nil
		}
		prev := len(m.childrenSnap)
		m.childrenSnap = m.childrenView.Snapshot()
		now := len(m.childrenSnap)
		if prev != now {
			m.rerenderViewport()
		}
		if now == 0 {
			// Stop the tick loop. Init re-arms it on the next session
			// boot; a future Spawn restarts it via the same seam (the
			// snapshot poll on the next user-message render frame).
			return m, nil
		}
		return m, scheduleChildrenTick()
	}
	return m, nil
}

// repumpCmd re-arms the event pump after each delivered event so the
// chat keeps draining the subscription channel.
// hiddenChatToolNames is the curated set of tool names whose
// invocations carlos suppresses from the chat transcript. Today this
// is the carlos_about self-introspection shim — the model leans on it
// before nearly every "what's my frame / vault / model" answer, and
// the resulting tool-card rows add no information the user actually
// needs (the answer is in the assistant reply that follows). Keeping
// this as a single small set means future "silent" tools can opt in
// without each one writing the same skip branch.
//
// Suppression is render-only: the event log still records the call +
// result, audit replay still works, and the transcript filter applies
// equally on first paint and on backfill.
var hiddenChatToolNames = map[string]struct{}{
	"carlos_about": {},
}

// isHiddenToolCall reports whether a tool's name is in the suppressed
// set. Pulled out so chat_test.go can pin the policy without poking
// at the underlying map.
func isHiddenToolCall(name string) bool {
	_, ok := hiddenChatToolNames[name]
	return ok
}

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
// for the same event - the two concerns are independent. A heartbeat
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
		// fallthrough - still render the entry below if applicable.
	}
	switch ev.Type {
	case agent.EvtUserMessage:
		var p agent.MessagePayload
		_ = json.Unmarshal(ev.Payload, &p)
		// Dedup against an optimistic append (see appendUserMessage):
		// when submit() paints the user-bubble immediately for
		// responsiveness, the subscription pump delivers the canonical
		// event a beat later. Skip when the most recent transcript
		// entry is an identical user_message and the timestamps are
		// within a small window — that's our optimistic copy, not a
		// new submit. Backfill events come through with old
		// timestamps and bypass the window check, so resume-from-log
		// still hydrates correctly.
		if n := len(m.transcript); n > 0 {
			last := m.transcript[n-1]
			if last.kind == entryUserMessage && last.text == p.Text &&
				absDuration(ev.TS.Sub(last.ts)) < 5*time.Second {
				break
			}
		}
		m.transcript = append(m.transcript, transcriptEntry{
			kind:        entryUserMessage,
			ts:          ev.TS,
			text:        p.Text,
			attachments: p.Attachments,
		})
	case agent.EvtAssistantMessage:
		var p agent.MessagePayload
		_ = json.Unmarshal(ev.Payload, &p)
		// chatglue surfaces loop / provider errors as EvtAssistantMessage
		// tagged with chatglue.ErrorEventPrefix so this surface renders
		// them as a bordered warn-color card instead of an avatar
		// reply. We strip the marker before storing.
		if rest, ok := stripErrorPrefix(p.Text); ok {
			m.transcript = append(m.transcript, transcriptEntry{
				kind: entryError,
				ts:   ev.TS,
				text: rest,
			})
			break
		}
		m.transcript = append(m.transcript, transcriptEntry{
			kind: entryAssistantMessage,
			ts:   ev.TS,
			text: p.Text,
		})
	case agent.EvtToolCall:
		var tc agent.ToolCall
		_ = json.Unmarshal(ev.Payload, &tc)
		// Some tools (notably carlos_about, the self-introspection
		// shim the model invokes to remember its own setup) get
		// called frequently as a sanity check before any "what's my
		// frame / vault / model" question. Surfacing those cards
		// reads as noise to the user — they care about WRITE tools
		// and outbound network calls, not internal getters. Skipping
		// the entry entirely (here AND in EvtToolResult below) keeps
		// the event log honest while keeping the transcript clean.
		if isHiddenToolCall(tc.Name) {
			break
		}
		entry := transcriptEntry{
			kind:         entryToolCall,
			ts:           ev.TS,
			tool:         tc.Name,
			toolInput:    string(tc.Input),
			toolCalledAt: ev.TS,
		}
		if tc.Name == skillUseToolName {
			entry.isSkill = true
			entry.skillName = parseSkillName(tc.Input)
		}
		if tc.Name == agentToolName {
			entry.isAgent = true
			entry.agentObjective = parseAgentObjective(tc.Input)
		}
		m.transcript = append(m.transcript, entry)
	case agent.EvtToolResult:
		// Fold the result back into the most recent matching
		// entryToolCall instead of creating a separate row. The
		// transcript renders the pair as ONE bordered tool card
		// (collapsed by default; future slice adds expand toggle).
		var tr agent.ToolResult
		_ = json.Unmarshal(ev.Payload, &tr)
		if isHiddenToolCall(tr.Name) {
			// Matching tool_call was suppressed; suppress the result
			// too so the standalone-card fallback below doesn't
			// resurrect it. The event still exists in the log for
			// replay / audit; only the transcript is filtered.
			break
		}
		idx := m.findLatestToolCall(tr.Name)
		if idx == -1 {
			// Defensive: result without a matching call (e.g. replay
			// of an event log truncated mid-turn). Fall back to a
			// standalone card so the result isn't silently dropped.
			entry := transcriptEntry{
				kind:         entryToolCall,
				ts:           ev.TS,
				tool:         tr.Name,
				toolResult:   string(tr.Output),
				hasResult:    true,
				isError:      tr.IsError,
				toolResultAt: ev.TS,
			}
			if tr.Name == skillUseToolName {
				entry.isSkill = true
			}
			if tr.Name == agentToolName {
				entry.isAgent = true
			}
			m.transcript = append(m.transcript, entry)
			break
		}
		m.transcript[idx].toolResult = string(tr.Output)
		m.transcript[idx].hasResult = true
		m.transcript[idx].isError = tr.IsError
		m.transcript[idx].toolResultAt = ev.TS
	case agent.EvtSteering:
		var p agent.MessagePayload
		_ = json.Unmarshal(ev.Payload, &p)
		m.transcript = append(m.transcript, transcriptEntry{
			kind: entrySteering,
			ts:   ev.TS,
			text: p.Text,
		})
	case agent.EvtStateChange:
		// No transcript entry - the header's id + state badge + model
		// already conveys session identity and current activity. The
		// older "agent <full-ulid> spawned (model=...)" line was
		// pure duplication and read as noise the user actively had
		// to skip past on every session open.
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
				shellCommand:      "(unknown - start event missing)",
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
		// whatever streamed during the run - the End event's copy is
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
	// Serialize through the composer so chip attachments travel with
	// the text (the markers stay in raw; they're the persisted form).
	// A nil composer (bare test Model) falls back to the textarea.
	raw := strings.TrimSpace(m.ta.Value())
	var atts []agent.Attachment
	if m.composer != nil {
		text, a := m.composer.Serialize()
		raw = strings.TrimSpace(text)
		atts = a
	}
	if raw == "" {
		return nil
	}

	// Phase U: "!cmd" submissions short-circuit to the user-shell
	// path before slash or model routing. A message carrying chips is
	// never a shell or slash submission - chips are model-bound
	// content, so those branches are gated on len(atts)==0.
	if len(atts) == 0 && isShellSubmission(raw) {
		m.slashSuggest.reset()
		m.mentionSuggest.reset()
		m.resetComposer()
		return m.submitUserShellCmd(extractShellCommand(raw), usershell.Foreground)
	}

	// Complete-then-submit: when slash mode is active and the user
	// typed a prefix that the selected suggestion fully matches
	// (e.g. "/fr" with /frame highlighted), expand to the full verb
	// before parsing. The user picked this Enter behavior in the
	// initial design pass - mirrors fish, where pressing Enter on a
	// ghost text first accepts the suggestion. We only rewrite when
	// the typed value is strictly a prefix of the verb; anything
	// past the verb (args) the user owns.
	if len(atts) == 0 && m.slashSuggest.open {
		if spec, ok := m.slashSuggest.selected(); ok {
			verb := "/" + spec.Name
			if strings.HasPrefix(verb, raw) && raw != verb {
				raw = verb
			}
		}
	}

	m.slashSuggest.reset()
	m.mentionSuggest.reset()
	m.resetComposer()
	// Any successful submit ends the current history walk so the
	// next ↑ starts from the most-recent entry again. Cheap; safe to
	// call when no walk is active.
	m.chatHistoryReset()

	if len(atts) == 0 {
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
	}
	// Mid-turn queueing: if the assistant is still working on the
	// previous turn, park the raw text in queuedUserMessages and let
	// flushQueuedUserMessage release it once the turn ends. Without
	// this, the message gets swallowed — submit() would have to wait
	// on the same goroutine the agent loop is serializing on, which
	// in practice means the user sees nothing happen until they
	// re-type after the turn finishes.
	if m.assistantBusy() {
		m.queuedUserMessages = append(m.queuedUserMessages, queuedUserMessage{text: raw, atts: atts})
		m.status = m.queuedHintLine()
		return nil
	}
	return m.appendUserMessage(raw, atts)
}

// resetComposer clears the textarea AND the composer's chip state on
// every submit path, so attachments can't bleed into the next message.
// Falls back to the bare textarea reset when no composer is wired
// (bare test Models).
func (m *Model) resetComposer() {
	if m.composer != nil {
		m.composer.Reset()
		return
	}
	m.ta.Reset()
}

// queuedHintLine renders the status hint shown after a mid-turn
// submit, so the user can tell that the keystroke landed and is
// parked rather than lost. Pluralized for the multi-queue case.
func (m *Model) queuedHintLine() string {
	n := len(m.queuedUserMessages)
	if n == 1 {
		return "queued — will send when assistant finishes"
	}
	return fmt.Sprintf("%d queued — will send when assistant finishes", n)
}

// flushQueuedUserMessage pops the head of queuedUserMessages and
// dispatches it as a normal user message, but only when the
// assistant has gone idle. Called from the eventMsg arm of Update
// after every event so any state-transition that lands the
// assistant in idle (assistant_message, chatglue error surfaced as
// assistant_message, etc.) drains the queue. Returns nil when the
// queue is empty or the assistant is still busy — both are no-ops.
//
// Releases one entry per call so each queued message gets its own
// turn (the next assistant_message will fire the next flush). This
// keeps the user model simple: "messages I typed mid-turn dispatch
// one at a time, in order, as the assistant frees up."
func (m *Model) flushQueuedUserMessage() tea.Cmd {
	if len(m.queuedUserMessages) == 0 {
		return nil
	}
	if m.assistantBusy() {
		return nil
	}
	q := m.queuedUserMessages[0]
	m.queuedUserMessages = m.queuedUserMessages[1:]
	// Clear the hint once the queue empties; otherwise refresh it so
	// the remaining count is accurate.
	if len(m.queuedUserMessages) == 0 {
		m.status = ""
	} else {
		m.status = m.queuedHintLine()
	}
	return m.appendUserMessage(q.text, q.atts)
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

// shellSlashForeground handles "/fg <id>". Accepts either the full
// ULID or the short j<id>/<8-char-suffix> form.
func (m *Model) shellSlashForeground(arg string) tea.Cmd {
	if m.usershell == nil {
		return func() tea.Msg {
			return statusMsg{text: "/fg: user-shell not wired", kind: statusWarn}
		}
	}
	id := resolveShellJobID(m.usershell, arg)
	if id == "" {
		return func() tea.Msg {
			return statusMsg{
				text: fmt.Sprintf("/fg: no job matches %q", arg),
				kind: statusWarn,
			}
		}
	}
	mgr := m.usershell
	return func() tea.Msg {
		if err := mgr.Foreground(id); err != nil {
			return statusMsg{
				text: fmt.Sprintf("/fg %s: %v", shortID(id), err),
				kind: statusWarn,
			}
		}
		return statusMsg{
			text: fmt.Sprintf("shell: j%s moved to foreground", shortID(id)),
			kind: statusInfo,
		}
	}
}

// shellSlashBackground handles "/bg" (no arg → background the current
// fg job, same as Ctrl+Z) and "/bg <id>" (background a specific
// running job).
func (m *Model) shellSlashBackground(arg string) tea.Cmd {
	if m.usershell == nil {
		return func() tea.Msg {
			return statusMsg{text: "/bg: user-shell not wired", kind: statusWarn}
		}
	}
	if arg == "" {
		return m.backgroundRunningCmd()
	}
	id := resolveShellJobID(m.usershell, arg)
	if id == "" {
		return func() tea.Msg {
			return statusMsg{
				text: fmt.Sprintf("/bg: no job matches %q", arg),
				kind: statusWarn,
			}
		}
	}
	mgr := m.usershell
	return func() tea.Msg {
		if err := mgr.Background(id); err != nil {
			return statusMsg{
				text: fmt.Sprintf("/bg %s: %v", shortID(id), err),
				kind: statusWarn,
			}
		}
		return statusMsg{
			text: fmt.Sprintf("shell: j%s moved to background", shortID(id)),
			kind: statusInfo,
		}
	}
}

// resolveShellJobID accepts the user's arg (raw ULID, "j<short>",
// or just the suffix) and returns the matching full ULID - or "" if
// no job matches. Case-insensitive substring match across job IDs.
func resolveShellJobID(mgr *usershell.Manager, arg string) string {
	arg = strings.TrimSpace(arg)
	arg = strings.TrimPrefix(arg, "j")
	if arg == "" {
		return ""
	}
	arg = strings.ToLower(arg)
	for _, s := range mgr.Jobs() {
		if strings.Contains(strings.ToLower(s.ID), arg) {
			return s.ID
		}
	}
	return ""
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
		// next "hi" - exactly the bug we hit in field testing.
		m.transcript = nil
		// History walk is sourced from the transcript we just
		// emptied; drop any in-flight cursor so the next ↑ falls
		// through to the textarea cleanly rather than indexing into
		// a stale length.
		m.chatHistoryReset()
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
		return m.modelSlash(c.Args)
	case "resume":
		return m.openResumePicker()
	case "skills":
		return m.skillsSlash(c.Args)
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
	case "shell":
		// Phase U S7: explicit slash entry to the user-shell. Same
		// effect as typing "!cmd" - for users who prefer the slash
		// vocabulary or have keyboards that fight the "!" key.
		body := strings.TrimSpace(c.Args)
		if body == "" {
			return func() tea.Msg {
				return statusMsg{text: "usage: /shell <cmd>", kind: statusWarn}
			}
		}
		return m.submitUserShellCmd(body, usershell.Foreground)
	case "jobs":
		// Phase U S7: toggle the jobs overlay. Mirrors Ctrl+J.
		if m.usershell == nil {
			return func() tea.Msg {
				return statusMsg{text: "/jobs: user-shell not wired", kind: statusWarn}
			}
		}
		m.showJobs = !m.showJobs
		if m.showJobs {
			m.jobsCursor = 0
			m.jobsFilter = ""
			m.jobsFilterMode = false
		}
		m.rerenderViewport()
		return nil
	case "fg":
		return m.shellSlashForeground(strings.TrimSpace(c.Args))
	case "bg":
		return m.shellSlashBackground(strings.TrimSpace(c.Args))
	case "trust":
		return m.trustSlashEnable()
	case "untrust":
		return m.trustSlashDisable()
	case "trusts":
		return m.trustSlashList()
	case "permissions":
		// Phase T-3: toggle the layered-policy overlay. Mirrors
		// /jobs's toggle pattern; ESC inside the overlay also
		// closes.
		m.showPerms = !m.showPerms
		if m.showPerms {
			m.permsTab = permsTabBuiltin
			m.permsCursor = 0
			m.permsFilter = ""
			m.permsFilterMode = false
		}
		m.rerenderViewport()
		return nil
	case "frame":
		return m.frameSlash(strings.TrimSpace(c.Args))
	case "capabilities":
		return m.capabilitiesSlash()
	case "mode":
		return m.modeSlash(strings.TrimSpace(c.Args))
	case "whoami":
		return m.whoamiSlash()
	}
	if _, ok := slash.Lookup(c.Name); ok {
		return func() tea.Msg {
			return statusMsg{
				text: fmt.Sprintf("slash command: /%s (handler pending - slice 1f)", c.Name),
				kind: statusInfo,
			}
		}
	}
	return func() tea.Msg {
		return statusMsg{
			text: fmt.Sprintf("unknown command: /%s - try /help", c.Name),
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
// SIGHUP / IPC reload - neither is needed for /schedule itself to work,
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
			return `schedule add: usage - /schedule add "<when>" <prompt...>`
		}
		sch, err := schedule.ParseNatural(when)
		if err != nil {
			return "schedule add: " + err.Error()
		}
		sch.Prompt = prompt
		sch.Name = autoSlugName(prompt)
		known := make(map[string]bool, len(cfg.Frames.List))
		for _, f := range cfg.Frames.List {
			known[f.Name] = true
		}
		if err := sch.Validate(known); err != nil {
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

// modelSlash handles `/model [provider:model | model]`. With no args
// it lists every configured provider + its default model alongside
// the *currently active* identity (so a user mid-conversation can see
// which model is answering before deciding to swap). With args it
// parses the requested target — accepting either the full
// "<provider>:<model>" form OR a bare "<model>" that keeps the active
// provider — and hands off to FrameUI.SwitchModel so the runtime can
// rebuild the chatglue.Loop atomically. Without a wired SwitchModel
// the slash echoes "not wired in this session" rather than silently
// dropping the request (the prior behavior, which read as "no-op
// crash" to the user).
// skillsSlash echoes the loaded skill library so the user can see
// what carlos has available in the current frame. `/skills` (no
// args) and `/skills list` both print the catalog. Other verbs
// (`review`, `edit`) advertise their hint but aren't wired in this
// release — the slash spec documents the shape so the slash help
// keeps the same vocabulary as it did pre-wire.
func (m *Model) skillsSlash(arg string) tea.Cmd {
	verb, _, _ := strings.Cut(strings.TrimSpace(arg), " ")
	switch verb {
	case "", "list":
		return m.skillsListCmd()
	default:
		return statusCmd("/skills "+verb+" not yet wired; try /skills list", statusInfo)
	}
}

// skillsListCmd builds the catalog status echo. With no skills
// loaded the message points the user at where to drop them. With
// a populated library it summarises name + (truncated) description
// in a single status line; the user can call skill_use via the
// model for the full body.
func (m *Model) skillsListCmd() tea.Cmd {
	if m.frame.SkillsCatalog == nil {
		return statusCmd("/skills: skill library not wired in this session", statusWarn)
	}
	entries := m.frame.SkillsCatalog()
	if len(entries) == 0 {
		return statusCmd("/skills: no skills available in this frame (drop *.md files into ~/.carlos/skills/ or wait for a future starter pack)", statusInfo)
	}
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			continue
		}
		desc := strings.TrimSpace(e.Description)
		if desc == "" {
			parts = append(parts, name)
			continue
		}
		// Cap description so a few wordy skills don't blow the
		// status row. The model can call skill_use for the full
		// body when curiosity strikes.
		const cap = 72
		if len(desc) > cap {
			desc = desc[:cap-1] + "…"
		}
		parts = append(parts, name+" — "+desc)
	}
	return statusCmd(strings.Join(parts, "  ·  "), statusInfo)
}

func (m *Model) modelSlash(arg string) tea.Cmd {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return statusCmd(m.modelStatusLine(), statusInfo)
	}
	provider, model := parseModelArg(arg)
	if model == "" && provider == "" {
		return statusCmd("/model: empty target; try /model <provider>:<model>", statusWarn)
	}
	if m.frame.SwitchModel == nil {
		return statusCmd("model switching not wired in this session", statusWarn)
	}
	resolvedProv, resolvedModel, err := m.frame.SwitchModel(provider, model)
	if err != nil {
		return statusCmd("/model: switch failed: "+err.Error(), statusWarn)
	}
	echo := "switched to " + resolvedProv + ":" + resolvedModel
	return statusCmd(echo, statusInfo)
}

// parseModelArg splits "<provider>:<model>" into its two parts, or
// returns ("", model) for a bare "<model>" (so the caller defaults
// the provider to the currently-active one). The split is on the
// FIRST ":" so OpenRouter ids like "openrouter:google/gemini-3.5-flash"
// or even "openai:gpt-5" survive unscathed even though some users
// might type extra colons inside the model id.
func parseModelArg(arg string) (provider, model string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", ""
	}
	idx := strings.IndexByte(arg, ':')
	if idx < 0 {
		return "", arg
	}
	return strings.TrimSpace(arg[:idx]), strings.TrimSpace(arg[idx+1:])
}

// modelStatusLine renders the no-args output of /model: the currently
// active provider:model first (the answer to the most common reason a
// user runs /model bare), then a horizontally-laid catalog of every
// configured provider with its default model. A `*` flags the
// session's currently-active provider. The line is intentionally one
// row so it fits in the status echo band.
func (m *Model) modelStatusLine() string {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return "/model: no config loaded (" + err.Error() + ")"
	}
	var head string
	if m.frame.Identity != nil {
		if p, mod := m.frame.Identity(); p != "" || mod != "" {
			head = "active: " + p + ":" + mod
		}
	}
	if len(cfg.Providers) == 0 {
		if head != "" {
			return head + "   no providers configured - run `carlos onboard`"
		}
		return "/model: no providers configured - run `carlos onboard`"
	}
	parts := make([]string, 0, len(cfg.Providers))
	activeProv := ""
	if m.frame.Identity != nil {
		activeProv, _ = m.frame.Identity()
	}
	if activeProv == "" {
		activeProv = cfg.DefaultProvider
	}
	for _, name := range sortedProviderNames(cfg.Providers) {
		pc := cfg.Providers[name]
		mark := " "
		if name == activeProv {
			mark = "*"
		}
		model := pc.DefaultModel
		if model == "" {
			model = "(no default)"
		}
		parts = append(parts, fmt.Sprintf("%s%s=%s", mark, name, model))
	}
	tail := "configured: " + strings.Join(parts, "  ") + "   (* = active)"
	if head == "" {
		return tail
	}
	return head + "   " + tail
}

// sortedProviderNames returns a stable, ALPHABETICAL list of provider
// names from a Providers map. Without this the /model echo flickered
// between renders because map iteration order is randomised, and the
// caller couldn't grep the line for a known position.
func sortedProviderNames(provs map[string]config.ProviderConfig) []string {
	out := make([]string, 0, len(provs))
	for n := range provs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// chatglueErrorPrefix mirrors chatglue.ErrorEventPrefix verbatim so
// applyEvent can route tagged assistant messages to the error-card
// renderer without importing chatglue (the dep graph already runs
// chat → chatglue at startup; importing the other direction would
// cycle).
const chatglueErrorPrefix = "[carlos-error] "

// stripErrorPrefix returns (text without the marker, true) when text
// begins with chatglueErrorPrefix, or ("", false) otherwise. Single
// purpose: keeps applyEvent's case statement readable.
func stripErrorPrefix(text string) (string, bool) {
	if strings.HasPrefix(text, chatglueErrorPrefix) {
		return strings.TrimPrefix(text, chatglueErrorPrefix), true
	}
	return "", false
}

// absDuration returns d's absolute value. Used by the EvtUserMessage
// optimistic-append dedup so clock skew (the optimistic row stamps
// time.Now(); the log event may stamp a few µs earlier or later)
// doesn't break the window comparison.
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// appendUserMessage writes a user_message event to the log AND
// optimistically paints the row into the local transcript so the
// user sees their submit immediately. Without the optimistic step
// there's a visible lag between Enter and the user-bubble showing,
// driven by the SQLite append + subscription round-trip; with it the
// row lands on the next render frame and the "thinking" indicator
// trips right away (isThinking keys off the most recent transcript
// entry).
//
// The optimistic copy is then deduped against the canonical event
// when the subscription pump delivers it — see applyEvent's
// EvtUserMessage case for the matching logic.
func (m *Model) appendUserMessage(text string, atts []agent.Attachment) tea.Cmd {
	m.transcript = append(m.transcript, transcriptEntry{
		kind:        entryUserMessage,
		ts:          time.Now().UTC(),
		text:        text,
		attachments: atts,
	})
	m.rerenderViewport()

	agentID := m.agentID
	log := m.log
	return func() tea.Msg {
		payload, err := json.Marshal(agent.MessagePayload{Text: text, Attachments: atts})
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

