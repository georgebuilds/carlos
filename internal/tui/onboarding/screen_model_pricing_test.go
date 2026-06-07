package onboarding

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
)

// TestModelDropdown_RendersPricingColumns proves the dropdown row for
// Anthropic Sonnet 4.6 includes both per-million prices ($3 / $15) and
// the 200K context window. We strip lipgloss styling to compare bare
// text so the test stays stable across palette changes.
func TestModelDropdown_RendersPricingColumns(t *testing.T) {
	m := newModelModel()
	m.syncFromConfig(&config.Config{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "x"},
		},
	})
	out := stripStyle(m.View())
	for _, want := range []string{"$3", "$15", "200K"} {
		if !strings.Contains(out, want) {
			t.Errorf("dropdown should render %q somewhere; got:\n%s", want, out)
		}
	}
}

// TestModelDropdown_OllamaRendersFreePrice proves local Ollama rows
// render the "$0 / $0" placeholder so the column stays aligned.
func TestModelDropdown_OllamaRendersFreePrice(t *testing.T) {
	m := newModelModel()
	m.syncFromConfig(&config.Config{
		Providers: map[string]config.ProviderConfig{
			"ollama": {BaseURL: "http://localhost:11434"},
		},
	})
	out := stripStyle(m.View())
	if !strings.Contains(out, "$0 / $0") {
		t.Errorf("ollama row should render $0 / $0; got:\n%s", out)
	}
}

// TestFormatPrice_Variants pins the dollar formatter so an integer
// price doesn't grow trailing zeros and a sub-dollar price keeps two
// decimals.
func TestFormatPrice_Variants(t *testing.T) {
	cases := map[float64]string{
		0:     "$0",
		3:     "$3",
		15:    "$15",
		0.10:  "$0.10",
		0.05:  "$0.05",
		1.25:  "$1.25",
	}
	for in, want := range cases {
		if got := formatPrice(in); got != want {
			t.Errorf("formatPrice(%v) = %q, want %q", in, got, want)
		}
	}
}

// TestFormatCtxColumn_Variants pins the context-window formatter.
func TestFormatCtxColumn_Variants(t *testing.T) {
	cases := map[int]string{
		0:         "",
		200_000:   "200K",
		1_000_000: "1M",
		1_500_000: "1.5M",
		128_000:   "128K",
	}
	for in, want := range cases {
		if got := formatCtxColumn(in); got != want {
			t.Errorf("formatCtxColumn(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestModelSuggestion_PricingPopulated guards the curated lists so a
// future edit that drops the pricing fields is caught here, not in a
// silent dropdown regression.
func TestModelSuggestion_PricingPopulated(t *testing.T) {
	for _, p := range []string{"anthropic", "openai", "gemini", "openrouter"} {
		for _, s := range providerModels(p) {
			if s.PromptUSDPerM <= 0 {
				t.Errorf("%s: %q has zero prompt price", p, s.Slug)
			}
			if s.CtxLen <= 0 {
				t.Errorf("%s: %q has zero CtxLen", p, s.Slug)
			}
		}
	}
}
