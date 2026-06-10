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
		"gemini":     "gemini-3.5-flash",
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

func TestProviderModels_AllPopulated(t *testing.T) {
	for _, p := range []string{"anthropic", "openai", "gemini", "openrouter", "ollama"} {
		got := providerModels(p)
		if len(got) == 0 {
			t.Errorf("providerModels(%q) returned empty", p)
		}
		for i, m := range got {
			if m.Slug == "" {
				t.Errorf("%s[%d] missing slug", p, i)
			}
			if m.Label == "" {
				t.Errorf("%s[%d] missing label", p, i)
			}
		}
	}
	if got := providerModels("nope"); got != nil {
		t.Errorf("unknown provider: want nil got %v", got)
	}
}

// TestProviderModels_OpenRouterIncludesClaudeFable pins the curated
// openrouter list contains anthropic/claude-fable-5 so it surfaces
// in both the onboarding picker and (via CuratedModelSlugs) the
// /model slash autocomplete on a fresh install with no cached
// catalog yet.
func TestProviderModels_OpenRouterIncludesClaudeFable(t *testing.T) {
	const slug = "anthropic/claude-fable-5"
	for _, m := range providerModels("openrouter") {
		if m.Slug == slug {
			return
		}
	}
	t.Errorf("%q missing from curated openrouter list", slug)

	// Belt-and-braces: the autocomplete view (CuratedModelSlugs)
	// must also surface it, since /model openrouter:<tab> reads
	// through there.
	for _, s := range CuratedModelSlugs("openrouter") {
		if s == slug {
			return
		}
	}
	t.Errorf("%q missing from CuratedModelSlugs(\"openrouter\")", slug)
}

func TestFilterModels_Substring(t *testing.T) {
	got := filterModels("openrouter", "qwen")
	if len(got) == 0 {
		t.Fatal("expected qwen matches")
	}
	for _, m := range got {
		if !strings.Contains(strings.ToLower(m.Slug), "qwen") {
			t.Errorf("filter leaked non-match: %q", m.Slug)
		}
	}

	// Empty query returns everything.
	all := filterModels("openrouter", "")
	if len(all) != len(providerModels("openrouter")) {
		t.Errorf("empty query: want all (%d), got %d", len(providerModels("openrouter")), len(all))
	}

	// Whitespace-only treated as empty.
	if got := filterModels("openrouter", "   "); len(got) != len(all) {
		t.Errorf("whitespace-only query should be empty: got %d", len(got))
	}

	// Case-insensitive.
	upper := filterModels("openrouter", "DEEPSEEK")
	lower := filterModels("openrouter", "deepseek")
	if len(upper) != len(lower) || len(upper) == 0 {
		t.Errorf("case-insensitive filter mismatch: upper=%d lower=%d", len(upper), len(lower))
	}
}

func TestModelScreen_DropdownNavigation(t *testing.T) {
	m := newModelModel()
	m.syncFromConfig(&config.Config{
		Providers: map[string]config.ProviderConfig{
			"openrouter": {APIKey: "x"},
		},
	})
	// Initial: cursor -1, no selection.
	if m.cursor != -1 {
		t.Errorf("initial cursor: want -1 got %d", m.cursor)
	}
	// Down → cursor 0.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(modelModel)
	if m.cursor != 0 {
		t.Errorf("after down: want 0 got %d", m.cursor)
	}
	// Another down → cursor 1.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(modelModel)
	if m.cursor != 1 {
		t.Errorf("after second down: want 1 got %d", m.cursor)
	}
	// Up → back to 0.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(modelModel)
	if m.cursor != 0 {
		t.Errorf("after up: want 0 got %d", m.cursor)
	}
	// Up from 0 → -1 (wrap to raw text mode).
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(modelModel)
	if m.cursor != -1 {
		t.Errorf("up from 0: want wrap to -1, got %d", m.cursor)
	}
}

func TestModelScreen_EnterCommitsHighlighted(t *testing.T) {
	m := newModelModel()
	m.syncFromConfig(&config.Config{
		Providers: map[string]config.ProviderConfig{
			"openrouter": {APIKey: "x"},
		},
	})
	// Highlight the second suggestion.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(modelModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(modelModel)
	// Enter commits whatever's highlighted.
	want := providerModels("openrouter")[1].Slug
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected commit cmd")
	}
	mm := next.(modelModel)
	if got := mm.chosen["openrouter"]; got != want {
		t.Errorf("committed slug: want %q got %q", want, got)
	}
}

func TestModelScreen_TabCompletesHighlighted(t *testing.T) {
	m := newModelModel()
	m.syncFromConfig(&config.Config{
		Providers: map[string]config.ProviderConfig{
			"openrouter": {APIKey: "x"},
		},
	})
	// Highlight position 2.
	for range 3 {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(modelModel)
	}
	want := providerModels("openrouter")[2].Slug
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(modelModel)
	if got := m.input.Value(); got != want {
		t.Errorf("after tab: input wanted %q got %q", want, got)
	}
	if m.cursor != -1 {
		t.Errorf("tab should reset cursor; got %d", m.cursor)
	}
}

func TestModelScreen_TypingResetsCursor(t *testing.T) {
	m := newModelModel()
	m.syncFromConfig(&config.Config{
		Providers: map[string]config.ProviderConfig{
			"openrouter": {APIKey: "x"},
		},
	})
	// Move cursor down.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(modelModel)
	if m.cursor != 0 {
		t.Fatalf("setup: cursor should be 0, got %d", m.cursor)
	}
	// Type a character.
	next, _ = m.Update(tea.KeyMsg{Runes: []rune{'q'}, Type: tea.KeyRunes})
	m = next.(modelModel)
	if m.cursor != -1 {
		t.Errorf("typing should reset cursor to -1; got %d", m.cursor)
	}
}

func TestModelScreen_FilterShowsOnlyMatching(t *testing.T) {
	m := newModelModel()
	m.syncFromConfig(&config.Config{
		Providers: map[string]config.ProviderConfig{
			"openrouter": {APIKey: "x"},
		},
	})
	// Replace input with "qwen".
	m.input.SetValue("qwen")
	for _, s := range m.suggestions() {
		if !strings.Contains(strings.ToLower(s.Slug), "qwen") {
			t.Errorf("non-matching suggestion in filtered list: %q", s.Slug)
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
