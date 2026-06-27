package chat

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
)

// overlay_config.go is the `/config` (alias `/settings`) takeover overlay:
// a single-column, flowing settings panel for the handful of config.yaml
// settings normally only touched during onboarding. It reads + writes
// ~/.carlos/config.yaml directly (the same path /mcp and /schedule use)
// and, for the active frame's provider/model, applies the change LIVE via
// FrameUI.SwitchModel so the next turn already uses it.
//
// The headline is provider management, which is per-frame: the Providers
// section is FRAME-FIRST. It shows the selected frame and what it resolves
// to right now, with provenance (frame / override / shared pantry), then
// the shared pantry underneath. Editing a value persists immediately;
// secret values (API keys) are masked in display.

// --- row model -------------------------------------------------------------

type cfgRowKind int

const (
	cfgHeader cfgRowKind = iota // section header (not focusable)
	cfgSpacer                   // blank line (not focusable)
	cfgText                     // free-text field
	cfgSecret                   // masked field (API key)
	cfgModel                    // free-text field with model-completion hint
	cfgEnum                     // left/right cycles a fixed set
	cfgAction                   // enter runs an action
	cfgInfo                     // read-only display line
)

// cfgRow is one rendered line. id is the stable handle the key handler
// switches on; value is the current display value; hint is the muted
// right-side provenance / help; enum is the cycle set for cfgEnum.
type cfgRow struct {
	kind  cfgRowKind
	id    string
	label string
	value string
	hint  string // shown only when the row is focused (progressive disclosure)
	tag   string // tiny always-visible marker after the value (e.g. "live")
	enum  []string
}

func (r cfgRow) focusable() bool {
	switch r.kind {
	case cfgText, cfgSecret, cfgModel, cfgEnum, cfgAction:
		return true
	}
	return false
}

const inheritLabel = "(inherit)"

// --- lifecycle -------------------------------------------------------------

// openConfig loads the on-disk config and opens the overlay. A load
// failure (no config yet) is surfaced as a status line rather than an
// empty panel; in a running chat the config always exists, so this is an
// edge path.
func (m *Model) openConfig() tea.Cmd {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return statusCmd("config: "+err.Error(), statusWarn)
	}
	m.configCfg = cfg
	m.configFrame = m.frame.Active
	if m.configFrame == "" && len(cfg.Frames.List) > 0 {
		m.configFrame = cfg.Frames.List[0].Name
	}
	m.showConfig = true
	m.configEditing = false
	m.configEditBuf = ""
	m.configErr = ""
	m.configNotice = ""
	m.configCursor = 0
	m.configScroll = 0
	// Land the cursor on the first focusable row.
	rows := m.buildConfigRows()
	for i, r := range rows {
		if r.focusable() {
			m.configCursor = i
			break
		}
	}
	m.rerenderViewport()
	return nil
}

func (m *Model) closeConfig() {
	m.showConfig = false
	m.configEditing = false
	m.configCfg = nil
	m.rerenderViewport()
}

// configSave persists the in-memory config; a write failure is shown in
// the overlay's error line rather than silently lost.
func (m *Model) configSave() bool {
	if err := config.Save(config.DefaultPath(), m.configCfg); err != nil {
		m.configErr = "save failed: " + err.Error()
		return false
	}
	m.configErr = ""
	return true
}

// --- row building ----------------------------------------------------------

// buildConfigRows is the single source of truth for what the panel shows.
// It is pure over (m.configCfg, m.configFrame) so the renderer and the key
// handler agree on row identity and order.
func (m *Model) buildConfigRows() []cfgRow {
	c := m.configCfg
	rows := []cfgRow{
		{kind: cfgHeader, label: "General"},
		{kind: cfgText, id: "name", label: "your name", value: orDefault(c.UserName, config.DefaultUserName), hint: "how carlos addresses you"},
		{kind: cfgEnum, id: "skills", label: "skills", value: skillsConventionOrDefault(c.Skills.Convention),
			enum: []string{config.SkillsConventionAgents, config.SkillsConventionClaude}, hint: "where new skills are written"},

		{kind: cfgHeader, label: "Appearance"},
		{kind: cfgEnum, id: "theme", label: "theme", value: themeVariantOrAuto(c.Theme.Variant),
			enum: []string{"auto", "dark", "light"}, hint: "applies on next start"},
		{kind: cfgText, id: "accent", label: "accent", value: orDefault(c.Theme.Accent, ""), hint: "hex or palette name; applies on next start"},

		{kind: cfgHeader, label: "Vault"},
		{kind: cfgText, id: "vault", label: "path", value: c.Vault.Path, hint: "Obsidian vault root; applies on next start"},
	}
	rows = append(rows, m.buildProviderRows()...)
	return rows
}

// buildProviderRows is the frame-first provider section: the selected
// frame, what it resolves to right now (with provenance), the editable
// per-frame provider/model/override, then the shared pantry.
func (m *Model) buildProviderRows() []cfgRow {
	c := m.configCfg
	pantry := c.ProviderNames()
	rows := []cfgRow{{kind: cfgHeader, label: "Providers"}}

	frameNames := frameNamesFromCfg(c)
	if len(frameNames) > 0 {
		fr := c.Frames.Find(m.configFrame)
		if fr == nil && len(c.Frames.List) > 0 {
			fr = &c.Frames.List[0]
			m.configFrame = fr.Name
		}
		eff := resolveEffective(c, fr)
		tag := ""
		if fr != nil && fr.Name == m.frame.Active {
			tag = "live"
		}
		rows = append(rows,
			cfgRow{kind: cfgEnum, id: "frame_select", label: "frame", value: m.configFrame, enum: frameNames,
				hint: "which frame these settings apply to"},
			cfgRow{kind: cfgInfo, label: "resolves to", value: eff.provider + " : " + eff.model, hint: "from " + eff.modelSource},
			cfgRow{kind: cfgEnum, id: "frame_provider", label: "provider", value: orDefault(frameProvider(fr), inheritLabel),
				enum: append([]string{inheritLabel}, pantry...), hint: "which pantry entry this frame uses", tag: tag},
			cfgRow{kind: cfgModel, id: "frame_model", label: "model", value: frameModel(fr), hint: "blank = provider default", tag: tag},
			cfgRow{kind: cfgSecret, id: "frame_override_key", label: "frame key", value: frameOverrideKey(fr, eff.provider),
				hint: m.configFrame + "-only key for " + eff.provider + " (next start)"},
		)
	}

	rows = append(rows, cfgRow{kind: cfgHeader, label: "Shared keys (pantry)"})
	if len(pantry) == 0 {
		rows = append(rows, cfgRow{kind: cfgInfo, label: "", value: "no providers configured yet"})
	}
	for _, name := range pantry {
		pc, _ := c.Provider(name)
		def := ""
		if c.DefaultProvider == name {
			def = "  (default)"
		}
		rows = append(rows,
			cfgRow{kind: cfgInfo, label: name, value: "", hint: def},
			cfgRow{kind: cfgSecret, id: "pantry:" + name + ":key", label: "  key", value: pc.APIKey},
			cfgRow{kind: cfgModel, id: "pantry:" + name + ":model", label: "  model", value: pc.DefaultModel},
			cfgRow{kind: cfgText, id: "pantry:" + name + ":baseurl", label: "  base url", value: pc.BaseURL, hint: "for ollama / self-hosted"},
		)
	}
	if len(pantry) > 1 {
		rows = append(rows, cfgRow{kind: cfgEnum, id: "default_provider", label: "default provider", value: orDefault(c.DefaultProvider, inheritLabel),
			enum: append([]string{inheritLabel}, pantry...), hint: "fallback when a frame does not pin one"})
	}
	rows = append(rows, cfgRow{kind: cfgAction, id: "add_provider", label: "+ add provider", value: ""})
	return rows
}

// --- effective resolution (read-only provenance display) -------------------

type effective struct {
	provider    string
	model       string
	modelSource string // provenance tag for the resolved model
}

// resolveEffective mirrors cmd/carlos.resolveProviderCreds + the dispatch
// model precedence for DISPLAY only, so the panel can show what a frame
// resolves to and where each value comes from.
func resolveEffective(c *config.Config, fr *frame.Frame) effective {
	provider := ""
	if fr != nil {
		provider = strings.TrimSpace(fr.Provider)
	}
	if provider == "" {
		provider = strings.TrimSpace(c.DefaultProvider)
	}
	if provider == "" {
		for _, n := range c.ProviderNames() {
			pc, _ := c.Provider(n)
			if pc.APIKey != "" || pc.BaseURL != "" {
				provider = n
				break
			}
		}
	}
	if provider == "" {
		return effective{provider: "(none)", model: "(none)", modelSource: "no provider configured"}
	}

	// model precedence: frame.Model -> override.DefaultModel -> pantry.DefaultModel
	model, src := "", ""
	if fr != nil && strings.TrimSpace(fr.Model) != "" {
		model, src = fr.Model, "frame"
	}
	if model == "" && fr != nil {
		if ov, ok := fr.ProviderOverride[provider]; ok && ov.DefaultModel != "" {
			model, src = ov.DefaultModel, "frame override"
		}
	}
	if model == "" {
		if pc, ok := c.Provider(provider); ok && pc.DefaultModel != "" {
			model, src = pc.DefaultModel, "shared pantry"
		}
	}
	if model == "" {
		model, src = "(provider default)", "built-in"
	}
	return effective{provider: provider, model: model, modelSource: src}
}

// --- key handling ----------------------------------------------------------

// handleConfigKey routes a keypress while the overlay is open. Returns
// handled=false only for ctrl+c (so it reaches the global quit path).
func (m *Model) handleConfigKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if msg.String() == "ctrl+c" {
		return m, nil, false
	}
	if m.configEditing {
		return m.handleConfigEditKey(msg)
	}
	rows := m.buildConfigRows()
	switch msg.String() {
	case "esc", "ctrl+,":
		m.closeConfig()
		return m, nil, true
	case "up", "k":
		m.configMove(rows, -1)
		return m, nil, true
	case "down", "j":
		m.configMove(rows, 1)
		return m, nil, true
	case "left", "h":
		return m, m.configCycle(rows, -1), true
	case "right", "l":
		return m, m.configCycle(rows, 1), true
	case "enter":
		return m, m.configActivate(rows), true
	}
	return m, nil, true
}

// handleConfigEditKey drives inline text editing (hand-rolled, matching
// the new-frame wizard) for cfgText / cfgSecret / cfgModel rows.
func (m *Model) handleConfigEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		m.configEditing = false
		m.configEditBuf = ""
		m.rerenderViewport()
		return m, nil, true
	case "enter":
		cmd := m.configCommitEdit()
		return m, cmd, true
	case "backspace":
		if n := len(m.configEditBuf); n > 0 {
			r := []rune(m.configEditBuf)
			m.configEditBuf = string(r[:len(r)-1])
			m.rerenderViewport()
		}
		return m, nil, true
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		m.configEditBuf += string(msg.Runes)
		m.rerenderViewport()
		return m, nil, true
	}
	if msg.String() == "space" {
		m.configEditBuf += " "
		m.rerenderViewport()
		return m, nil, true
	}
	return m, nil, true
}

func (m *Model) configMove(rows []cfgRow, dir int) {
	idx := m.configCursor
	for {
		idx += dir
		if idx < 0 || idx >= len(rows) {
			return // off the end: leave cursor where it was
		}
		if rows[idx].focusable() {
			m.configCursor = idx
			m.configErr = ""
			m.rerenderViewport()
			return
		}
	}
}

// configCycle handles left/right on the focused row: enum rows cycle in
// place (and apply immediately); other rows ignore it.
func (m *Model) configCycle(rows []cfgRow, dir int) tea.Cmd {
	if m.configCursor >= len(rows) {
		return nil
	}
	r := rows[m.configCursor]
	if r.kind != cfgEnum || len(r.enum) == 0 {
		return nil
	}
	cur := 0
	for i, v := range r.enum {
		if v == r.value {
			cur = i
			break
		}
	}
	next := (cur + dir + len(r.enum)) % len(r.enum)
	return m.applyEnum(r.id, r.enum[next])
}

// configActivate handles enter on the focused row: text-ish rows enter
// edit mode; action rows run; enum rows advance one step (so enter feels
// natural even without left/right).
func (m *Model) configActivate(rows []cfgRow) tea.Cmd {
	if m.configCursor >= len(rows) {
		return nil
	}
	r := rows[m.configCursor]
	switch r.kind {
	case cfgText, cfgSecret, cfgModel:
		m.configEditing = true
		m.configEditBuf = r.value
		// "+ add provider" pseudo-rows never reach here; real edits start
		// from the row's current value.
		m.configNotice = ""
		m.rerenderViewport()
		return nil
	case cfgEnum:
		return m.configCycle(rows, 1)
	case cfgAction:
		return m.runConfigAction(r.id)
	}
	return nil
}

// configCommitEdit applies the in-progress text edit to the focused row.
func (m *Model) configCommitEdit() tea.Cmd {
	rows := m.buildConfigRows()
	if m.configCursor >= len(rows) {
		m.configEditing = false
		return nil
	}
	val := strings.TrimSpace(m.configEditBuf)
	m.configEditing = false
	m.configEditBuf = ""
	if m.configAddingProvider {
		m.configAddingProvider = false
		return m.applyText("new_provider_name", val)
	}
	r := rows[m.configCursor]
	return m.applyText(r.id, val)
}

// --- appliers --------------------------------------------------------------

// applyEnum applies an enum change by row id, persists, and returns a
// status command.
func (m *Model) applyEnum(id, val string) tea.Cmd {
	c := m.configCfg
	switch id {
	case "skills":
		c.Skills.Convention = val
	case "theme":
		if val == "auto" {
			c.Theme.Variant = ""
		} else {
			c.Theme.Variant = val
		}
	case "frame_select":
		m.configFrame = val
		m.rerenderViewport()
		return nil // no write; just changes what's shown
	case "frame_provider":
		prov := val
		if prov == inheritLabel {
			prov = ""
		}
		if err := c.SetFrameProvider(m.configFrame, prov, frameModelRaw(c, m.configFrame)); err != nil {
			m.configErr = err.Error()
			return nil
		}
		if !m.configSave() {
			return nil
		}
		m.rerenderViewport()
		return m.maybeLiveSwap()
	case "default_provider":
		if val == inheritLabel {
			c.SetDefaultProvider("")
		} else {
			c.SetDefaultProvider(val)
		}
	default:
		return nil
	}
	if !m.configSave() {
		return nil
	}
	m.configNotice = "saved"
	m.rerenderViewport()
	return nil
}

// applyText applies a committed text edit by row id, persists, and returns
// a status command. Provider/model edits on the active frame apply live.
func (m *Model) applyText(id, val string) tea.Cmd {
	c := m.configCfg
	switch {
	case id == "name":
		c.UserName = val
		if val != "" {
			m.userName = val // live: drives the empty-state greeting
		}
	case id == "accent":
		c.Theme.Accent = val
	case id == "vault":
		c.Vault.Path = val
	case id == "frame_model":
		if err := c.SetFrameProvider(m.configFrame, frameProviderRaw(c, m.configFrame), val); err != nil {
			m.configErr = err.Error()
			return nil
		}
		if !m.configSave() {
			return nil
		}
		m.rerenderViewport()
		return m.maybeLiveSwap()
	case id == "frame_override_key":
		eff := resolveEffective(c, c.Frames.Find(m.configFrame))
		fr := c.Frames.Find(m.configFrame)
		ov := frame.ProviderOverride{}
		if fr != nil {
			ov = fr.ProviderOverride[eff.provider]
		}
		ov.APIKey = val
		if err := c.SetFrameProviderOverride(m.configFrame, eff.provider, ov); err != nil {
			m.configErr = err.Error()
			return nil
		}
		if !m.configSave() {
			return nil
		}
		m.configNotice = "saved; new keys apply on next start"
		m.rerenderViewport()
		return nil
	case strings.HasPrefix(id, "pantry:"):
		return m.applyPantryText(id, val)
	case id == "new_provider_name":
		if val == "" {
			m.configErr = "provider name is required"
			return nil
		}
		if err := c.SetProvider(val, config.ProviderConfig{}); err != nil {
			m.configErr = err.Error()
			return nil
		}
		if !m.configSave() {
			return nil
		}
		m.configNotice = "added " + val + "; fill in its key + model"
		m.focusRowByID("pantry:" + val + ":key")
		m.rerenderViewport()
		return nil
	default:
		return nil
	}
	if !m.configSave() {
		return nil
	}
	m.configNotice = "saved"
	m.rerenderViewport()
	return nil
}

// applyPantryText handles "pantry:<name>:<field>" edits.
func (m *Model) applyPantryText(id, val string) tea.Cmd {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) != 3 {
		return nil
	}
	name, field := parts[1], parts[2]
	pc, _ := m.configCfg.Provider(name)
	switch field {
	case "key":
		pc.APIKey = val
	case "model":
		pc.DefaultModel = val
	case "baseurl":
		pc.BaseURL = val
	}
	if err := m.configCfg.SetProvider(name, pc); err != nil {
		m.configErr = err.Error()
		return nil
	}
	if !m.configSave() {
		return nil
	}
	if field == "key" {
		m.configNotice = "saved; new keys apply on next start"
	} else {
		m.configNotice = "saved"
	}
	m.rerenderViewport()
	return nil
}

// runConfigAction handles cfgAction rows.
func (m *Model) runConfigAction(id string) tea.Cmd {
	switch id {
	case "add_provider":
		// Start an inline edit for the new provider's name. We stash a
		// transient row id the commit path recognizes.
		m.configEditing = true
		m.configEditBuf = ""
		m.configAddingProvider = true
		m.configNotice = "type a provider name, enter to add"
		m.rerenderViewport()
		return nil
	}
	return nil
}

// maybeLiveSwap applies an active-frame provider/model change to the
// running session via FrameUI.SwitchModel (no restart). For a non-active
// frame it is a no-op (the change is persisted and loads next start).
func (m *Model) maybeLiveSwap() tea.Cmd {
	if m.configFrame != m.frame.Active {
		m.configNotice = "saved; applies when you switch to " + m.configFrame
		return nil
	}
	if m.frame.SwitchModel == nil {
		m.configNotice = "saved"
		return nil
	}
	fr := m.configCfg.Frames.Find(m.configFrame)
	if fr == nil {
		return nil
	}
	// Resolve provider + model the same way the runtime will, so the live
	// swap matches the "resolves to" line and the next-start behavior.
	// Crucially, pass the RESOLVED provider (not the raw frame field): an
	// (inherit) selection clears fr.Provider, and SwitchModel reads "" as
	// "keep the current provider" rather than re-resolving to the default.
	eff := resolveEffective(m.configCfg, fr)
	if eff.provider == "(none)" {
		m.configNotice = "saved; configure a provider to apply it live"
		m.rerenderViewport()
		return nil
	}
	model := strings.TrimSpace(fr.Model)
	if model == "" {
		if eff.modelSource == "built-in" {
			// No concrete model anywhere. The live loop rejects an empty
			// model and can't use the "(provider default)" placeholder, so
			// persist + ask for a model rather than swap to junk.
			m.configNotice = "saved; set a model to apply it live"
			m.rerenderViewport()
			return nil
		}
		model = eff.model
	}
	rp, rm, err := m.frame.SwitchModel(eff.provider, model)
	if err != nil {
		m.configErr = "live swap failed: " + err.Error()
		return nil
	}
	m.configNotice = "now using " + rp + " : " + rm
	m.rerenderViewport()
	return nil
}

// --- small helpers ---------------------------------------------------------

func (m *Model) focusRowByID(id string) {
	rows := m.buildConfigRows()
	for i, r := range rows {
		if r.id == id {
			m.configCursor = i
			return
		}
	}
}

func frameNamesFromCfg(c *config.Config) []string {
	names := make([]string, 0, len(c.Frames.List))
	for _, f := range c.Frames.List {
		names = append(names, f.Name)
	}
	return names
}

func frameProvider(fr *frame.Frame) string {
	if fr == nil {
		return ""
	}
	return fr.Provider
}

func frameModel(fr *frame.Frame) string {
	if fr == nil {
		return ""
	}
	return fr.Model
}

func frameProviderRaw(c *config.Config, name string) string {
	if fr := c.Frames.Find(name); fr != nil {
		return fr.Provider
	}
	return ""
}

func frameModelRaw(c *config.Config, name string) string {
	if fr := c.Frames.Find(name); fr != nil {
		return fr.Model
	}
	return ""
}

func frameOverrideKey(fr *frame.Frame, provider string) string {
	if fr == nil {
		return ""
	}
	return fr.ProviderOverride[provider].APIKey
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func skillsConventionOrDefault(v string) string {
	if v == "" {
		return config.DefaultSkillsConvention
	}
	return v
}

func themeVariantOrAuto(v string) string {
	if v == "" {
		return "auto"
	}
	return v
}

// maskSecret renders a secret for display: a few leading chars, a dotted
// middle, never the full value. Empty reads as "(not set)".
func maskSecret(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(not set)"
	}
	if strings.HasPrefix(s, "env:") || strings.HasPrefix(s, "${") {
		return s // an env reference is not itself a secret; show it
	}
	r := []rune(s)
	if len(r) <= 6 {
		return "••••"
	}
	return string(r[:3]) + "••••" + string(r[len(r)-2:])
}

// --- render ----------------------------------------------------------------

// renderConfigOverlay draws the settings panel into a rounded accent box,
// windowed so the focused row stays visible within innerH.
func renderConfigOverlay(m *Model, innerW, innerH int) string {
	rows := m.buildConfigRows()

	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)

	header := titleStyle.Render("settings") + "  " + mutedStyle.Render("edit ~/.carlos/config.yaml")

	// Chrome budget inside the box: header(1) + rule(1) + blank(1) +
	// footer(1) + maybe error/notice(1) + border(2) + padding(0,1).
	footerH := 1
	statusH := 0
	if m.configErr != "" || m.configNotice != "" {
		statusH = 1
	}
	capacity := innerH - 2 /*border*/ - 1 /*header*/ - 1 /*rule*/ - 1 /*blank*/ - footerH - statusH - 1
	if capacity < 6 {
		capacity = 6
	}

	// Compute a scroll window that keeps the cursor visible.
	start := m.configScroll
	if m.configCursor < start {
		start = m.configCursor
	}
	if m.configCursor >= start+capacity {
		start = m.configCursor - capacity + 1
	}
	if start < 0 {
		start = 0
	}
	if start > len(rows)-1 {
		start = 0
	}
	m.configScroll = start
	end := start + capacity
	if end > len(rows) {
		end = len(rows)
	}

	boxInner := innerW - 4 // border(2) + padding(2)
	if boxInner < 20 {
		boxInner = 20
	}

	// clip keeps a line within the content width so it never soft-wraps
	// (ANSI-aware truncation), at any terminal size. The focused row's
	// hint is the usual overflow culprit; clipping it is acceptable since
	// the hint is supplementary.
	clip := func(s string) string { return lipgloss.NewStyle().MaxWidth(boxInner).Render(s) }

	var b strings.Builder
	b.WriteString(clip(header) + "\n")
	b.WriteString(mutedStyle.Render(dashRule(boxInner)) + "\n")
	if start > 0 {
		b.WriteString(mutedStyle.Render("  ↑ more above") + "\n")
	}
	for i := start; i < end; i++ {
		b.WriteString(clip(renderConfigRow(m, rows[i], i == m.configCursor, boxInner)) + "\n")
	}
	if end < len(rows) {
		b.WriteString(mutedStyle.Render("  ↓ more below") + "\n")
	}

	if m.configErr != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorWarn).Bold(true).Render("  ! "+m.configErr) + "\n")
	} else if m.configNotice != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorOK).Render("  ✓ "+m.configNotice) + "\n")
	}

	b.WriteString(renderConfigFooter(m))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Padding(0, 1).
		Width(innerW - 2).
		Render(b.String())
}

func renderConfigRow(m *Model, r cfgRow, focused bool, w int) string {
	muted := lipgloss.NewStyle().Foreground(colorMuted)
	subtle := lipgloss.NewStyle().Foreground(colorSubtle)
	accent := lipgloss.NewStyle().Foreground(colorAccent)

	switch r.kind {
	case cfgHeader:
		return "\n" + lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(r.label)
	case cfgSpacer:
		return ""
	case cfgInfo:
		left := ""
		if r.label != "" {
			left = subtle.Render("  " + r.label)
			if r.value != "" {
				left += "  "
			}
		} else {
			left = "  "
		}
		line := left + subtle.Render(r.value)
		if r.hint != "" {
			line += subtle.Italic(true).Render("  " + r.hint)
		}
		return line
	}

	// Focusable row: marker + label + value (or live editor) + hint.
	marker := "  "
	if focused {
		marker = accent.Bold(true).Render("▸ ")
	}
	const labelCol = 18
	label := r.label
	pad := labelCol - lipgloss.Width(label)
	if pad < 1 {
		pad = 1
	}
	labelStyled := muted.Render(label)
	if focused {
		labelStyled = accent.Render(label)
	}

	val := m.renderConfigValue(r, focused)
	line := marker + labelStyled + strings.Repeat(" ", pad) + val
	if r.tag != "" {
		line += accent.Render("  [" + r.tag + "]")
	}
	// Progressive disclosure: only the focused (non-editing) row shows its
	// hint, so the panel stays uncluttered and lines do not wrap.
	if focused && !m.configEditing && r.hint != "" {
		line += subtle.Italic(true).Render("   " + r.hint)
	}
	return line
}

// renderConfigValue renders the value cell, switching to the live editor
// when this row is being edited.
func (m *Model) renderConfigValue(r cfgRow, focused bool) string {
	accent := lipgloss.NewStyle().Foreground(colorAccent)
	val := lipgloss.NewStyle().Foreground(colorAgent)
	dim := lipgloss.NewStyle().Foreground(colorMuted)

	if focused && m.configEditing {
		shown := m.configEditBuf
		if shown == "" {
			shown = ""
		}
		return val.Render(shown) + newFrameCaret()
	}

	switch r.kind {
	case cfgSecret:
		return val.Render(maskSecret(r.value))
	case cfgEnum:
		// render the set with the current one bracketed in accent
		parts := make([]string, 0, len(r.enum))
		for _, v := range r.enum {
			if v == r.value {
				parts = append(parts, accent.Bold(true).Render("["+v+"]"))
			} else {
				parts = append(parts, dim.Render(v))
			}
		}
		return strings.Join(parts, " ")
	case cfgModel:
		if strings.TrimSpace(r.value) == "" {
			return dim.Italic(true).Render("(default)")
		}
		return val.Render(r.value)
	case cfgAction:
		return accent.Render(r.value)
	default: // cfgText
		if strings.TrimSpace(r.value) == "" {
			return dim.Italic(true).Render("(not set)")
		}
		return val.Render(r.value)
	}
}

func renderConfigFooter(m *Model) string {
	if m.configEditing {
		return "\n" + footerKey("enter") + footerLabel(" save") + footerSep() +
			footerKey("esc") + footerLabel(" cancel")
	}
	return "\n" + footerKey("↑/↓") + footerLabel(" move") + footerSep() +
		footerKey("←/→") + footerLabel(" change") + footerSep() +
		footerKey("enter") + footerLabel(" edit") + footerSep() +
		footerKey("esc") + footerLabel(" close")
}

// dashRule returns a dashed horizontal rule of width w (sandlot idiom).
func dashRule(w int) string {
	if w < 1 {
		w = 1
	}
	return strings.Repeat("┄", w)
}
