package onboarding

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestProviderScreen_SetLaterWritesDisabledEntry walks the provider
// screen pressing [l] for the first entry and [n] for the rest.
// Expected result: the chosen provider lands in the result map with an
// empty secret (the "set later" placeholder); skipped providers are
// absent.
func TestProviderScreen_SetLaterWritesDisabledEntry(t *testing.T) {
	m := newProviderModel()
	// First provider: anthropic → set later (l).
	next, _ := m.Update(tea.KeyMsg{Runes: []rune{'l'}, Type: tea.KeyRunes})
	m = next.(providerModel)
	// Walk past the remaining providers by pressing n on each.
	for m.stage == stageAsking {
		next, _ = m.Update(tea.KeyMsg{Runes: []rune{'n'}, Type: tea.KeyRunes})
		m = next.(providerModel)
	}
	res := m.toResult()
	pc, ok := res.providers["anthropic"]
	if !ok {
		t.Fatalf("set-later anthropic should appear in result; got %+v", res.providers)
	}
	if pc.APIKey != "" || pc.BaseURL != "" {
		t.Errorf("set-later entry should have empty secret; got %+v", pc)
	}
	// Default provider stays empty since nothing was actually configured.
	if res.defaultProvider != "" {
		t.Errorf("set-later only: defaultProvider should stay empty; got %q", res.defaultProvider)
	}
	// Skipped providers (n) are not in the map.
	for _, name := range []string{"openai", "gemini", "openrouter", "ollama"} {
		if _, ok := res.providers[name]; ok {
			t.Errorf("%s was skipped (n) and should not appear in result", name)
		}
	}
}

// TestProviderScreen_AskingPromptListsThreeChoices verifies the new
// y/l/n affordance is rendered to the user.
func TestProviderScreen_AskingPromptListsThreeChoices(t *testing.T) {
	m := newProviderModel()
	out := stripStyle(m.View())
	for _, want := range []string{"[y]", "[l]", "[n]", "configure now", "set later", "skip"} {
		if !strings.Contains(out, want) {
			t.Errorf("provider prompt missing %q; got:\n%s", want, out)
		}
	}
}

// TestProviderScreen_EmptyErrorMessageDropsPressNHint pins the new
// copy: the error message no longer asks the user to unwind back to a
// previous step.
func TestProviderScreen_EmptyErrorMessageDropsPressNHint(t *testing.T) {
	m := newProviderModel()
	// Press y to enter the secret stage, then enter on empty input.
	next, _ := m.Update(tea.KeyMsg{Runes: []rune{'y'}, Type: tea.KeyRunes})
	m = next.(providerModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(providerModel)
	if m.warn == "" {
		t.Fatal("expected a warn on empty secret")
	}
	if strings.Contains(m.warn, "press [n] on the previous step") {
		t.Errorf("error copy should drop the unwind hint; got %q", m.warn)
	}
}
