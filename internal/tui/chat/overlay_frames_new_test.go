package chat

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/frame"
)

// newWizardModel boots a model with the wizard's FrameUI wiring. Optional
// addFn / tmplFn hook into the AddFrame / PersonalTemplate closures the
// production code passes in.
func newWizardModel(
	t *testing.T,
	available []string,
	addFn func(frame.Frame) error,
	tmplFn func() frame.Frame,
) *Model {
	t.Helper()
	ui := FrameUI{
		Active:           "personal",
		Glyph:            "◉",
		Accent:           "cream",
		Available:        append([]string{}, available...),
		SwitchActive:     func(string) error { return nil },
		AddFrame:         addFn,
		PersonalTemplate: tmplFn,
	}
	return newFramedModel(t, ui)
}

func runeKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func namedKey(typ tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: typ}
}

func TestNewFrame_OpenSetsDefaults(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, func() frame.Frame {
		return frame.Frame{Provider: "anthropic", Model: "claude-sonnet-4-6"}
	})
	m.openNewFrameWizard("")
	if !m.showNewFrame {
		t.Fatal("openNewFrameWizard did not flip showNewFrame")
	}
	if m.newFrameField != newFrameFieldName {
		t.Errorf("field = %d, want %d (name)", m.newFrameField, newFrameFieldName)
	}
	if m.newFrameAccent != 0 {
		t.Errorf("accent index = %d, want 0", m.newFrameAccent)
	}
	if !m.newFrameCopy {
		t.Errorf("copy-personal should default to true when PersonalTemplate is wired")
	}
	if m.newFrame.Name != "" {
		t.Errorf("name should start empty; got %q", m.newFrame.Name)
	}
}

func TestNewFrame_OpenWithPrefillFillsNameAndGlyph(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, nil, nil)
	m.openNewFrameWizard("research")
	if m.newFrame.Name != "research" {
		t.Errorf("prefill name = %q, want research", m.newFrame.Name)
	}
	if m.newFrame.Glyph != frame.DefaultGlyphFor("research") {
		t.Errorf("prefill glyph = %q, want %q", m.newFrame.Glyph, frame.DefaultGlyphFor("research"))
	}
}

func TestNewFrame_RendersAt80x24(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	m.width = 80
	m.height = 24
	m.openNewFrameWizard("")
	out := renderNewFrameOverlay(m, 76, 18)
	for _, want := range []string{"name", "glyph", "accent", "start-from", "tab", "enter"} {
		if !strings.Contains(out, want) {
			t.Errorf("80x24 render missing %q\n%s", want, out)
		}
	}
}

func TestNewFrame_RendersAt120x40(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	m.width = 120
	m.height = 40
	m.openNewFrameWizard("")
	out := renderNewFrameOverlay(m, 116, 32)
	for _, want := range []string{"name", "glyph", "accent", "start-from"} {
		if !strings.Contains(out, want) {
			t.Errorf("120x40 render missing %q\n%s", want, out)
		}
	}
}

func TestNewFrame_TabCyclesFields(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	m.openNewFrameWizard("")
	for want := 1; want < newFrameFieldCount; want++ {
		m.handleNewFrameKey(namedKey(tea.KeyTab))
		if m.newFrameField != want {
			t.Errorf("after %d tabs, field = %d, want %d", want, m.newFrameField, want)
		}
	}
	// Tab past the last field wraps back to name.
	m.handleNewFrameKey(namedKey(tea.KeyTab))
	if m.newFrameField != newFrameFieldName {
		t.Errorf("tab past last field should wrap to name; got %d", m.newFrameField)
	}
}

func TestNewFrame_ShiftTabPrev(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	m.openNewFrameWizard("")
	m.handleNewFrameKey(namedKey(tea.KeyShiftTab))
	if m.newFrameField != newFrameFieldCount-1 {
		t.Errorf("shift-tab from name should wrap to last field; got %d", m.newFrameField)
	}
}

func TestNewFrame_TypeIntoName(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	m.openNewFrameWizard("")
	for _, r := range "work" {
		m.handleNewFrameKey(runeKey(string(r)))
	}
	if m.newFrame.Name != "work" {
		t.Errorf("name = %q, want work", m.newFrame.Name)
	}
	// Glyph default tracks name when user hasn't touched it.
	if m.newFrame.Glyph != frame.DefaultGlyphFor("work") {
		t.Errorf("glyph default = %q, want %q", m.newFrame.Glyph, frame.DefaultGlyphFor("work"))
	}
}

func TestNewFrame_BackspaceName(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	m.openNewFrameWizard("work")
	if m.newFrame.Name != "work" {
		t.Fatal("prefill failed")
	}
	m.handleNewFrameKey(namedKey(tea.KeyBackspace))
	if m.newFrame.Name != "wor" {
		t.Errorf("after backspace, name = %q, want wor", m.newFrame.Name)
	}
}

func TestNewFrame_GlyphEditStopsAutoTracking(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	m.openNewFrameWizard("")
	// Type into name.
	for _, r := range "work" {
		m.handleNewFrameKey(runeKey(string(r)))
	}
	wantDefault := frame.DefaultGlyphFor("work")
	if m.newFrame.Glyph != wantDefault {
		t.Fatalf("glyph default not tracking; got %q want %q", m.newFrame.Glyph, wantDefault)
	}
	// Move to glyph field, type custom glyph.
	m.handleNewFrameKey(namedKey(tea.KeyTab))
	m.handleNewFrameKey(runeKey("X"))
	if m.newFrame.Glyph != "X" {
		t.Errorf("glyph edit failed; got %q", m.newFrame.Glyph)
	}
	// Now editing the name should NOT clobber the user's glyph.
	m.handleNewFrameKey(namedKey(tea.KeyShiftTab))
	m.handleNewFrameKey(runeKey("s"))
	if m.newFrame.Glyph != "X" {
		t.Errorf("editing name after glyph touched should not auto-track; got %q", m.newFrame.Glyph)
	}
}

func TestNewFrame_AccentLeftRightCycles(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	m.openNewFrameWizard("")
	// Move to accent.
	m.handleNewFrameKey(namedKey(tea.KeyTab))
	m.handleNewFrameKey(namedKey(tea.KeyTab))
	if m.newFrameField != newFrameFieldAccent {
		t.Fatalf("expected accent field; got %d", m.newFrameField)
	}
	// Right cycles forward.
	m.handleNewFrameKey(namedKey(tea.KeyRight))
	if m.newFrameAccent != 1 {
		t.Errorf("right from 0 should land on 1; got %d", m.newFrameAccent)
	}
	// Left from 0 wraps to last.
	m.newFrameAccent = 0
	m.handleNewFrameKey(namedKey(tea.KeyLeft))
	if m.newFrameAccent != len(frame.AccentPalette)-1 {
		t.Errorf("left from 0 should wrap; got %d", m.newFrameAccent)
	}
	// Right from last wraps to 0.
	m.newFrameAccent = len(frame.AccentPalette) - 1
	m.handleNewFrameKey(namedKey(tea.KeyRight))
	if m.newFrameAccent != 0 {
		t.Errorf("right from last should wrap; got %d", m.newFrameAccent)
	}
}

func TestNewFrame_StartFromToggle(t *testing.T) {
	m := newWizardModel(t, []string{"personal"},
		func(frame.Frame) error { return nil },
		func() frame.Frame { return frame.Frame{Provider: "anthropic"} },
	)
	m.openNewFrameWizard("")
	// Walk to start-from field.
	for i := 0; i < 3; i++ {
		m.handleNewFrameKey(namedKey(tea.KeyTab))
	}
	if m.newFrameField != newFrameFieldStart {
		t.Fatalf("expected start field; got %d", m.newFrameField)
	}
	startCopy := m.newFrameCopy
	m.handleNewFrameKey(namedKey(tea.KeyLeft))
	if m.newFrameCopy == startCopy {
		t.Errorf("left should toggle start-from")
	}
	m.handleNewFrameKey(namedKey(tea.KeyRight))
	if m.newFrameCopy != startCopy {
		t.Errorf("right should toggle back")
	}
	m.handleNewFrameKey(runeKey(" "))
	if m.newFrameCopy == startCopy {
		t.Errorf("space should toggle start-from")
	}
}

func TestNewFrame_EnterEmptyNameShowsError(t *testing.T) {
	called := false
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error {
		called = true
		return nil
	}, nil)
	m.openNewFrameWizard("")
	_, cmd, ok := m.handleNewFrameKey(namedKey(tea.KeyEnter))
	if !ok {
		t.Fatal("enter not handled")
	}
	if cmd != nil {
		t.Errorf("expected nil cmd on validation error; got %T", cmd())
	}
	if called {
		t.Errorf("AddFrame should not fire on empty name")
	}
	if m.newFrameError == "" {
		t.Errorf("expected inline error")
	}
	if !m.showNewFrame {
		t.Errorf("wizard should stay open on validation error")
	}
}

func TestNewFrame_EnterDuplicateNameShowsError(t *testing.T) {
	called := false
	m := newWizardModel(t, []string{"personal", "work"}, func(frame.Frame) error {
		called = true
		return nil
	}, nil)
	m.openNewFrameWizard("work")
	_, _, _ = m.handleNewFrameKey(namedKey(tea.KeyEnter))
	if called {
		t.Errorf("AddFrame should not fire on duplicate")
	}
	if !strings.Contains(m.newFrameError, "already exists") {
		t.Errorf("expected 'already exists' error; got %q", m.newFrameError)
	}
	if !m.showNewFrame {
		t.Errorf("wizard should stay open on validation error")
	}
}

func TestNewFrame_EnterValidInputCallsAddFrameAndCloses(t *testing.T) {
	var captured frame.Frame
	m := newWizardModel(t, []string{"personal"}, func(f frame.Frame) error {
		captured = f
		return nil
	}, nil)
	m.openNewFrameWizard("research")
	_, cmd, ok := m.handleNewFrameKey(namedKey(tea.KeyEnter))
	if !ok {
		t.Fatal("enter not handled")
	}
	if cmd == nil {
		t.Fatal("expected status cmd on success")
	}
	s, isStatus := cmd().(statusMsg)
	if !isStatus {
		t.Fatalf("expected statusMsg; got %T", cmd())
	}
	if !strings.Contains(s.text, "research") {
		t.Errorf("status should mention name; got %q", s.text)
	}
	if captured.Name != "research" {
		t.Errorf("AddFrame received name %q, want research", captured.Name)
	}
	if captured.Accent != frame.AccentPalette[0] {
		t.Errorf("accent = %q, want %q", captured.Accent, frame.AccentPalette[0])
	}
	if m.showNewFrame {
		t.Errorf("wizard should close on success")
	}
	// Available list now includes the new frame.
	found := false
	for _, n := range m.frame.Available {
		if n == "research" {
			found = true
		}
	}
	if !found {
		t.Errorf("Available should include the new frame; got %v", m.frame.Available)
	}
}

func TestNewFrame_EnterAddFrameErrorShowsInline(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error {
		return errors.New("disk full")
	}, nil)
	m.openNewFrameWizard("research")
	_, cmd, _ := m.handleNewFrameKey(namedKey(tea.KeyEnter))
	if cmd != nil {
		t.Errorf("expected nil cmd on hook error; got %T", cmd())
	}
	if !strings.Contains(m.newFrameError, "disk full") {
		t.Errorf("expected hook err inline; got %q", m.newFrameError)
	}
	if !m.showNewFrame {
		t.Errorf("wizard should stay open on hook error")
	}
}

func TestNewFrame_EnterAddFrameUnwiredShowsError(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, nil, nil)
	m.openNewFrameWizard("research")
	_, _, _ = m.handleNewFrameKey(namedKey(tea.KeyEnter))
	if !strings.Contains(m.newFrameError, "not wired") {
		t.Errorf("expected 'not wired' error; got %q", m.newFrameError)
	}
}

func TestNewFrame_EscClosesWithoutFiring(t *testing.T) {
	called := false
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error {
		called = true
		return nil
	}, nil)
	m.openNewFrameWizard("research")
	_, _, ok := m.handleNewFrameKey(namedKey(tea.KeyEsc))
	if !ok {
		t.Fatal("esc not handled")
	}
	if called {
		t.Errorf("esc should not fire AddFrame")
	}
	if m.showNewFrame {
		t.Errorf("esc should close wizard")
	}
}

func TestNewFrame_CopyPersonalCopiesFields(t *testing.T) {
	var captured frame.Frame
	m := newWizardModel(t, []string{"personal"}, func(f frame.Frame) error {
		captured = f
		return nil
	}, func() frame.Frame {
		return frame.Frame{
			Provider:           "anthropic",
			Model:              "claude-sonnet-4-6",
			VaultSubtree:       "personal/",
			SystemPromptAppend: "Tone: relaxed.",
			Mode:               frame.ModeSolo,
		}
	})
	m.openNewFrameWizard("client")
	if !m.newFrameCopy {
		t.Fatal("copy-personal default should be true")
	}
	_, _, _ = m.handleNewFrameKey(namedKey(tea.KeyEnter))
	if captured.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic", captured.Provider)
	}
	if captured.Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want claude-sonnet-4-6", captured.Model)
	}
	if captured.VaultSubtree != "personal/" {
		t.Errorf("VaultSubtree = %q, want personal/", captured.VaultSubtree)
	}
	if captured.SystemPromptAppend != "Tone: relaxed." {
		t.Errorf("SystemPromptAppend = %q, want copy", captured.SystemPromptAppend)
	}
	if captured.Mode != frame.ModeSolo {
		t.Errorf("Mode = %q, want solo", captured.Mode)
	}
	// Name + accent come from the wizard, not the template.
	if captured.Name != "client" {
		t.Errorf("Name should be wizard's, not template's; got %q", captured.Name)
	}
}

func TestNewFrame_BlankToggleZeroes(t *testing.T) {
	var captured frame.Frame
	m := newWizardModel(t, []string{"personal"}, func(f frame.Frame) error {
		captured = f
		return nil
	}, func() frame.Frame {
		return frame.Frame{Provider: "anthropic", Model: "claude-sonnet-4-6"}
	})
	m.openNewFrameWizard("blank-one")
	// Flip to blank.
	for i := 0; i < 3; i++ {
		m.handleNewFrameKey(namedKey(tea.KeyTab))
	}
	m.handleNewFrameKey(runeKey(" "))
	if m.newFrameCopy {
		t.Fatal("space should have flipped copy to blank")
	}
	_, _, _ = m.handleNewFrameKey(namedKey(tea.KeyEnter))
	if captured.Provider != "" {
		t.Errorf("blank should not inherit provider; got %q", captured.Provider)
	}
	if captured.Model != "" {
		t.Errorf("blank should not inherit model; got %q", captured.Model)
	}
	if captured.Name != "blank-one" {
		t.Errorf("Name = %q, want blank-one", captured.Name)
	}
}

// TestNewFrame_BlankShellDefaultsToOrchestratorMode pins the
// "first-run consistency" contract: a freshly-created blank frame
// must land with Mode = orchestrator, matching what NewPersonal /
// onboarding write for the default install. Without this, the
// blank-shell branch produced a Mode-empty frame that surfaced as
// solo via EffectiveMode (when its fallback was solo) or — even
// after the EffectiveMode flip — as an inconsistent "no explicit
// mode" record that any future safety-stance refactor would treat
// as no-delegation.
func TestNewFrame_BlankShellDefaultsToOrchestratorMode(t *testing.T) {
	var captured frame.Frame
	m := newWizardModel(t, []string{"personal"}, func(f frame.Frame) error {
		captured = f
		return nil
	}, func() frame.Frame {
		return frame.Frame{Provider: "anthropic", Model: "claude-sonnet-4-6"}
	})
	m.openNewFrameWizard("blank-mode")
	// Flip to blank, same dance as TestNewFrame_BlankToggleZeroes.
	for i := 0; i < 3; i++ {
		m.handleNewFrameKey(namedKey(tea.KeyTab))
	}
	m.handleNewFrameKey(runeKey(" "))
	if m.newFrameCopy {
		t.Fatal("space should have flipped copy to blank")
	}
	_, _, _ = m.handleNewFrameKey(namedKey(tea.KeyEnter))
	if captured.Mode != frame.ModeOrchestrator {
		t.Errorf("blank-shell new frame Mode = %q, want %q", captured.Mode, frame.ModeOrchestrator)
	}
}

// TestNewFrame_CopyTemplateWithEmptyModeFallsBackToOrchestrator
// covers the partial-template case: if the personal template happens
// to have an empty Mode (legacy / migrated-pre-modes config), the
// copy path must NOT downgrade the new frame into the
// no-explicit-mode state. The wizard's seed orchestrator value
// should survive when the template can't override it.
func TestNewFrame_CopyTemplateWithEmptyModeFallsBackToOrchestrator(t *testing.T) {
	var captured frame.Frame
	m := newWizardModel(t, []string{"personal"}, func(f frame.Frame) error {
		captured = f
		return nil
	}, func() frame.Frame {
		// Note: Mode intentionally left empty.
		return frame.Frame{Provider: "anthropic", Model: "claude-sonnet-4-6"}
	})
	m.openNewFrameWizard("legacy-template")
	_, _, _ = m.handleNewFrameKey(namedKey(tea.KeyEnter))
	if captured.Mode != frame.ModeOrchestrator {
		t.Errorf("copy from empty-Mode template should keep the orchestrator seed; got Mode = %q", captured.Mode)
	}
	// Sanity check that the template's other fields DID copy through.
	if captured.Provider != "anthropic" {
		t.Errorf("template provider lost: %q", captured.Provider)
	}
}

// TestNewFrame_CopyTemplateWithExplicitSoloIsHonoured guards
// against an over-eager default. When the user's personal frame is
// explicitly solo and they choose "copy personal", the new frame
// should inherit solo — not be silently promoted to orchestrator by
// the wizard's seed.
func TestNewFrame_CopyTemplateWithExplicitSoloIsHonoured(t *testing.T) {
	var captured frame.Frame
	m := newWizardModel(t, []string{"personal"}, func(f frame.Frame) error {
		captured = f
		return nil
	}, func() frame.Frame {
		return frame.Frame{
			Provider: "anthropic",
			Model:    "claude-sonnet-4-6",
			Mode:     frame.ModeSolo,
		}
	})
	m.openNewFrameWizard("intentional-solo")
	_, _, _ = m.handleNewFrameKey(namedKey(tea.KeyEnter))
	if captured.Mode != frame.ModeSolo {
		t.Errorf("explicit solo on template should not be overridden by wizard seed; got %q", captured.Mode)
	}
}

func TestNewFrame_SwitcherNKeyOpensWizard(t *testing.T) {
	m := newWizardModel(t, []string{"personal", "work"}, func(frame.Frame) error { return nil }, nil)
	m.openFrameSwitcher()
	if m.showNewFrame {
		t.Fatal("wizard should start closed")
	}
	_, _, ok := m.handleFrameSwitcherKey(key("n"))
	if !ok {
		t.Fatal("n not handled by switcher")
	}
	if !m.showNewFrame {
		t.Errorf("n should open the wizard")
	}
}

func TestNewFrame_SwitcherEnterOnNewTileOpensWizard(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	m.openFrameSwitcher()
	// Move cursor to the "+ new frame" tile, which lives at index
	// len(Available).
	m.switcherCursor = len(m.frame.Available)
	_, _, ok := m.handleFrameSwitcherKey(key("enter"))
	if !ok {
		t.Fatal("enter not handled")
	}
	if !m.showNewFrame {
		t.Errorf("enter on '+ new frame' should open the wizard")
	}
}

func TestNewFrame_SwitcherCursorReachesNewTile(t *testing.T) {
	// With 4 frames @ width 120 (3 cols), the placeholder lives at
	// index 4. Cursor at 3 (bottom-left) → right → 4.
	m := newWizardModel(t, []string{"personal", "work", "research", "writing"}, func(frame.Frame) error { return nil }, nil)
	m.openFrameSwitcher()
	m.switcherCursor = 3
	m.handleFrameSwitcherKey(key("right"))
	if m.switcherCursor != 4 {
		t.Errorf("right from 3 should reach the new-frame tile at 4; got %d", m.switcherCursor)
	}
}

func TestNewFrame_FrameSlashNewOpensWizard(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	cmd := m.frameSlash("new")
	if cmd != nil {
		t.Errorf("/frame new should return nil cmd (opens overlay synchronously); got %T", cmd())
	}
	if !m.showNewFrame {
		t.Errorf("/frame new should open the wizard")
	}
	if m.newFrame.Name != "" {
		t.Errorf("/frame new (no arg) should not prefill name; got %q", m.newFrame.Name)
	}
}

func TestNewFrame_FrameSlashNewWithNamePrefills(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	_ = m.frameSlash("new research")
	if !m.showNewFrame {
		t.Errorf("/frame new <name> should open the wizard")
	}
	if m.newFrame.Name != "research" {
		t.Errorf("/frame new <name> should prefill name; got %q", m.newFrame.Name)
	}
	if m.newFrame.Glyph != frame.DefaultGlyphFor("research") {
		t.Errorf("/frame new <name> should fill default glyph; got %q", m.newFrame.Glyph)
	}
}

func TestNewFrame_NameWithSpacesRejected(t *testing.T) {
	called := false
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error {
		called = true
		return nil
	}, nil)
	m.openNewFrameWizard("")
	// Type "my frame" via inserts.
	for _, r := range "my frame" {
		m.handleNewFrameKey(runeKey(string(r)))
	}
	_, _, _ = m.handleNewFrameKey(namedKey(tea.KeyEnter))
	if called {
		t.Errorf("name with space should not call AddFrame")
	}
	if !strings.Contains(m.newFrameError, "space") {
		t.Errorf("expected space-rejection error; got %q", m.newFrameError)
	}
}

func TestNewFrame_DefaultCopyFalseWhenNoTemplate(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	m.openNewFrameWizard("")
	if m.newFrameCopy {
		t.Errorf("copy-personal should default to false when PersonalTemplate is nil")
	}
}

// TestNewFrame_PathInjectionRejected pins the security gate: a name
// that would escape ~/.carlos/frames/<name>/ via "../" must show an
// inline error and never call AddFrame. The wizard's old check rejected
// spaces + duplicates but happily accepted "../escape", which builds
// ~/.carlos/escape via PathsFor.
func TestNewFrame_PathInjectionRejected(t *testing.T) {
	called := false
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error {
		called = true
		return nil
	}, nil)
	m.openNewFrameWizard("")
	for _, r := range "../escape" {
		m.handleNewFrameKey(runeKey(string(r)))
	}
	_, _, _ = m.handleNewFrameKey(namedKey(tea.KeyEnter))
	if called {
		t.Errorf("AddFrame should not fire on path-escape name")
	}
	if m.newFrameError == "" {
		t.Errorf("expected inline validation error")
	}
	if !m.showNewFrame {
		t.Errorf("wizard should stay open on validation error")
	}
}

// TestNewFrame_NonCanonicalNamesRejected walks the gate against the
// other shapes IsValidName covers (capitals, digit-start, separator,
// at-sign). Each one must fail at commit time.
func TestNewFrame_NonCanonicalNamesRejected(t *testing.T) {
	bad := []string{"Personal", "123foo", "work/x", "foo.bar"}
	for _, n := range bad {
		t.Run(n, func(t *testing.T) {
			called := false
			m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error {
				called = true
				return nil
			}, nil)
			m.openNewFrameWizard("")
			for _, r := range n {
				m.handleNewFrameKey(runeKey(string(r)))
			}
			_, _, _ = m.handleNewFrameKey(namedKey(tea.KeyEnter))
			if called {
				t.Errorf("AddFrame fired on bad name %q", n)
			}
			if m.newFrameError == "" {
				t.Errorf("no inline error for bad name %q", n)
			}
		})
	}
}
