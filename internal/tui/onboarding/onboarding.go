package onboarding

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/theme"
	"github.com/georgebuilds/carlos/internal/tui/termscrub"
)

// Screen identifies the current step in the five-screen flow. Order
// matters: shift-tab decrements, enter (where applicable) advances.
//
// There is no separate welcome / splash screen. The persistent left-rail
// portrait IS the welcome moment, and the intake form starts immediately
// on the right. Carlos's face stays put across all five screens.
type Screen int

const (
	ScreenName Screen = iota
	ScreenProvider
	ScreenModel
	ScreenSkills
	ScreenVault
	ScreenDaemon
	ScreenGateway
	ScreenDone
)

const totalScreens = 8

// screenTitle is the heading shown above each right-pane form.
func screenTitle(s Screen) string {
	switch s {
	case ScreenName:
		return "What should I call you?"
	case ScreenProvider:
		return "Wire up your providers"
	case ScreenModel:
		return "Pick default models"
	case ScreenSkills:
		return "Skills convention"
	case ScreenVault:
		return "Notes vault"
	case ScreenDaemon:
		return "Background daemon"
	case ScreenGateway:
		return "Messaging gateway"
	case ScreenDone:
		return "Ready"
	}
	return ""
}

// ErrAborted is the sentinel Run returns when the user ctrl-c's out of the
// flow. Callers treat this as "do not write config" - NOT as an error to
// surface to the user. main() exits 0 in that case.
var ErrAborted = errors.New("onboarding: aborted by user")

// Brand palette - package-level vars populated by [ApplyPalette].
//
// Onboarding was the historical source of truth for the brand colors
// (cap navy + the brighter accent borders). As of Phase 9 slice 9a the
// values come from [internal/theme.Palette] so every TUI surface
// stays in lockstep. init() seeds with the autodetect default;
// cmd/carlos overrides at startup.
var (
	colorBrand   lipgloss.Color
	colorAccent  lipgloss.Color
	colorMuted   lipgloss.Color
	colorWarn    lipgloss.Color
	colorSuccess lipgloss.Color
)

func init() {
	ApplyPalette(theme.Load(theme.Options{}))
}

// ApplyPalette wires a freshly-loaded [theme.Palette] into onboarding's
// color vars AND rebuilds the cached style values. Note: onboarding
// runs BEFORE the user has a config, so main calls this with an
// autodetect-only palette (no AccentOverride); the post-onboarding
// chat/manage TUIs pick up the configured accent instead.
func ApplyPalette(p theme.Palette) {
	colorBrand = p.Brand
	colorAccent = p.Accent
	colorMuted = p.Muted
	// onboarding's "warn" was the same amber 214 chat called Tool.
	colorWarn = p.Tool
	colorSuccess = p.OK
	rebuildStyles()
}

// Layout constants. Left rail is a fixed cell width - the portrait is
// rendered into it at known aspect (cols == 2×rows preserves square aspect
// on terminals where cells are ~1:2 W:H).
//
// Portrait is intentionally smaller than the rail so we get visible padding
// around it; the rail centers the portrait horizontally. The rail has a
// right border (lipgloss NormalBorder, accent color) that gives a clean
// vertical column separator between the persistent face and the intake
// form on the right.
const (
	portraitCols  = 18
	portraitRows  = 9
	leftRailWidth = 28 // portrait + ~5 cells padding each side (centered)
	railTopPad    = 1  // blank rows above the portrait
	colGap        = 3  // gutter between rail's right border and right pane
	minTerminalW  = 80
	minTerminalH  = 24
)

// Styles re-used across screens. Per-frame width/height-dependent
// styles are constructed in the View() path.
//
// Phase 9 slice 9a: lipgloss.Style captures colors by value at the
// time .Foreground() is called, so we rebuild these styles inside
// [ApplyPalette] every time the palette changes. The init() default-
// load seeds them; cmd/carlos's startup ApplyPalette overwrites them
// with the user-configured palette.
var (
	styleTagline     lipgloss.Style
	styleHint        lipgloss.Style
	stylePrompt      lipgloss.Style
	styleHeader      lipgloss.Style
	styleBrand       lipgloss.Style
	styleDotOn       lipgloss.Style
	styleDotOff      lipgloss.Style
	styleDotCurrent  lipgloss.Style
	styleDotPending  lipgloss.Style
	styleKey         lipgloss.Style
)

// rebuildStyles regenerates the cached styles from the current color
// vars. Called from ApplyPalette so every palette swap refreshes them.
func rebuildStyles() {
	styleTagline = lipgloss.NewStyle().Foreground(colorAccent).Italic(true)
	styleHint = lipgloss.NewStyle().Foreground(colorMuted)
	stylePrompt = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleHeader = lipgloss.NewStyle().Foreground(colorMuted)
	styleBrand = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleDotOn = lipgloss.NewStyle().Foreground(colorAccent)
	styleDotOff = lipgloss.NewStyle().Foreground(colorMuted)
	styleDotCurrent = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleDotPending = lipgloss.NewStyle().Foreground(colorMuted)
	styleKey = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
}

// outerBorder is the crispy blue full-screen border applied at View
// time. We recompute per-frame from the latest WindowSizeMsg so the
// border always hugs the screen edge.
//
// Height(height - 4) + a two-row leading margin in View leaves enough
// breathing room for terminals with overlaid tab bars (Ghostty's
// tabbed mode is the empirical motivator - its tab chrome overlaps
// the first two cell rows; iTerm2 tabs + tmux status lines fit too).
// One row of margin (the original v0.2.0 behavior) was not enough in
// Ghostty. Two rows is universal - the cost in untabbed terminals is
// two blank rows above the border, which is invisible to the eye.
func outerBorderStyle(width, height int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.ThickBorder()).
		BorderForeground(colorAccent).
		Width(width - 2).
		Height(height - 4).
		Padding(1, 2)
}

// Flow is the orchestrator. It composes the five sub-models, handles
// ctrl-c abort, threads the in-progress *config.Config between screens,
// and owns the persistent left-rail rendering so children only have to
// produce their right-pane form content.
type Flow struct {
	current Screen
	cfg     *config.Config
	aborted bool

	// only restricts the flow to a single screen (set via Options.Only).
	// onlyStart records which screen was the entry point so shift-tab
	// can refuse to back-nav out of the requested sub-flow.
	only      bool
	onlyStart Screen

	width  int
	height int

	name     nameModel
	provider providerModel
	model    modelModel
	skills   skillsModel
	vault    vaultModel
	daemon   daemonModel
	gateway  gatewayModel
	done     doneModel

	// portrait is rendered once (fixed-size left rail) and cached.
	portrait string

	// pulseFrame drives the slice-9e advance microinteraction. On
	// advance() it bumps to 1; tickPulse re-schedules itself until
	// pulseFrame walks 1 → 2 → 3 → 0, painting the newly-active dot
	// as ◐ → ◉ → ● before settling to the static fill. 0 means no
	// animation in flight.
	pulseFrame int
}

// pulseFrameDuration is how long each pulse frame is visible.
// 100ms × 3 frames = 300ms total animation per advance - long
// enough to register, short enough to not feel sluggish.
const pulseFrameDuration = 100 * time.Millisecond

// New constructs a Flow with sane defaults. The returned config can be
// inspected post-Run to persist; on ErrAborted, callers MUST NOT persist.
func New() *Flow {
	return NewWithOptions(Options{})
}

// Options tunes Flow construction. Zero value matches New()'s historical
// behavior - start at the name screen, framed by the welcome → done
// sequence. StartingScreen jumps the user past every earlier screen and
// terminates at Done after the target screen finishes, used by
// `carlos onboard --only <screen>` to re-enter a single sub-flow.
type Options struct {
	// StartingScreen is the first screen to land on. When zero
	// (ScreenName) the flow behaves as the unflagged onboarding.
	StartingScreen Screen
	// Only, when true, restricts the flow to StartingScreen. After the
	// child screen advances we jump straight to Done (skipping the
	// downstream screens) and back-nav from StartingScreen is disabled.
	Only bool
	// ExistingConfig seeds the flow with a previously-saved config so
	// a partial re-onboard (e.g. `--only models`) sees the providers
	// already configured. nil = empty config + defaults.
	ExistingConfig *config.Config
}

// NewWithOptions is the Options-aware constructor. Used by the
// `--only` flag to enter the flow at a specific screen.
func NewWithOptions(opts Options) *Flow {
	cfg := opts.ExistingConfig
	if cfg == nil {
		cfg = &config.Config{
			UserName:  config.DefaultUserName,
			Providers: map[string]config.ProviderConfig{},
		}
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]config.ProviderConfig{}
	}
	if strings.TrimSpace(cfg.UserName) == "" {
		cfg.UserName = config.DefaultUserName
	}
	starting := opts.StartingScreen
	if starting < ScreenName || starting > ScreenDone {
		starting = ScreenName
	}
	daemonChild := newDaemonModel()
	gatewayChild := newGatewayModel()
	// --only daemon / --only gateway re-enter a screen the user has
	// likely already filled in. Preload the daemon toggle so `enter`
	// keeps the current state; auto-Prime the gateway so the user
	// skips the redundant "set up later" gate that only makes sense
	// during the initial onboarding walk.
	if opts.Only && opts.ExistingConfig != nil {
		switch starting {
		case ScreenDaemon:
			daemonChild = newDaemonModelWithInitial(opts.ExistingConfig.Daemon.Enabled)
		case ScreenGateway:
			gatewayChild = NewGatewayStandalone()
		}
	}
	f := &Flow{
		current:   starting,
		only:      opts.Only,
		onlyStart: starting,
		cfg:       cfg,
		name:      newNameModel(cfg.UserName),
		provider:  newProviderModel(),
		model:     newModelModel(),
		skills:    newSkillsModel(),
		vault:     newVaultModel(),
		daemon:    daemonChild,
		gateway:   gatewayChild,
		done:      newDoneModel(),
		portrait:  rememberRail(portraitCols, portraitRows),
	}
	// When the caller hands us an existing config that already wires
	// openrouter, kick off the catalog fetch now so a re-entered
	// `--only models` flow sees live pricing.
	if pc, ok := cfg.Providers["openrouter"]; ok && pc.APIKey != "" {
		f.model.orFuture = startOpenRouterFetch()
	}
	return f
}

// PrimeGatewayStandalone advances the gateway sub-model past the
// "set up later" gate so a caller (cmd/carlos `gateway add`) can run
// the wizard end-to-end without re-asking the user. Idempotent.
func (f *Flow) PrimeGatewayStandalone() {
	f.gateway = NewGatewayStandalone()
}

// rememberRail caches the rail-portrait at construction time so the View
// path doesn't re-render every frame. The portrait is fixed-size for the
// life of the flow; resize messages don't change the rail width.
func rememberRail(cols, rows int) string {
	s, _ := RenderRailCells(cols, rows)
	return s
}

// Run drives the bubbletea program in alt-screen (full-screen) mode.
// Returns the populated config on completion, or (nil, ErrAborted) if
// the user ctrl-c'd at any screen.
func (f *Flow) Run() (*config.Config, error) {
	p := tea.NewProgram(f, tea.WithAltScreen(), tea.WithFilter(termscrub.FilterTerminalLeaks))
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	ff, ok := final.(*Flow)
	if !ok {
		return nil, fmt.Errorf("onboarding: unexpected final model type %T", final)
	}
	if ff.aborted {
		return nil, ErrAborted
	}
	// Seed a default "personal" frame so the post-onboarding chat lands
	// in a wired frame instead of legacy single-shelf mode. screen_name's
	// intro already tells the user "this is your personal frame, the one
	// carlos opens by default" - this is where the data side meets that
	// promise. Idempotent: the partial-re-onboard (--only) paths pass an
	// existing config whose Frames.List is already populated, in which
	// case MigrateFromLegacy is a no-op.
	ensurePersonalFrame(ff.cfg)
	return ff.cfg, nil
}

// ensurePersonalFrame populates cfg.Frames with a synthetic "personal"
// frame derived from cfg.DefaultProvider + that provider's chosen model.
// No-op when cfg already has at least one frame.
func ensurePersonalFrame(cfg *config.Config) {
	if cfg == nil || len(cfg.Frames.List) > 0 {
		return
	}
	model := ""
	if cfg.DefaultProvider != "" {
		if pc, ok := cfg.Providers[cfg.DefaultProvider]; ok {
			model = pc.DefaultModel
		}
	}
	cfg.Frames = frame.MigrateFromLegacy(cfg.Frames, cfg.DefaultProvider, model)
}

// --- tea.Model -------------------------------------------------------------

func (f *Flow) Init() tea.Cmd {
	// Tick the first active screen so its textinput cursor starts blinking.
	switch f.current {
	case ScreenName:
		return f.name.Init()
	case ScreenProvider:
		return f.provider.Init()
	case ScreenModel:
		f.model.syncFromConfig(f.cfg)
		return f.model.Init()
	case ScreenSkills:
		return f.skills.Init()
	case ScreenVault:
		return f.vault.Init()
	case ScreenDaemon:
		return f.daemon.Init()
	case ScreenGateway:
		return f.gateway.Init()
	case ScreenDone:
		return f.done.Init()
	}
	return f.name.Init()
}

func (f *Flow) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Window-size: track for re-layout. No data change; the next View()
	// call uses the new dims.
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		f.width = ws.Width
		f.height = ws.Height
		return f, nil
	}

	// Global key handling. ctrl-c is the most important: it MUST abort
	// cleanly without writing config, on every screen.
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "ctrl+c":
			f.aborted = true
			return f, tea.Quit
		case "shift+tab":
			// In `--only` mode the entry screen is the floor: back-nav
			// out of it would land on an unrelated screen the user
			// didn't ask to revisit.
			if f.only && f.current <= f.onlyStart {
				return f, nil
			}
			if f.current > ScreenName {
				f.current--
				// Mirror the advance() auto-skip: never let back-nav
				// land on Gateway when the daemon is off.
				if f.current == ScreenGateway && !f.cfg.Daemon.Enabled {
					f.current--
				}
				return f, nil
			}
			return f, nil
		}
	}

	// Navigation messages from children.
	switch m := msg.(type) {
	case nextScreenMsg:
		f.applyChildPayload(m.payload)
		f.advance()
		// Slice 9e microinteraction: pulse the newly-active dot
		// through ◐ → ◉ → ● over ~300ms. tickPulseMsg chains
		// itself; this initial schedule kicks it off.
		f.pulseFrame = 1
		return f, schedulePulseTick()
	case pulseTickMsg:
		f.pulseFrame++
		if f.pulseFrame > 3 {
			f.pulseFrame = 0
			return f, nil
		}
		return f, schedulePulseTick()
	case quitMsg:
		return f, tea.Quit
	}

	// Route everything else to the active child.
	switch f.current {
	case ScreenName:
		updated, cmd := f.name.Update(msg)
		f.name = updated.(nameModel)
		return f, cmd
	case ScreenProvider:
		updated, cmd := f.provider.Update(msg)
		f.provider = updated.(providerModel)
		return f, cmd
	case ScreenModel:
		f.model.syncFromConfig(f.cfg)
		updated, cmd := f.model.Update(msg)
		f.model = updated.(modelModel)
		return f, cmd
	case ScreenSkills:
		updated, cmd := f.skills.Update(msg)
		f.skills = updated.(skillsModel)
		return f, cmd
	case ScreenVault:
		updated, cmd := f.vault.Update(msg)
		f.vault = updated.(vaultModel)
		return f, cmd
	case ScreenDaemon:
		updated, cmd := f.daemon.Update(msg)
		f.daemon = updated.(daemonModel)
		return f, cmd
	case ScreenGateway:
		updated, cmd := f.gateway.Update(msg)
		f.gateway = updated.(gatewayModel)
		return f, cmd
	case ScreenDone:
		updated, cmd := f.done.Update(msg)
		if dm, ok := updated.(doneModel); ok {
			f.done = dm
		}
		return f, cmd
	}
	return f, nil
}

func (f *Flow) View() string {
	w, h := f.width, f.height
	if w == 0 || h == 0 {
		// Before the first WindowSizeMsg, render defensively; bubbletea
		// will redraw immediately after sending the size.
		w, h = 100, 30
	}
	if w < minTerminalW || h < minTerminalH {
		return styleHint.Render(fmt.Sprintf(
			"carlos onboarding needs at least %d×%d. Current: %d×%d.",
			minTerminalW, minTerminalH, w, h))
	}

	border := outerBorderStyle(w, h)
	inner := f.renderInner(border.GetWidth(), border.GetHeight())
	// Leading "\n\n" pushes the border down by two rows so terminals
	// with tab bars (Ghostty especially) don't eat the top edge. See
	// outerBorderStyle for the matching Height(h - 4).
	return "\n\n" + border.Render(inner)
}

// renderInner composes the persistent left rail and the active right pane
// within the dimensions provided by the outer border. The bottom row inside
// the border is a footer with keybind hints.
func (f *Flow) renderInner(innerW, innerH int) string {
	footer := footerBar(f.current)
	footerH := lipgloss.Height(footer) + 1
	contentH := innerH - footerH
	if contentH < 12 {
		contentH = 12
	}

	left := f.renderLeftRail(leftRailWidth, contentH)
	// Rail consumes leftRailWidth (content) + 1 (BorderRight) + 1
	// (PaddingRight) = leftRailWidth + 2 cells; right pane gets the
	// remainder minus colGap of breathing room.
	railOnScreen := leftRailWidth + 2
	rightW := innerW - railOnScreen - colGap
	if rightW < 30 {
		rightW = 30
	}
	right := f.renderRightPane(rightW, contentH)

	row := lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", colGap), right)
	return lipgloss.JoinVertical(lipgloss.Left, row, "", footer)
}

// renderLeftRail renders the persistent portrait + brand + step indicator
// with a right-edge border that doubles as the column separator.
//
// Width math: leftRailWidth is the *content* width; the BorderRight adds
// one cell on the right, so the rail's total on-screen width is
// leftRailWidth + 1. Center the inner content within leftRailWidth (the
// border isn't part of the centered region).
func (f *Flow) renderLeftRail(w, h int) string {
	innerW := w // w == leftRailWidth (content area, sans border)
	portrait := centerLines(f.portrait, innerW, portraitCols)
	brand := centerLines(styleBrand.Render("carlos"), innerW, lipgloss.Width("carlos"))
	step := centerLines(
		styleHeader.Render(fmt.Sprintf("step %d of %d", int(f.current)+1, totalScreens)),
		innerW, lipgloss.Width(fmt.Sprintf("step %d of %d", int(f.current)+1, totalScreens)))
	dotsRaw := renderStepDots(int(f.current), totalScreens, f.pulseFrame)
	dots := centerLines(dotsRaw, innerW, totalScreens*2-1)
	top := strings.Repeat("\n", railTopPad)
	rail := lipgloss.JoinVertical(lipgloss.Left,
		top,
		portrait,
		"",
		brand,
		step,
		"",
		dots,
	)
	return lipgloss.NewStyle().
		Width(innerW).
		Height(h).
		BorderStyle(lipgloss.NormalBorder()).
		BorderRight(true).
		BorderForeground(colorAccent).
		PaddingRight(1).
		Render(rail)
}

// centerLines prepends a consistent left-pad to every line so a block of
// SGR-coded text appears horizontally centered inside a width of w cells.
// visibleWidth is the displayed cell count of the content (callers pass
// what they know, since lipgloss.Width can be wrong for multi-line ANSI).
func centerLines(s string, w, visibleWidth int) string {
	pad := (w - visibleWidth) / 2
	if pad <= 0 {
		return s
	}
	prefix := strings.Repeat(" ", pad)
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}

// renderRightPane shows the current screen's form. The screen title is
// rendered here; child models return only their body content.
func (f *Flow) renderRightPane(w, _ int) string {
	title := stylePrompt.Render(screenTitle(f.current))
	var body string
	switch f.current {
	case ScreenName:
		body = f.name.View()
	case ScreenProvider:
		body = f.provider.View()
	case ScreenModel:
		f.model.syncFromConfig(f.cfg)
		body = f.model.View()
	case ScreenSkills:
		body = f.skills.View()
	case ScreenVault:
		body = f.vault.View()
	case ScreenDaemon:
		body = f.daemon.View()
	case ScreenGateway:
		body = f.gateway.View()
	case ScreenDone:
		body = f.done.renderName(f.cfg.UserName, config.DefaultPath())
	}
	pane := lipgloss.JoinVertical(lipgloss.Left, title, "", body)
	return lipgloss.NewStyle().Width(w).Render(pane)
}

// renderStepDots produces a three-tier counter:
//
//   - ● accent: completed steps (i < current)
//   - ○ accent+bold: the current step
//   - · muted: pending steps (i > current)
//
// When pulseFrame is non-zero, the current dot animates through
// ◐ → ◉ → ● over three render frames (advance microinteraction, slice
// 9e) and then settles back to the outlined-current glyph on the next
// idle render.
func renderStepDots(current, total, pulseFrame int) string {
	parts := make([]string, 0, total)
	for i := 0; i < total; i++ {
		switch {
		case i == current && pulseFrame > 0:
			parts = append(parts, styleDotOn.Render(pulseGlyph(pulseFrame)))
		case i < current:
			parts = append(parts, styleDotOn.Render("●"))
		case i == current:
			parts = append(parts, styleDotCurrent.Render("○"))
		default:
			parts = append(parts, styleDotPending.Render("·"))
		}
	}
	return strings.Join(parts, " ")
}

// pulseGlyph maps a pulse frame index (1-3) to its glyph. Frame 1 is the
// first paint after advance (half-fill), frame 2 is the brightest mid-
// state (bullseye), frame 3 is the settle frame before falling back to ●.
func pulseGlyph(frame int) string {
	switch frame {
	case 1:
		return "◐"
	case 2:
		return "◉"
	case 3:
		return "●"
	}
	return "●"
}

// pulseTickMsg fires from a scheduled tea.Tick to advance pulseFrame.
type pulseTickMsg struct{}

// schedulePulseTick returns a Cmd that fires pulseTickMsg after one
// pulseFrameDuration. The Update handler chains the next tick until
// pulseFrame wraps back to 0.
func schedulePulseTick() tea.Cmd {
	return tea.Tick(pulseFrameDuration, func(time.Time) tea.Msg { return pulseTickMsg{} })
}

// advance is called by Update when a child screen returns nextScreenMsg.
// At the Done screen, advance is a no-op - Done returns quitMsg separately.
//
// Conditional skip: the gateway is daemon-owned (see
// internal/daemon/gateway.go), so when the user declined the daemon
// the gateway screen has nothing to configure. We jump past it
// directly to Done. The step-counter still shows totalScreens=8 so
// users notice the dot pattern; the skip is a UX nicety, not a
// statement about the flow's structural length.
func (f *Flow) advance() {
	if f.current >= ScreenDone {
		return
	}
	// `--only` mode: after the requested screen finishes we skip
	// directly to Done. No further screens belong to this sub-flow.
	if f.only && f.current == f.onlyStart {
		f.current = ScreenDone
		return
	}
	f.current++
	if f.current == ScreenGateway && !f.cfg.Daemon.Enabled {
		f.current++
	}
}

// applyChildPayload merges the child's per-screen output into the config.
// We do this at navigation time, not on every keystroke, so a back-nav
// followed by ctrl-c won't half-write the config struct.
func (f *Flow) applyChildPayload(p any) {
	switch v := p.(type) {
	case nameResult:
		if v.name != "" {
			f.cfg.UserName = v.name
		}
	case providerResult:
		f.cfg.Providers = v.providers
		f.cfg.DefaultProvider = v.defaultProvider
		// Kick off the OpenRouter /models fetch in the background as
		// soon as we know the user wired openrouter. The model screen
		// blocks on this future up to orWait before falling back to
		// the curated list.
		if pc, ok := v.providers["openrouter"]; ok && pc.APIKey != "" {
			f.model.orFuture = startOpenRouterFetch()
		}
	case modelResult:
		for name, model := range v.models {
			pc := f.cfg.Providers[name]
			pc.DefaultModel = model
			f.cfg.Providers[name] = pc
		}
	case daemonResult:
		f.cfg.Daemon.Enabled = v.enabled
	case skillsResult:
		f.cfg.Skills.Convention = v.convention
	case vaultResult:
		f.cfg.Vault.Path = v.path
	case gatewayResult:
		f.cfg.Gateway.Enabled = v.enabled
		if v.ntfy.Enabled {
			f.cfg.Gateway.Ntfy = v.ntfy
		}
		if v.telegram.Enabled {
			f.cfg.Gateway.Telegram = v.telegram
		}
	}
}

// footerBar styles the contextual keybind hints. Key names are highlighted
// in the brand accent so they read at a glance.
func footerBar(s Screen) string {
	switch s {
	case ScreenName:
		return styleKey.Render("enter") + styleHint.Render(" continue   ") +
			styleKey.Render("ctrl-c") + styleHint.Render(" cancel")
	case ScreenDone:
		return styleKey.Render("enter") + styleHint.Render(" finish   ") +
			styleKey.Render("ctrl-c") + styleHint.Render(" cancel")
	default:
		return styleKey.Render("enter") + styleHint.Render(" continue   ") +
			styleKey.Render("shift-tab") + styleHint.Render(" back   ") +
			styleKey.Render("ctrl-c") + styleHint.Render(" cancel")
	}
}

// --- inter-screen messages -------------------------------------------------

// nextScreenMsg is a child's signal to advance. The payload (if any)
// carries per-screen output that Flow merges into the config.
type nextScreenMsg struct{ payload any }

// quitMsg is the Done screen's signal that the user is finished.
type quitMsg struct{}

// nextScreen is a small helper for child models to emit a navigation cmd.
func nextScreen(payload any) tea.Cmd {
	return func() tea.Msg { return nextScreenMsg{payload: payload} }
}

// quit emits a tea.Cmd that asks the Flow to terminate cleanly.
func quit() tea.Cmd {
	return func() tea.Msg { return quitMsg{} }
}
