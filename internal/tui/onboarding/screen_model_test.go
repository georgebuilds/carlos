package onboarding

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/config"
)

// TestModelScreen_ViewSurvivesAdvanceFrame reproduces the panic from
// real-world onboarding: with a single configured provider, pressing
// enter on the model screen increments idx past len(providers), Update
// returns a nextScreen cmd, and bubbletea renders one more frame
// before the cmd propagates. View() must not crash on that frame.
func TestModelScreen_ViewSurvivesAdvanceFrame(t *testing.T) {
	m := newModelModel()
	m.syncFromConfig(&config.Config{
		Providers: map[string]config.ProviderConfig{
			"openrouter": {APIKey: "x"},
		},
	})

	// Simulate the user pressing enter on the last (and only) provider.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(modelModel)
	if cmd == nil {
		t.Fatal("enter on last provider should have returned a nextScreen cmd")
	}
	if mm.idx < len(mm.providers) {
		t.Fatalf("idx should have advanced past providers; got idx=%d len=%d", mm.idx, len(mm.providers))
	}

	// Bubbletea's next render frame: View() runs with idx == len.
	// This used to panic with "index out of range [1] with length 1".
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View() panicked after advance: %v", r)
		}
	}()
	out := mm.View()
	if !strings.Contains(out, "advancing") && !strings.Contains(out, "no providers") {
		t.Errorf("expected transitional copy, got %q", out)
	}
}

// TestSuggestedDefaultModel pins the per-provider defaults so a future
// "let's bump the suggestion" change is a deliberate code edit + a
// test update rather than a silent drift the user only notices at
// onboarding.
func TestSuggestedDefaultModel(t *testing.T) {
	cases := map[string]string{
		"anthropic":  "claude-sonnet-4-6",
		"openai":     "gpt-5",
		"openrouter": "google/gemini-3.5-flash",
		"ollama":     "llama3.1:8b",
		"unknown":    "",
	}
	for provider, want := range cases {
		if got := suggestedDefaultModel(provider); got != want {
			t.Errorf("suggestedDefaultModel(%q) = %q, want %q", provider, got, want)
		}
	}
}

// TestModelScreen_HappyPathThreeProviders just exercises the normal
// flow so the defensive guard doesn't mask regressions elsewhere.
func TestModelScreen_HappyPathThreeProviders(t *testing.T) {
	m := newModelModel()
	m.syncFromConfig(&config.Config{
		Providers: map[string]config.ProviderConfig{
			"anthropic":  {APIKey: "x"},
			"openai":     {APIKey: "y"},
			"openrouter": {APIKey: "z"},
		},
	})
	for i := 0; i < 3; i++ {
		view := m.View()
		if strings.Contains(view, "advancing") {
			t.Fatalf("iter %d: shouldn't have advanced yet:\n%s", i, view)
		}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(modelModel)
	}
	// After 3 enters the model is past the end; View should be safe.
	out := m.View()
	if !strings.Contains(out, "advancing") {
		t.Errorf("expected 'advancing' on final frame, got %q", out)
	}
}
