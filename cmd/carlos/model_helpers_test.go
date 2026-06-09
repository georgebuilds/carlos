package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/skills"
)

// TestDedupStrings covers the order-preserving dedup that powers the
// /model autocomplete list (configured default + cached catalog ids
// can overlap; the user shouldn't see the same model twice).
func TestDedupStrings(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, nil},
		{"no dupes", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"dupes preserve first", []string{"a", "b", "a", "c", "b"}, []string{"a", "b", "c"}},
		{"single", []string{"x"}, []string{"x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dedupStrings(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("dedupStrings(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSortedProviderNamesForCompletion(t *testing.T) {
	in := map[string]config.ProviderConfig{
		"openrouter": {DefaultModel: "google/gemini-3.5-flash"},
		"anthropic":  {DefaultModel: "claude-opus-4-7"},
		"openai":     {DefaultModel: "gpt-5"},
	}
	got := sortedProviderNamesForCompletion(in)
	want := []string{"anthropic", "openai", "openrouter"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// TestModelCompletionsFor_NilConfig guards the defensive early
// return — the runtime closure can hand nil into this helper when
// config didn't load (transient onboarding race).
func TestModelCompletionsFor_NilConfig(t *testing.T) {
	if got := modelCompletionsFor(nil, ""); got != nil {
		t.Errorf("nil cfg should return nil; got %v", got)
	}
}

// TestModelCompletionsFor_NoColonReturnsProviders verifies the first
// regime: empty partial → every configured provider name + ":".
func TestModelCompletionsFor_NoColonReturnsProviders(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"anthropic":  {DefaultModel: "claude-opus-4-7"},
			"openai":     {DefaultModel: "gpt-5"},
			"openrouter": {DefaultModel: "google/gemini-3.5-flash"},
		},
	}
	got := modelCompletionsFor(cfg, "")
	want := []string{"anthropic:", "openai:", "openrouter:"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("empty partial: got %v want %v", got, want)
	}
}

// TestModelCompletionsFor_PrefixFiltersProviders narrows the provider
// list by what's typed (e.g. "op" matches openai and openrouter).
func TestModelCompletionsFor_PrefixFiltersProviders(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"anthropic":  {DefaultModel: "claude-opus-4-7"},
			"openai":     {DefaultModel: "gpt-5"},
			"openrouter": {DefaultModel: "google/gemini-3.5-flash"},
		},
	}
	got := modelCompletionsFor(cfg, "op")
	want := []string{"openai:", "openrouter:"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// TestModelCompletionsFor_AfterColonReturnsModels exercises the
// second regime: "<provider>:" or "<provider>:<frag>" → known model
// ids for that provider.
func TestModelCompletionsFor_AfterColonReturnsModels(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {DefaultModel: "claude-opus-4-7"},
		},
	}
	got := modelCompletionsFor(cfg, "anthropic:")
	want := []string{"anthropic:claude-opus-4-7"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// TestModelCompletionsFor_OpenRouterCatalog seeds a temp cached catalog
// file under HOME/.carlos and asserts the entries surface in the
// suggestion list.
func TestModelCompletionsFor_OpenRouterCatalog(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	carlosDir := filepath.Join(tmp, ".carlos")
	if err := os.MkdirAll(carlosDir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		"models": []map[string]string{
			{"id": "google/gemini-3.5-flash"},
			{"id": "anthropic/claude-opus-4-7"},
			{"id": ""}, // skipped
		},
	}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(carlosDir, "openrouter-models.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openrouter": {DefaultModel: "google/gemini-3.5-flash"},
		},
	}
	got := modelCompletionsFor(cfg, "openrouter:")
	// Default + catalog id should be deduped and present (order isn't
	// load-bearing — we only assert membership).
	wantSet := map[string]bool{
		"openrouter:google/gemini-3.5-flash":   true,
		"openrouter:anthropic/claude-opus-4-7": true,
	}
	gotSet := map[string]bool{}
	for _, g := range got {
		gotSet[g] = true
	}
	for k := range wantSet {
		if !gotSet[k] {
			t.Errorf("missing %q in %v", k, got)
		}
	}
}

// TestModelCompletionsFor_FragmentFiltersModels checks the partial-
// match filter applied AFTER the colon: typing "openrouter:gemini"
// should narrow to entries containing "gemini".
func TestModelCompletionsFor_FragmentFiltersModels(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	carlosDir := filepath.Join(tmp, ".carlos")
	_ = os.MkdirAll(carlosDir, 0o700)
	doc := map[string]any{
		"models": []map[string]string{
			{"id": "google/gemini-3.5-flash"},
			{"id": "anthropic/claude-opus-4-7"},
		},
	}
	raw, _ := json.Marshal(doc)
	_ = os.WriteFile(filepath.Join(carlosDir, "openrouter-models.json"), raw, 0o600)
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openrouter": {DefaultModel: "google/gemini-3.5-flash"},
		},
	}
	got := modelCompletionsFor(cfg, "openrouter:gemini")
	for _, g := range got {
		if !strings.Contains(strings.ToLower(g), "gemini") {
			t.Errorf("got entry %q that doesn't match the fragment", g)
		}
	}
	if len(got) == 0 {
		t.Errorf("expected at least one gemini match, got none")
	}
}

// TestModelCompletionsFor_UnknownProvider returns nil — no defaults
// to surface, no catalog wired.
func TestModelCompletionsFor_UnknownProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{"openai": {DefaultModel: "gpt-5"}},
	}
	if got := modelCompletionsFor(cfg, "ollama:"); got != nil {
		t.Errorf("unknown provider should return nil; got %v", got)
	}
}

// TestKnownModelsFor_NoDefault returns nil for a provider with no
// configured default and no cached catalog.
func TestKnownModelsFor_NoDefault(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{"anthropic": {}},
	}
	if got := knownModelsFor(cfg, "anthropic"); got != nil {
		t.Errorf("no default + no catalog should yield nil; got %v", got)
	}
}

// TestLoadOpenRouterCatalog_NoHome returns nil silently.
func TestLoadOpenRouterCatalog_MissingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := loadOpenRouterCatalog(); got != nil {
		t.Errorf("missing catalog file should return nil; got %v", got)
	}
}

// TestLoadOpenRouterCatalog_BadJSON returns nil rather than panicking.
func TestLoadOpenRouterCatalog_BadJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	carlosDir := filepath.Join(tmp, ".carlos")
	_ = os.MkdirAll(carlosDir, 0o700)
	_ = os.WriteFile(filepath.Join(carlosDir, "openrouter-models.json"),
		[]byte("not-json"), 0o600)
	if got := loadOpenRouterCatalog(); got != nil {
		t.Errorf("bad json should yield nil; got %v", got)
	}
}

// TestSummariseSkills_NilLibrary returns nil so callers can pass
// through to FrameInfo without nil-checking.
func TestSummariseSkills_NilLibrary(t *testing.T) {
	if got := summariseSkills(nil, "personal"); got != nil {
		t.Errorf("nil lib should return nil; got %v", got)
	}
}

// TestSummariseSkills_FilterByFrame proves the projection honours
// the skill's Frames:[] restriction: a skill scoped to "work" must
// NOT appear in the personal frame's prompt.
func TestSummariseSkills_FilterByFrame(t *testing.T) {
	lib := &skills.Library{
		Active: []*skills.Skill{
			{Name: "calendar-caldav", Description: "talk to a caldav server"},
			{Name: "work-only", Description: "ticketing", Frames: []string{"work"}},
		},
	}
	personal := summariseSkills(lib, "personal")
	if len(personal) != 1 {
		t.Fatalf("personal should see only the unrestricted skill; got %d: %+v", len(personal), personal)
	}
	if personal[0].Name != "calendar-caldav" {
		t.Errorf("got %q want calendar-caldav", personal[0].Name)
	}
	work := summariseSkills(lib, "work")
	if len(work) != 2 {
		t.Errorf("work should see both skills; got %d", len(work))
	}
}

// TestSummariseSkills_EmptyLibrary returns nil.
func TestSummariseSkills_EmptyLibrary(t *testing.T) {
	lib := &skills.Library{}
	if got := summariseSkills(lib, "personal"); got != nil {
		t.Errorf("empty library should yield nil; got %v", got)
	}
}

// TestExtractCapabilityBackends_PinsContract is a sanity test that
// the helper we touched alongside the new skills wiring still
// behaves: a frame with no capabilities returns nil; one with a
// well-formed `backend` key surfaces it.
func TestExtractCapabilityBackends_PinsContract(t *testing.T) {
	if got := extractCapabilityBackends(frame.Frame{}); got != nil {
		t.Errorf("zero frame should return nil; got %v", got)
	}
	in := frame.Frame{
		Capabilities: map[string]map[string]any{
			"calendar": {"backend": "caldav"},
			"empty":    nil,
		},
	}
	got := extractCapabilityBackends(in)
	if got["calendar"] != "caldav" {
		t.Errorf("want calendar=caldav; got %v", got)
	}
	if _, ok := got["empty"]; ok {
		t.Errorf("empty capability should drop; got %v", got)
	}
}

// Force-link agent for the SkillSummary projection — without this
// import the test file would compile clean without exercising the
// summariseSkills contract.
var _ = agent.SkillSummary{}
