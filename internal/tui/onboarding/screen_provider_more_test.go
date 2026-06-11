package onboarding

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// enableProvider drives the provider screen to configure the named
// provider (which must be the one at the cursor) by pressing y then
// typing the secret and enter. Returns the resulting model.
func enableCurrentProvider(t *testing.T, m providerModel, secret string) providerModel {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Runes: []rune{'y'}, Type: tea.KeyRunes})
	m = next.(providerModel)
	if m.stage != stageEntering {
		t.Fatalf("y should enter the secret stage; got %v", m.stage)
	}
	m.input.SetValue(secret)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(providerModel)
}

// TestProviderScreen_ConfiguredAPIKeyProducesResult walks anthropic →
// configure now, then skips the rest, and asserts the API-key result
// plus first-configured-wins default selection.
func TestProviderScreen_ConfiguredAPIKeyProducesResult(t *testing.T) {
	m := newProviderModel() // cursor on anthropic
	m = enableCurrentProvider(t, m, "sk-test-123")
	// Skip the remaining providers.
	for m.stage == stageAsking {
		next, _ := m.Update(tea.KeyMsg{Runes: []rune{'n'}, Type: tea.KeyRunes})
		m = next.(providerModel)
	}
	res := m.toResult()
	pc, ok := res.providers["anthropic"]
	if !ok {
		t.Fatalf("anthropic should be in result; got %+v", res.providers)
	}
	if pc.APIKey != "sk-test-123" {
		t.Errorf("APIKey not stored; got %+v", pc)
	}
	if res.defaultProvider != "anthropic" {
		t.Errorf("first-configured should win default; got %q", res.defaultProvider)
	}
}

// TestProviderScreen_OllamaURLPath drives the URL-typed provider (ollama)
// to exercise toResult's BaseURL branch and the URL input seeding.
func TestProviderScreen_OllamaURLPath(t *testing.T) {
	m := newProviderModel()
	// Walk to ollama (the only isURL entry, last in the list).
	for providerEntries[m.idx].name != "ollama" {
		next, _ := m.Update(tea.KeyMsg{Runes: []rune{'n'}, Type: tea.KeyRunes})
		m = next.(providerModel)
	}
	// Press y: URL providers pre-fill the default and use plain echo.
	next, _ := m.Update(tea.KeyMsg{Runes: []rune{'y'}, Type: tea.KeyRunes})
	m = next.(providerModel)
	if m.input.Value() != "http://localhost:11434" {
		t.Errorf("ollama should pre-fill the default URL; got %q", m.input.Value())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(providerModel)
	res := m.toResult()
	pc, ok := res.providers["ollama"]
	if !ok {
		t.Fatalf("ollama should appear; got %+v", res.providers)
	}
	if pc.BaseURL != "http://localhost:11434" {
		t.Errorf("ollama should store BaseURL; got %+v", pc)
	}
	if pc.APIKey != "" {
		t.Errorf("URL provider should not set APIKey; got %+v", pc)
	}
}

// TestProviderScreen_EscBacksOutOfEntering covers the esc arm that
// returns from secret entry to the y/n prompt without advancing.
func TestProviderScreen_EscBacksOutOfEntering(t *testing.T) {
	m := newProviderModel()
	next, _ := m.Update(tea.KeyMsg{Runes: []rune{'y'}, Type: tea.KeyRunes})
	m = next.(providerModel)
	idxBefore := m.idx
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(providerModel)
	if m.stage != stageAsking {
		t.Errorf("esc should return to asking stage; got %v", m.stage)
	}
	if m.idx != idxBefore {
		t.Errorf("esc should not advance the provider index; got %d want %d", m.idx, idxBefore)
	}
}

// TestProviderScreen_EnteringTypingRoutesToInput covers the input-routing
// tail of the stageEntering arm (non-enter/esc keys go to the textinput).
func TestProviderScreen_EnteringTypingRoutesToInput(t *testing.T) {
	m := newProviderModel()
	next, _ := m.Update(tea.KeyMsg{Runes: []rune{'y'}, Type: tea.KeyRunes})
	m = next.(providerModel)
	m.input.SetValue("")
	next, _ = m.Update(tea.KeyMsg{Runes: []rune{'k'}, Type: tea.KeyRunes})
	m = next.(providerModel)
	if m.input.Value() != "k" {
		t.Errorf("typed key should route to input; got %q", m.input.Value())
	}
}

// TestProviderScreen_ReviewReconfigureLoops covers stageReviewing's [r]
// arm: it resets to the first provider at stageAsking.
func TestProviderScreen_ReviewReconfigureLoops(t *testing.T) {
	m := newProviderModel()
	m.stage = stageReviewing
	m.warn = "stale"
	m.idx = 4
	next, cmd := m.Update(tea.KeyMsg{Runes: []rune{'r'}, Type: tea.KeyRunes})
	if cmd != nil {
		t.Error("[r] should loop in place, not advance")
	}
	mm := next.(providerModel)
	if mm.stage != stageAsking || mm.idx != 0 || mm.warn != "" {
		t.Errorf("[r] should reset to asking/idx0/clear-warn; got stage=%v idx=%d warn=%q", mm.stage, mm.idx, mm.warn)
	}
}

// TestProviderScreen_ReviewEnterNothingConfiguredLoops covers the
// stageReviewing enter arm when nothing was configured: it warns and
// loops back rather than advancing.
func TestProviderScreen_ReviewEnterNothingConfiguredLoops(t *testing.T) {
	m := newProviderModel()
	m.stage = stageReviewing // nothing enabled
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("enter with nothing configured should not advance")
	}
	mm := next.(providerModel)
	if mm.warn == "" {
		t.Error("expected a warn telling the user to configure at least one provider")
	}
	if mm.stage != stageAsking || mm.idx != 0 {
		t.Errorf("should loop back to asking/idx0; got stage=%v idx=%d", mm.stage, mm.idx)
	}
}

// TestProviderScreen_ReviewEnterAdvancesWhenConfigured covers the happy
// stageReviewing enter arm.
func TestProviderScreen_ReviewEnterAdvancesWhenConfigured(t *testing.T) {
	m := newProviderModel()
	m.enabled["anthropic"] = true
	m.keys["anthropic"] = "sk-x"
	m.stage = stageReviewing
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter with a configured provider should advance")
	}
}

// TestProviderScreen_ViewEnteringStage covers the stageEntering View arm.
func TestProviderScreen_ViewEnteringStage(t *testing.T) {
	m := newProviderModel()
	next, _ := m.Update(tea.KeyMsg{Runes: []rune{'y'}, Type: tea.KeyRunes})
	m = next.(providerModel)
	out := stripStyle(m.View())
	for _, want := range []string{"API key", "[enter] save", "[esc] skip"} {
		if !strings.Contains(out, want) {
			t.Errorf("entering-stage view missing %q; got:\n%s", want, out)
		}
	}
}

// TestProviderScreen_ViewReviewConfigured covers the stageReviewing View
// arm with at least one provider configured.
func TestProviderScreen_ViewReviewConfigured(t *testing.T) {
	m := newProviderModel()
	m.enabled["anthropic"] = true
	m.stage = stageReviewing
	out := stripStyle(m.View())
	if !strings.Contains(out, "[enter] continue") || !strings.Contains(out, "[r] reconfigure") {
		t.Errorf("review view (configured) missing continue/reconfigure; got:\n%s", out)
	}
}

// TestProviderScreen_ViewReviewEmpty covers the stageReviewing View arm
// with nothing configured (the loop-back warning).
func TestProviderScreen_ViewReviewEmpty(t *testing.T) {
	m := newProviderModel()
	m.stage = stageReviewing
	out := stripStyle(m.View())
	if !strings.Contains(out, "No providers configured") {
		t.Errorf("review view (empty) should warn about no providers; got:\n%s", out)
	}
}

// TestProviderScreen_ViewMarkers exercises the per-row marker branches:
// set-later (~) and configured (x) and skipped (-) all render.
func TestProviderScreen_ViewMarkers(t *testing.T) {
	m := newProviderModel()
	m.enabled["anthropic"] = true // [x]
	m.setLater["openai"] = true   // [~]
	m.enabled["gemini"] = false   // [-] (skipped, past cursor)
	m.idx = 4                     // cursor on the last entry
	m.stage = stageAsking
	out := stripStyle(m.View())
	for _, want := range []string{"[x]", "[~]", "(set later)"} {
		if !strings.Contains(out, want) {
			t.Errorf("provider summary missing marker %q; got:\n%s", want, out)
		}
	}
}

// TestCuratedModelSlugs covers the slug-extraction helper and its
// unknown-provider nil arm.
func TestCuratedModelSlugs(t *testing.T) {
	got := CuratedModelSlugs("anthropic")
	want := providerModels("anthropic")
	if len(got) != len(want) {
		t.Errorf("slug count mismatch: got %d want %d", len(got), len(want))
	}
	if got[0] != want[0].Slug {
		t.Errorf("first slug: got %q want %q", got[0], want[0].Slug)
	}
	if CuratedModelSlugs("nope") != nil {
		t.Error("unknown provider should yield nil")
	}
}
