package onboarding

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/providers/openrouter"
)

// TestOpenrouterFuture_WaitNil verifies a nil future reports not-ready.
func TestOpenrouterFuture_WaitNil(t *testing.T) {
	var f *openrouterFuture
	if _, ok := f.wait(time.Millisecond); ok {
		t.Error("nil future should report ok=false")
	}
}

// TestOpenrouterFuture_WaitSuccess covers the resolved-success path: the
// fetch completed before the deadline with a non-nil model slice.
func TestOpenrouterFuture_WaitSuccess(t *testing.T) {
	f := &openrouterFuture{done: make(chan struct{})}
	f.models = []openrouter.ModelInfo{{ID: "x/y", Name: "XY"}}
	close(f.done)
	got, ok := f.wait(time.Second)
	if !ok {
		t.Fatal("resolved future should report ok=true")
	}
	if len(got) != 1 || got[0].ID != "x/y" {
		t.Errorf("unexpected models: %+v", got)
	}
}

// TestOpenrouterFuture_WaitErr covers the resolved-but-errored path: a
// completed fetch with a non-nil error falls back (ok=false).
func TestOpenrouterFuture_WaitErr(t *testing.T) {
	f := &openrouterFuture{done: make(chan struct{})}
	f.err = errFetch
	close(f.done)
	if _, ok := f.wait(time.Second); ok {
		t.Error("errored future should report ok=false")
	}
}

var errFetch = stubErr("fetch failed")

type stubErr string

func (e stubErr) Error() string { return string(e) }

// TestOpenrouterFuture_WaitTimeout covers the deadline path: the fetch is
// still in flight when the budget expires.
func TestOpenrouterFuture_WaitTimeout(t *testing.T) {
	f := &openrouterFuture{done: make(chan struct{})} // never closed
	if _, ok := f.wait(5 * time.Millisecond); ok {
		t.Error("in-flight future past deadline should report ok=false")
	}
}

// TestLiveOpenRouterSuggestions_Adapts pins the ModelInfo → ModelSuggestion
// field mapping.
func TestLiveOpenRouterSuggestions_Adapts(t *testing.T) {
	in := []openrouter.ModelInfo{
		{ID: "a/b", Name: "A B", PromptUSDPerM: 1.5, CompletionUSDPerM: 3.0, CtxLen: 128000},
		{ID: "c/d", Name: "C D", PromptUSDPerM: 0, CompletionUSDPerM: 0, CtxLen: 0},
	}
	out := liveOpenRouterSuggestions(in)
	if len(out) != 2 {
		t.Fatalf("want 2 suggestions, got %d", len(out))
	}
	if out[0].Slug != "a/b" || out[0].Label != "A B" ||
		out[0].PromptUSDPerM != 1.5 || out[0].CompletionUSDPerM != 3.0 || out[0].CtxLen != 128000 {
		t.Errorf("first suggestion mismapped: %+v", out[0])
	}
}

// TestLiveOpenRouterSuggestions_Empty covers the zero-length input.
func TestLiveOpenRouterSuggestions_Empty(t *testing.T) {
	if got := liveOpenRouterSuggestions(nil); len(got) != 0 {
		t.Errorf("nil input should produce empty slice; got %v", got)
	}
}

// TestProviderSuggestions_LiveOverlay verifies the live OpenRouter path:
// when the future resolves with models inside the budget, those win over
// the curated list.
func TestProviderSuggestions_LiveOverlay(t *testing.T) {
	f := &openrouterFuture{done: make(chan struct{})}
	f.models = []openrouter.ModelInfo{{ID: "live/only", Name: "Live Only", CtxLen: 99}}
	close(f.done)
	m := newModelModel()
	m.orFuture = f
	got := m.providerSuggestions("openrouter")
	if len(got) != 1 || got[0].Slug != "live/only" {
		t.Errorf("live overlay should win; got %+v", got)
	}
}

// TestProviderSuggestions_FallbackOnNonOpenRouter ensures non-openrouter
// providers always read the curated list, future or not.
func TestProviderSuggestions_FallbackOnNonOpenRouter(t *testing.T) {
	f := &openrouterFuture{done: make(chan struct{})}
	f.models = []openrouter.ModelInfo{{ID: "live/only"}}
	close(f.done)
	m := newModelModel()
	m.orFuture = f
	got := m.providerSuggestions("anthropic")
	if len(got) == 0 || got[0].Slug == "live/only" {
		t.Errorf("anthropic should use curated list, not live openrouter overlay; got %+v", got)
	}
}

// TestProviderSuggestions_TimeoutFallsBackToCurated verifies the curated
// list is returned when the future times out.
func TestProviderSuggestions_TimeoutFallsBackToCurated(t *testing.T) {
	f := &openrouterFuture{done: make(chan struct{})} // never resolves
	m := newModelModel()
	m.orFuture = f
	// providerSuggestions blocks up to orWait (800ms) here; acceptable
	// for a single test. It must return the curated openrouter list.
	got := m.providerSuggestions("openrouter")
	curated := providerModels("openrouter")
	if len(got) != len(curated) {
		t.Errorf("timeout should fall back to curated (%d); got %d", len(curated), len(got))
	}
}

// TestEqualStrSlices covers the helper's length-mismatch and
// element-mismatch arms (the all-equal arm is hit by syncFromConfig).
func TestEqualStrSlices(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"equal", []string{"a", "b"}, []string{"a", "b"}, true},
		{"len mismatch", []string{"a"}, []string{"a", "b"}, false},
		{"elem mismatch", []string{"a", "b"}, []string{"a", "c"}, false},
		{"both empty", nil, nil, true},
	}
	for _, c := range cases {
		if got := equalStrSlices(c.a, c.b); got != c.want {
			t.Errorf("%s: equalStrSlices = %v want %v", c.name, got, c.want)
		}
	}
}

// TestCurrentProvider_OutOfRange covers the idx<0 / idx>=len guards.
func TestCurrentProvider_OutOfRange(t *testing.T) {
	m := newModelModel()
	m.providers = []string{"anthropic"}
	m.idx = 5
	if got := m.currentProvider(); got != "" {
		t.Errorf("idx past end should yield empty; got %q", got)
	}
	m.idx = -1
	if got := m.currentProvider(); got != "" {
		t.Errorf("negative idx should yield empty; got %q", got)
	}
	m.idx = 0
	if got := m.currentProvider(); got != "anthropic" {
		t.Errorf("idx 0: got %q want anthropic", got)
	}
}

// TestSuggestions_NoProviderNil covers suggestions() when no provider is
// active.
func TestSuggestions_NoProviderNil(t *testing.T) {
	m := newModelModel() // no providers synced
	if got := m.suggestions(); got != nil {
		t.Errorf("no provider should yield nil suggestions; got %v", got)
	}
}

// TestSuggestions_ExactMatchUnfiltered verifies that when the input
// exactly equals a curated slug, the full (capped) list is shown so the
// user can keep browsing rather than seeing only the one match.
func TestSuggestions_ExactMatchUnfiltered(t *testing.T) {
	m := newModelModel()
	m.syncFromConfig(&config.Config{
		Providers: map[string]config.ProviderConfig{"openrouter": {APIKey: "x"}},
	})
	curated := providerModels("openrouter")
	m.input.SetValue(curated[0].Slug) // exact match on the default
	got := m.suggestions()
	// Exact-match branch returns the list capped at maxSuggestions, not a
	// single filtered row.
	if len(got) <= 1 {
		t.Errorf("exact match should keep the browse list, not collapse to 1; got %d", len(got))
	}
	if len(got) > maxSuggestions {
		t.Errorf("suggestions should be capped at %d; got %d", maxSuggestions, len(got))
	}
}

// TestPadRight covers the equal/pad/truncate/ellipsis-collapse arms.
func TestPadRight(t *testing.T) {
	cases := []struct {
		name string
		in   string
		w    int
		want string
	}{
		{"exact width", "abc", 3, "abc"},
		{"pad", "ab", 4, "ab  "},
		{"truncate with ellipsis", "abcdef", 4, "abc…"},
		{"width one collapses to ellipsis", "abcdef", 1, "…"},
		{"zero width collapses", "abc", 0, "…"},
	}
	for _, c := range cases {
		if got := padRight(c.in, c.w); got != c.want {
			t.Errorf("%s: padRight(%q,%d) = %q want %q", c.name, c.in, c.w, got, c.want)
		}
	}
}

// TestFormatCtxColumn covers all magnitude arms including the fractional
// millions and sub-1K branches.
func TestFormatCtxColumn(t *testing.T) {
	cases := map[int]string{
		0:         "",
		-5:        "",
		512:       "512",
		200_000:   "200K",
		1_000_000: "1M",
		1_500_000: "1.5M",
		2_000_000: "2M",
	}
	for n, want := range cases {
		if got := formatCtxColumn(n); got != want {
			t.Errorf("formatCtxColumn(%d) = %q want %q", n, got, want)
		}
	}
}

// TestModelView_NoProvidersHint covers the empty-providers View arm.
func TestModelView_NoProvidersHint(t *testing.T) {
	m := newModelModel()
	out := stripStyle(m.View())
	if !strings.Contains(out, "no providers configured") {
		t.Errorf("empty providers View should hint to go back; got %q", out)
	}
}

// TestModelView_CustomSlugHint covers the "no curated match" View branch
// where the user typed a slug not in the list.
func TestModelView_CustomSlugHint(t *testing.T) {
	m := newModelModel()
	m.syncFromConfig(&config.Config{
		Providers: map[string]config.ProviderConfig{"anthropic": {APIKey: "x"}},
	})
	m.input.SetValue("totally-custom-slug-xyz")
	out := stripStyle(m.View())
	if !strings.Contains(out, "no match in the curated list") {
		t.Errorf("custom slug should show verbatim-accept hint; got:\n%s", out)
	}
}

// TestModelUpdate_NoProvidersAdvances covers the defensive early-advance
// when Update runs with zero providers.
func TestModelUpdate_NoProvidersAdvances(t *testing.T) {
	m := newModelModel() // no providers
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Error("Update with no providers should emit a nextScreen cmd")
	}
}

// TestModelUpdate_EnterEmptyUsesDefault covers the branch where the input
// is empty on enter and the suggested default is substituted.
func TestModelUpdate_EnterEmptyUsesDefault(t *testing.T) {
	m := newModelModel()
	m.syncFromConfig(&config.Config{
		Providers: map[string]config.ProviderConfig{"anthropic": {APIKey: "x"}},
	})
	m.input.SetValue("")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(modelModel)
	if mm.chosen["anthropic"] != suggestedDefaultModel("anthropic") {
		t.Errorf("empty enter should commit the suggested default; got %q", mm.chosen["anthropic"])
	}
}

// TestPrimeInput_UsesConfiguredDefaultModel covers the branch where a
// provider already has a DefaultModel in config (revisit case).
func TestPrimeInput_UsesConfiguredDefaultModel(t *testing.T) {
	m := newModelModel()
	m.syncFromConfig(&config.Config{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "x", DefaultModel: "claude-opus-4-7"},
		},
	})
	if got := m.input.Value(); got != "claude-opus-4-7" {
		t.Errorf("primeInput should prefer the configured DefaultModel; got %q", got)
	}
}

// TestSyncFromConfig_NoChangeIsNoop verifies a second sync with the same
// providers doesn't reset the in-progress input.
func TestSyncFromConfig_NoChangeIsNoop(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{"anthropic": {APIKey: "x"}},
	}
	m := newModelModel()
	m.syncFromConfig(cfg)
	m.input.SetValue("user-typed-value")
	m.syncFromConfig(cfg) // identical providers → should not reset
	if got := m.input.Value(); got != "user-typed-value" {
		t.Errorf("no-change sync should preserve input; got %q", got)
	}
}

// TestOpenrouterCacheDir_NonEmpty confirms the cache dir resolves to a
// non-empty path ending in the expected suffix.
func TestOpenrouterCacheDir_NonEmpty(t *testing.T) {
	got := openrouterCacheDir()
	if got == "" {
		t.Fatal("cache dir should never be empty")
	}
	if !strings.Contains(got, "cache") {
		t.Errorf("cache dir should contain 'cache'; got %q", got)
	}
}
