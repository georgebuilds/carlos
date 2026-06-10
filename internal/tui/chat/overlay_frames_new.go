// Phase F-10: new-frame wizard overlay.
//
// Opens from the F-5 switcher when the user presses `n`, when they hit
// Enter while the "+ new frame" tile is focused, or via the slash
// `/frame new [name]`. Lives in the same overlay slot as the switcher
// so layout math doesn't change - when the wizard is up the switcher
// is hidden behind it. Esc returns to the switcher; Enter validates +
// fires FrameUI.AddFrame.
//
// The form is intentionally tiny: four fields, inline minimal text
// editing (printable runes + backspace), no bubbles/textinput tree.
// The wizard creates a minimum-viable frame; the user finishes
// configuration in ~/.carlos/config.yaml until the edit wizard lands
// in a sibling slice.

package chat

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/frame"
)

// Form field indices. The wizard cycles through these with Tab /
// Shift+Tab. Kept as untyped consts so test files can refer to them
// without exporting an internal type.
const (
	newFrameFieldName = iota
	newFrameFieldGlyph
	newFrameFieldAccent
	newFrameFieldStart
	newFrameFieldCount
)

// newFrameFieldName / etc. are intentionally lower-case - they're an
// implementation detail. The brief calls for ~80% coverage on the new
// code, so the field-index tests live in the test file in the same
// package and use these unexported constants directly.

// openNewFrameWizard initializes the form state and flips showNewFrame
// on. Optional pre-filled name lets `/frame new <name>` skip the first
// field. Idempotent: re-opening resets the form.
func (m *Model) openNewFrameWizard(prefillName string) {
	m.showNewFrame = true
	m.newFrame = frame.Frame{Name: prefillName}
	m.newFrameField = newFrameFieldName
	if prefillName != "" {
		m.newFrame.Glyph = frame.DefaultGlyphFor(prefillName)
	}
	// Default accent is the first palette entry so the picker always
	// has a valid selection; the user cycles with left/right.
	m.newFrameAccent = 0
	// Default to copy-personal when the template hook is wired; blank
	// when it's not (the toggle hides the copy option in render).
	m.newFrameCopy = m.frame.PersonalTemplate != nil
	m.newFrameGlyphEd = false
	m.newFrameError = ""
	m.rerenderViewport()
}

// closeNewFrameWizard returns to the switcher. Used by Esc and after a
// successful Enter. We deliberately leave switcher state untouched so
// the user lands back on the tile they came from.
func (m *Model) closeNewFrameWizard() {
	m.showNewFrame = false
	m.newFrameError = ""
	m.rerenderViewport()
}

// handleNewFrameKey is the wizard's key router. Same shape as
// handleFrameSwitcherKey so the Update routing pattern stays uniform.
func (m *Model) handleNewFrameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c":
		return m, nil, false
	case "esc":
		m.closeNewFrameWizard()
		return m, nil, true
	case "tab", "down":
		m.newFrameField = (m.newFrameField + 1) % newFrameFieldCount
		m.rerenderViewport()
		return m, nil, true
	case "shift+tab", "up":
		m.newFrameField = (m.newFrameField - 1 + newFrameFieldCount) % newFrameFieldCount
		m.rerenderViewport()
		return m, nil, true
	case "enter":
		return m, m.newFrameCommit(), true
	case "left":
		if m.newFrameField == newFrameFieldAccent {
			m.newFrameAccent = (m.newFrameAccent - 1 + len(frame.AccentPalette)) % len(frame.AccentPalette)
			m.rerenderViewport()
			return m, nil, true
		}
		if m.newFrameField == newFrameFieldStart {
			m.newFrameCopy = !m.newFrameCopy
			m.rerenderViewport()
			return m, nil, true
		}
	case "right":
		if m.newFrameField == newFrameFieldAccent {
			m.newFrameAccent = (m.newFrameAccent + 1) % len(frame.AccentPalette)
			m.rerenderViewport()
			return m, nil, true
		}
		if m.newFrameField == newFrameFieldStart {
			m.newFrameCopy = !m.newFrameCopy
			m.rerenderViewport()
			return m, nil, true
		}
	case " ":
		if m.newFrameField == newFrameFieldStart {
			m.newFrameCopy = !m.newFrameCopy
			m.rerenderViewport()
			return m, nil, true
		}
	case "backspace":
		m.newFrameBackspace()
		return m, nil, true
	}
	// Printable text falls through to the focused field's inline edit.
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		m.newFrameInsert(string(msg.Runes))
		return m, nil, true
	}
	return m, nil, true
}

// newFrameInsert appends printable runes to the focused text field.
// Glyph is single-character: each insert REPLACES the prior glyph so
// the user can correct typos without backspacing.
func (m *Model) newFrameInsert(s string) {
	switch m.newFrameField {
	case newFrameFieldName:
		m.newFrame.Name += s
		// Glyph defaults track the name until the user touches it.
		if !m.newFrameGlyphEd {
			m.newFrame.Glyph = frame.DefaultGlyphFor(m.newFrame.Name)
		}
	case newFrameFieldGlyph:
		// Single-character field; the latest keystroke wins.
		runes := []rune(s)
		if len(runes) > 0 {
			m.newFrame.Glyph = string(runes[len(runes)-1])
			m.newFrameGlyphEd = true
		}
	}
	m.rerenderViewport()
}

// newFrameBackspace deletes the last rune of the focused text field.
// No-op for accent/start fields - those have their own key bindings.
func (m *Model) newFrameBackspace() {
	switch m.newFrameField {
	case newFrameFieldName:
		runes := []rune(m.newFrame.Name)
		if len(runes) == 0 {
			return
		}
		m.newFrame.Name = string(runes[:len(runes)-1])
		if !m.newFrameGlyphEd {
			m.newFrame.Glyph = frame.DefaultGlyphFor(m.newFrame.Name)
		}
	case newFrameFieldGlyph:
		m.newFrame.Glyph = ""
		m.newFrameGlyphEd = true
	}
	m.rerenderViewport()
}

// newFrameCommit validates the form, calls AddFrame, and on success
// closes the wizard back to the switcher. On validation failure stores
// an inline error string and stays open so the user can fix it.
func (m *Model) newFrameCommit() tea.Cmd {
	name := strings.TrimSpace(m.newFrame.Name)
	switch {
	case name == "":
		m.newFrameError = "name is required"
		m.rerenderViewport()
		return nil
	case strings.ContainsAny(name, " \t"):
		m.newFrameError = "name cannot contain spaces; try kebab-case"
		m.rerenderViewport()
		return nil
	case !frame.IsValidName(name):
		m.newFrameError = "name must start with a lowercase letter; use a-z 0-9 _ -; max 31 chars"
		m.rerenderViewport()
		return nil
	}
	for _, existing := range m.frame.Available {
		if existing == name {
			m.newFrameError = "frame " + name + " already exists"
			m.rerenderViewport()
			return nil
		}
	}
	if m.frame.AddFrame == nil {
		m.newFrameError = "new-frame not wired in this session"
		m.rerenderViewport()
		return nil
	}

	// Compose the frame. Start with either the personal template
	// (provider/model/vault/system_prompt_append/mode/capabilities) or
	// a blank shell, then layer the wizard's name/glyph/accent on top.
	// Blank-shell frames pick up the orchestrator default explicitly
	// so a "just give me an empty frame" choice doesn't end up in
	// EffectiveMode's fallback path. NewPersonal makes the same call
	// for onboarding-created frames; this keeps the two surfaces
	// aligned.
	out := frame.Frame{Mode: frame.ModeOrchestrator}
	if m.newFrameCopy && m.frame.PersonalTemplate != nil {
		tmpl := m.frame.PersonalTemplate()
		out.Provider = tmpl.Provider
		out.Model = tmpl.Model
		out.ProviderOverride = tmpl.ProviderOverride
		out.VaultSubtree = tmpl.VaultSubtree
		out.SystemPromptAppend = tmpl.SystemPromptAppend
		// Only override the orchestrator seed when the template
		// itself has an explicit Mode — a template with an empty
		// Mode (legacy / partial config) should not silently
		// downgrade the new frame back into EffectiveMode's
		// fallback path.
		if tmpl.Mode != "" {
			out.Mode = tmpl.Mode
		}
		out.Capabilities = tmpl.Capabilities
	}
	out.Name = name
	out.Glyph = strings.TrimSpace(m.newFrame.Glyph)
	if out.Glyph == "" {
		out.Glyph = frame.DefaultGlyphFor(name)
	}
	out.Accent = frame.AccentPalette[m.newFrameAccent]

	if err := m.frame.AddFrame(out); err != nil {
		m.newFrameError = "create failed: " + err.Error()
		m.rerenderViewport()
		return nil
	}

	// Success: mutate FrameUI's local mirror so the switcher reflects
	// the new tile immediately, point the cursor at it, and close.
	m.frame.Available = append(m.frame.Available, name)
	m.switcherCursor = len(m.frame.Available) - 1
	m.alignPageToCursor()
	m.closeNewFrameWizard()
	return statusCmd("created frame "+name, statusInfo)
}

// renderNewFrameOverlay paints the wizard panel. innerW / innerH are
// the chat box's inner dimensions, same shape as renderFrameSwitcher.
func renderNewFrameOverlay(
	m *Model,
	innerW, innerH int,
) string {
	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	subtleStyle := lipgloss.NewStyle().Foreground(colorSubtle)
	errStyle := lipgloss.NewStyle().Foreground(colorWarn).Bold(true)

	header := titleStyle.Render("new frame") + "  " + mutedStyle.Render("compose + create")

	rows := []string{
		renderNewFrameNameRow(m, innerW),
		renderNewFrameGlyphRow(m, innerW),
		renderNewFrameAccentRow(m, innerW),
		renderNewFrameStartRow(m, innerW),
	}

	body := strings.Join(rows, "\n")

	footer := subtleStyle.Render("  ") +
		footerKey("tab") + footerLabel(" next") +
		footerSep() +
		footerKey("shift-tab") + footerLabel(" prev") +
		footerSep() +
		footerKey("enter") + footerLabel(" create") +
		footerSep() +
		footerKey("esc") + footerLabel(" cancel")

	parts := []string{header, "", body, ""}
	if m.newFrameError != "" {
		parts = append(parts, errStyle.Render("  "+m.newFrameError), "")
	}
	parts = append(parts, footer)
	block := strings.Join(parts, "\n")

	// Vertically center-ish, mirroring the switcher's cosmetic pad.
	if innerH > lipgloss.Height(block)+2 {
		topPad := (innerH - lipgloss.Height(block)) / 3
		if topPad > 0 {
			block = strings.Repeat("\n", topPad) + block
		}
	}
	return block
}

func footerKey(s string) string {
	return lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(s)
}

func footerLabel(s string) string {
	return lipgloss.NewStyle().Foreground(colorMuted).Render(s)
}

func footerSep() string {
	return lipgloss.NewStyle().Foreground(colorMuted).Render("  ·  ")
}

// renderNewFrameNameRow paints "name:  <value>" with a leading focus
// marker on the active field. The minimal text edit is just the
// current Name with a trailing "_" caret when focused.
func renderNewFrameNameRow(m *Model, w int) string {
	focused := m.newFrameField == newFrameFieldName
	label := newFrameLabel("name", focused)
	val := m.newFrame.Name
	if focused {
		val += newFrameCaret()
	}
	if val == "" {
		val = subtleHint("required, kebab-case")
	}
	return newFrameRowFrame(focused, label, val, w)
}

func renderNewFrameGlyphRow(m *Model, w int) string {
	focused := m.newFrameField == newFrameFieldGlyph
	label := newFrameLabel("glyph", focused)
	val := m.newFrame.Glyph
	if focused {
		val += newFrameCaret()
	}
	if val == "" {
		val = subtleHint("single character; defaults from name")
	}
	return newFrameRowFrame(focused, label, val, w)
}

// renderNewFrameAccentRow paints all 8 palette names in a row; the
// current one is wrapped in [brackets] in its accent color.
func renderNewFrameAccentRow(m *Model, w int) string {
	focused := m.newFrameField == newFrameFieldAccent
	label := newFrameLabel("accent", focused)
	parts := make([]string, 0, len(frame.AccentPalette))
	for i, name := range frame.AccentPalette {
		col := frame.AccentColor(name)
		st := lipgloss.NewStyle().Foreground(col)
		if i == m.newFrameAccent {
			parts = append(parts, st.Bold(true).Render("["+name+"]"))
		} else {
			parts = append(parts, st.Render(" "+name+" "))
		}
	}
	val := strings.Join(parts, " ")
	return newFrameRowFrame(focused, label, val, w)
}

// renderNewFrameStartRow paints the start-from toggle. Hides the
// copy-personal option when PersonalTemplate isn't wired so the user
// isn't offered an empty action.
func renderNewFrameStartRow(m *Model, w int) string {
	focused := m.newFrameField == newFrameFieldStart
	label := newFrameLabel("start-from", focused)
	val := newFrameToggleText(m)
	return newFrameRowFrame(focused, label, val, w)
}

func newFrameToggleText(m *Model) string {
	on := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	off := lipgloss.NewStyle().Foreground(colorMuted)
	copyMark, blankMark := "( )", "( )"
	if m.newFrameCopy {
		copyMark = "[*]"
	} else {
		blankMark = "[*]"
	}
	if m.frame.PersonalTemplate == nil {
		return off.Render(copyMark+" copy personal (not wired)") + "  " +
			on.Render(blankMark+" blank")
	}
	return on.Render(copyMark+" copy personal") + "  " +
		off.Render(blankMark+" blank")
}

func newFrameLabel(name string, focused bool) string {
	if focused {
		return lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(name)
	}
	return lipgloss.NewStyle().Foreground(colorMuted).Render(name)
}

func newFrameCaret() string {
	return lipgloss.NewStyle().Foreground(colorAccent).Render("_")
}

func subtleHint(s string) string {
	return lipgloss.NewStyle().Foreground(colorSubtle).Italic(true).Render(s)
}

// newFrameRowFrame paints one form row with the focused row sporting a
// thin left margin in the accent color so the eye finds it quickly.
func newFrameRowFrame(focused bool, label, val string, w int) string {
	marker := "  "
	if focused {
		marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸ ")
	}
	// Pad the label to a fixed column so values line up vertically.
	const labelCol = 12
	pad := labelCol - lipgloss.Width(label)
	if pad < 1 {
		pad = 1
	}
	return marker + label + strings.Repeat(" ", pad) + val
}
