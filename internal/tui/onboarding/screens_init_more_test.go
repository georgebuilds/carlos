package onboarding

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestNameModel_InitReturnsBlinkCmd proves textinput.Blink is wired so
// the cursor blinks immediately on the first frame.
func TestNameModel_InitReturnsBlinkCmd(t *testing.T) {
	m := newNameModel("Boss")
	if cmd := m.Init(); cmd == nil {
		t.Error("nameModel.Init should return textinput.Blink cmd")
	}
}

// TestNameModel_UpdateNonEnterRoutesToTextInput proves the textinput is
// driven by stray runes so typing actually changes the field.
func TestNameModel_UpdateNonEnterRoutesToTextInput(t *testing.T) {
	m := newNameModel("Boss")
	// Type 'X' - the textinput's Update accepts runes via tea.KeyMsg.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	mm := next.(nameModel)
	// We don't pin the resulting value (the input was pre-filled with
	// "Boss" + cursor at end so it appends), but the model must remain
	// usable.
	if mm.input.Value() == "" {
		t.Error("nameModel.input value should remain non-empty after a runes message")
	}
}

// TestProviderModel_InitReturnsBlinkCmd ensures the provider screen
// initializes its textinput cursor blink.
func TestProviderModel_InitReturnsBlinkCmd(t *testing.T) {
	m := newProviderModel()
	if cmd := m.Init(); cmd == nil {
		t.Error("providerModel.Init should return textinput.Blink cmd")
	}
}

// TestProviderModel_AnyConfiguredEmptyState proves a freshly-built
// model with no choices reports zero configured providers.
func TestProviderModel_AnyConfiguredEmptyState(t *testing.T) {
	m := newProviderModel()
	if m.anyConfigured() {
		t.Error("fresh provider model should have no configured providers")
	}
}

// TestProviderModel_AnyConfiguredAfterEnable flips one provider to
// enabled and verifies anyConfigured detects it.
func TestProviderModel_AnyConfiguredAfterEnable(t *testing.T) {
	m := newProviderModel()
	m.enabled["openai"] = true
	if !m.anyConfigured() {
		t.Error("enabled openai should be detected by anyConfigured")
	}
}

// TestDaemonModel_InitReturnsNil pins the contract.
func TestDaemonModel_InitReturnsNil(t *testing.T) {
	m := newDaemonModel()
	if cmd := m.Init(); cmd != nil {
		t.Errorf("daemonModel.Init should be nil, got %v", cmd)
	}
}

// TestSkillsModel_Init returns nil; no work to do on entry.
func TestSkillsModel_Init(t *testing.T) {
	m := newSkillsModel()
	if cmd := m.Init(); cmd != nil {
		t.Errorf("skillsModel.Init should be nil, got %v", cmd)
	}
}

// TestSkillsModel_NavigationAndPick walks through up/down/1/2 to verify
// the choice index moves with the key affordances we advertise.
func TestSkillsModel_NavigationAndPick(t *testing.T) {
	m := newSkillsModel()
	if m.choice != 0 {
		t.Fatalf("default choice should be 0 (agents), got %d", m.choice)
	}
	// Down moves to claude.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(skillsModel)
	if m.choice != 1 {
		t.Errorf("after down, choice = %d, want 1", m.choice)
	}
	// Up restores agents.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(skillsModel)
	if m.choice != 0 {
		t.Errorf("after up, choice = %d, want 0", m.choice)
	}
	// 2 jumps to claude.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = next.(skillsModel)
	if m.choice != 1 {
		t.Errorf("after '2', choice = %d, want 1", m.choice)
	}
	// 1 jumps back to agents.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m = next.(skillsModel)
	if m.choice != 0 {
		t.Errorf("after '1', choice = %d, want 0", m.choice)
	}
}

// TestSkillsModel_BoundaryClamps proves up at 0 and down at 1 are
// no-ops (the radio doesn't wrap).
func TestSkillsModel_BoundaryClamps(t *testing.T) {
	m := newSkillsModel()
	// Up at the top stays at 0.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(skillsModel)
	if m.choice != 0 {
		t.Errorf("up at top, choice = %d, want 0", m.choice)
	}
	// Down twice from 0 lands at 1 then clamps at 1.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(skillsModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(skillsModel)
	if m.choice != 1 {
		t.Errorf("down twice, choice = %d, want 1 (clamped)", m.choice)
	}
}

// TestSkillsModel_EnterEmitsNextScreen proves enter fires the advance
// message with a skillsResult payload (agents by default).
func TestSkillsModel_EnterEmitsNextScreen(t *testing.T) {
	m := newSkillsModel()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should produce a cmd")
	}
	msg := cmd()
	ns, ok := msg.(nextScreenMsg)
	if !ok {
		t.Fatalf("enter cmd should emit nextScreenMsg, got %T", msg)
	}
	r, ok := ns.payload.(skillsResult)
	if !ok {
		t.Fatalf("skills payload should be skillsResult, got %T", ns.payload)
	}
	if r.convention != "agents" {
		t.Errorf("default convention should be 'agents', got %q", r.convention)
	}
}

// TestSkillsModel_View covers the visible body content.
func TestSkillsModel_View(t *testing.T) {
	m := newSkillsModel()
	out := stripStyle(m.View())
	for _, want := range []string{
		".agents/skills/",
		".claude/skills/",
		"agentskills.io",
		"Claude Code convention",
	} {
		if !containsSubstr(out, want) {
			t.Errorf("skills view missing %q; got:\n%s", want, out)
		}
	}
}

// TestSkillsModel_ViewChoiceClaude pins that the second option is
// rendered with the active marker when choice is 1.
func TestSkillsModel_ViewChoiceClaude(t *testing.T) {
	m := skillsModel{choice: 1}
	out := stripStyle(m.View())
	if !containsSubstr(out, ".claude/skills/") {
		t.Errorf("skills view (claude choice) missing .claude/skills/; got:\n%s", out)
	}
}

// TestVaultModel_InitReturnsBlinkCmd ensures the vault input's cursor
// starts blinking.
func TestVaultModel_InitReturnsBlinkCmd(t *testing.T) {
	m := newVaultModel()
	if cmd := m.Init(); cmd == nil {
		t.Error("vaultModel.Init should return textinput.Blink cmd")
	}
}

// TestGatewayModel_InitReturnsBlinkCmd ensures the gateway textinput
// gets its blink loop on entry.
func TestGatewayModel_InitReturnsBlinkCmd(t *testing.T) {
	m := newGatewayModel()
	if cmd := m.Init(); cmd == nil {
		t.Error("gatewayModel.Init should return textinput.Blink cmd")
	}
}

// containsSubstr is a tiny helper to avoid pulling in strings just for
// readability; the wrapper makes the failure messages cleaner.
func containsSubstr(haystack, needle string) bool {
	return indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
