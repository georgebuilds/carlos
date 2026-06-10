package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
)

func newAboutTool() *CarlosAboutTool {
	return NewCarlosAboutTool(
		config.VaultConfig{Path: "/Volumes/nas/carlos-vault", Exclude: []string{"private/**"}},
		frame.Config{
			Default: "personal",
			Active:  "personal",
			List: []frame.Frame{
				{
					Name:         "personal",
					Glyph:        "◉",
					Accent:       "cream",
					Provider:     "anthropic",
					Model:        "claude-sonnet-4-6",
					Mode:         "solo",
					VaultSubtree: "personal",
					CwdHints:     []string{"~/Code/anneal"},
					Capabilities: map[string]map[string]any{
						"calendar": {"backend": "apple-calendar"},
					},
				},
				{
					Name:         "work",
					Glyph:        "▣",
					Accent:       "rust",
					Provider:     "anthropic",
					Mode:         "orchestrator",
					VaultSubtree: "work",
					CwdHints:     []string{"~/Code/ludus*"},
					Capabilities: map[string]map[string]any{
						"calendar": {"backend": "caldav"},
						"email":    {"backend": "fastmail-imap"},
					},
				},
			},
		},
		"personal",
		map[string]ProviderSummary{
			"anthropic": {HasKey: true, DefaultModel: "claude-sonnet-4-6"},
		},
		"George",
	)
}

func runAbout(t *testing.T, tool *CarlosAboutTool, section string) carlosAboutResponse {
	t.Helper()
	in, _ := json.Marshal(carlosAboutInput{Section: section})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("carlos_about: %v", err)
	}
	var resp carlosAboutResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCarlosAbout_FullEnvelope(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "")
	if resp.User != "George" {
		t.Errorf("user = %q, want George", resp.User)
	}
	if resp.Vault == nil || resp.Vault.Path == "" {
		t.Errorf("vault should be populated; got %+v", resp.Vault)
	}
	if resp.Active == nil || resp.Active.Name != "personal" {
		t.Errorf("active frame should be personal; got %+v", resp.Active)
	}
	if len(resp.Frames) != 2 {
		t.Errorf("frames len = %d, want 2", len(resp.Frames))
	}
	if resp.Capabilities["calendar"] != "apple-calendar" {
		t.Errorf("capabilities should reflect active frame; got %+v", resp.Capabilities)
	}
	if _, ok := resp.Providers["anthropic"]; !ok {
		t.Errorf("providers should include anthropic; got %+v", resp.Providers)
	}
}

func TestCarlosAbout_SectionUser(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "user")
	if resp.User != "George" {
		t.Errorf("user = %q, want George", resp.User)
	}
	if resp.Vault != nil || resp.Active != nil || len(resp.Frames) != 0 {
		t.Errorf("section=user should leave other fields zero; got %+v", resp)
	}
}

func TestCarlosAbout_SectionVault(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "vault")
	if resp.Vault == nil || resp.Vault.Path != "/Volumes/nas/carlos-vault" {
		t.Errorf("vault wrong: %+v", resp.Vault)
	}
	if len(resp.Vault.Exclude) != 1 || resp.Vault.Exclude[0] != "private/**" {
		t.Errorf("exclude not surfaced; got %+v", resp.Vault.Exclude)
	}
	if resp.User != "" {
		t.Errorf("section=vault should not surface user; got %q", resp.User)
	}
}

func TestCarlosAbout_SectionActive(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "active")
	if resp.Active == nil {
		t.Fatal("active should be populated")
	}
	if resp.Active.Name != "personal" {
		t.Errorf("active = %q, want personal", resp.Active.Name)
	}
	if resp.Active.Mode != "solo" {
		t.Errorf("mode = %q, want solo", resp.Active.Mode)
	}
	if resp.Active.VaultSubtree != "personal" {
		t.Errorf("vault_subtree = %q, want personal", resp.Active.VaultSubtree)
	}
}

func TestCarlosAbout_SectionFrames(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "frames")
	if len(resp.Frames) != 2 {
		t.Errorf("got %d frames, want 2", len(resp.Frames))
	}
	var work *frameSummary
	for i := range resp.Frames {
		if resp.Frames[i].Name == "work" {
			work = &resp.Frames[i]
		}
	}
	if work == nil {
		t.Fatal("work frame missing from list")
	}
	if work.Mode != "orchestrator" {
		t.Errorf("work mode = %q, want orchestrator", work.Mode)
	}
}

func TestCarlosAbout_SectionCapabilities(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "capabilities")
	if resp.Capabilities["calendar"] != "apple-calendar" {
		t.Errorf("active frame's capabilities should surface; got %+v", resp.Capabilities)
	}
	if resp.Active != nil {
		t.Errorf("section=capabilities should not include active")
	}
}

func TestCarlosAbout_SectionProviders(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "providers")
	p, ok := resp.Providers["anthropic"]
	if !ok {
		t.Fatal("providers should include anthropic")
	}
	if !p.HasKey {
		t.Error("HasKey should be true")
	}
}

func TestCarlosAbout_UnknownSectionErrors(t *testing.T) {
	tool := newAboutTool()
	b, _ := json.Marshal(carlosAboutInput{Section: "garbage"})
	if _, err := tool.Execute(context.Background(), b); err == nil {
		t.Error("unknown section should reject")
	} else if !strings.Contains(err.Error(), "unknown section") {
		t.Errorf("error should mention 'unknown section'; got %v", err)
	}
}

func TestCarlosAbout_NoInputReturnsFull(t *testing.T) {
	tool := newAboutTool()
	out, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var resp carlosAboutResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.User == "" || resp.Vault == nil || resp.Active == nil {
		t.Errorf("nil input should return full envelope; got %+v", resp)
	}
}

func TestCarlosAbout_ProviderSummariesNeverIncludesAPIKey(t *testing.T) {
	summaries := ProviderSummariesFromConfig(map[string]config.ProviderConfig{
		"anthropic": {APIKey: "sk-very-secret", DefaultModel: "claude-sonnet-4-6"},
	})
	raw, _ := json.Marshal(summaries)
	if strings.Contains(string(raw), "sk-very-secret") {
		t.Errorf("API key must NEVER appear in carlos_about output; got %s", raw)
	}
	if strings.Contains(string(raw), "api_key") {
		t.Errorf("JSON should not even mention api_key field; got %s", raw)
	}
	if !summaries["anthropic"].HasKey {
		t.Errorf("HasKey should be true when APIKey is set")
	}
}

// TestCarlosAbout_LiveDispatch_OverridesActive is the regression test
// for the mid-session /model swap bug: the chat header updated to the
// freshly-chosen model but the assistant kept self-reporting as the
// original session-start model because carlos_about read from the
// frame config (which swapModel never mutates). When the runtime
// wires a liveDispatch closure, the active section must report the
// runtime values instead of the stored frame config.
func TestCarlosAbout_LiveDispatch_OverridesActive(t *testing.T) {
	tool := newAboutTool()
	tool.SetLiveDispatch(func() (string, string) {
		return "openrouter", "anthropic/claude-fable-5"
	})

	resp := runAbout(t, tool, "active")
	if resp.Active == nil {
		t.Fatal("active should be populated")
	}
	if resp.Active.Provider != "openrouter" {
		t.Errorf("provider = %q, want %q (live override should win)", resp.Active.Provider, "openrouter")
	}
	if resp.Active.Model != "anthropic/claude-fable-5" {
		t.Errorf("model = %q, want %q (live override should win)", resp.Active.Model, "anthropic/claude-fable-5")
	}
}

// TestCarlosAbout_LiveDispatch_OverridesProvidersDefaultModel is the
// follow-on regression test for the v0.7.7 bug report where /model
// visibly swapped the dispatch and updated the header but the
// assistant kept self-reporting the original session-start model. The
// "active" section was already wired (see _OverridesActive above),
// but the response ALSO contained the stale value in
// providers.<active>.default_model, and the model was empirically
// observed preferring the providers-section value when answering
// "what model are you". With this fix the live override propagates to
// both sections so the envelope reads coherently.
func TestCarlosAbout_LiveDispatch_OverridesProvidersDefaultModel(t *testing.T) {
	tool := NewCarlosAboutTool(
		config.VaultConfig{Path: "/v"},
		frame.Config{
			Default: "personal",
			Active:  "personal",
			List: []frame.Frame{
				{Name: "personal", Provider: "openrouter", Model: "google/gemini-3.5-flash"},
			},
		},
		"personal",
		map[string]ProviderSummary{
			"openrouter": {HasKey: true, DefaultModel: "google/gemini-3.5-flash"},
		},
		"George",
	)
	tool.SetLiveDispatch(func() (string, string) {
		return "openrouter", "anthropic/claude-fable-5"
	})

	resp := runAbout(t, tool, "")
	if resp.Providers["openrouter"].DefaultModel != "anthropic/claude-fable-5" {
		t.Errorf("providers.openrouter.default_model = %q, want live override %q",
			resp.Providers["openrouter"].DefaultModel, "anthropic/claude-fable-5")
	}
	// Sanity: the rest of the envelope stays coherent.
	if resp.Active.Model != "anthropic/claude-fable-5" {
		t.Errorf("active.model also expected to be overridden: got %q", resp.Active.Model)
	}
}

// TestCarlosAbout_LiveDispatch_NoProviderMatch leaves the providers
// map untouched when the live provider name doesn't match any
// configured provider key. Defensive: a misconfigured runtime
// shouldn't introduce a phantom provider entry.
func TestCarlosAbout_LiveDispatch_NoProviderMatch(t *testing.T) {
	tool := newAboutTool()
	tool.SetLiveDispatch(func() (string, string) {
		return "made-up-provider", "made-up-model"
	})

	resp := runAbout(t, tool, "providers")
	if _, exists := resp.Providers["made-up-provider"]; exists {
		t.Errorf("unmatched live provider must not surface as a new providers entry: %+v", resp.Providers)
	}
	if got := resp.Providers["anthropic"].DefaultModel; got != "claude-sonnet-4-6" {
		t.Errorf("anthropic should keep its stored DefaultModel; got %q", got)
	}
}

// TestCarlosAbout_LiveDispatch_NonActiveFramesUntouched pins the
// non-active boundary: the runtime override applies to the active
// frame's entry in the list AND the active section, but a non-active
// frame's stored config stays intact. The override is
// session-specific to the active frame.
func TestCarlosAbout_LiveDispatch_NonActiveFramesUntouched(t *testing.T) {
	tool := newAboutTool() // active=personal; non-active=work
	tool.SetLiveDispatch(func() (string, string) {
		return "openrouter", "anthropic/claude-fable-5"
	})

	resp := runAbout(t, tool, "frames")
	for _, f := range resp.Frames {
		if f.Name != "work" {
			continue
		}
		if f.Provider != "anthropic" {
			t.Errorf("non-active frame work should keep stored provider; got %q", f.Provider)
		}
	}
}

// TestCarlosAbout_LiveDispatch_Nil_FallsBackToFrameConfig pins the
// "tool wired without runtime closure" path. Headless paths and the
// daemon don't run a mid-session swap so they intentionally don't
// wire SetLiveDispatch; the existing frame-config behavior must keep
// working unchanged.
func TestCarlosAbout_LiveDispatch_Nil_FallsBackToFrameConfig(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "active") // no SetLiveDispatch
	if resp.Active == nil {
		t.Fatal("active should be populated")
	}
	if resp.Active.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic (frame default)", resp.Active.Provider)
	}
	if resp.Active.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q, want claude-sonnet-4-6 (frame default)", resp.Active.Model)
	}
}

// TestCarlosAbout_LiveDispatch_EmptyValues_PreservesFrameConfig is
// the defensive case: the runtime closure exists but momentarily
// hands back empty strings (e.g. mid-swap or a defensive nil-guard
// path). The frame config defaults must still surface so the active
// section never reads as "provider: , model: " - which the model
// would then parrot back to the user.
func TestCarlosAbout_LiveDispatch_EmptyValues_PreservesFrameConfig(t *testing.T) {
	tool := newAboutTool()
	tool.SetLiveDispatch(func() (string, string) { return "", "" })

	resp := runAbout(t, tool, "active")
	if resp.Active == nil {
		t.Fatal("active should be populated")
	}
	if resp.Active.Provider != "anthropic" || resp.Active.Model != "claude-sonnet-4-6" {
		t.Errorf("empty live dispatch should fall back to frame config; got provider=%q model=%q",
			resp.Active.Provider, resp.Active.Model)
	}
}

// TestCarlosAbout_LiveDispatch_PartialOverride covers the per-field
// fallback documented on effectiveActiveIdentity: a closure returning
// a fresh provider but an empty model overrides only the provider
// slot, not blanking the model.
func TestCarlosAbout_LiveDispatch_PartialOverride(t *testing.T) {
	tool := newAboutTool()
	tool.SetLiveDispatch(func() (string, string) { return "openrouter", "" })

	resp := runAbout(t, tool, "active")
	if resp.Active == nil {
		t.Fatal("active should be populated")
	}
	if resp.Active.Provider != "openrouter" {
		t.Errorf("provider = %q, want openrouter (partial override)", resp.Active.Provider)
	}
	if resp.Active.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q, want claude-sonnet-4-6 (frame fallback)", resp.Active.Model)
	}
}

// newAboutToolGeorgeReport reconstructs the exact frame + provider
// shape from the v0.7.7 bug report so the assertion target ("the
// stale slug must not appear in the envelope") is concrete and the
// scenario is the one a real user hit, not a synthetic minimum.
func newAboutToolGeorgeReport() *CarlosAboutTool {
	return NewCarlosAboutTool(
		config.VaultConfig{Path: "/Volumes/nas/carlos-vault"},
		frame.Config{
			Default: "personal",
			Active:  "personal",
			List: []frame.Frame{
				{
					Name:     "personal",
					Provider: "openrouter",
					Model:    "google/gemini-3.5-flash",
					Mode:     "orchestrator",
				},
			},
		},
		"personal",
		map[string]ProviderSummary{
			"openrouter": {HasKey: true, DefaultModel: "google/gemini-3.5-flash"},
		},
		"George",
	)
}

// TestCarlosAbout_LiveDispatch_NoStaleModelStringInResponse is the
// load-bearing post-fix regression. It rebuilds George's frame config
// (openrouter + gemini), wires the live override to claude-fable-5,
// then asserts the SERIALIZED envelope contains zero occurrences of
// the stale model slug AND every model-flavored field reports the
// live model. A future refactor that adds a new section surfacing the
// model name without going through the override would fail here,
// catching the bug class - not just the two cells fixed today.
func TestCarlosAbout_LiveDispatch_NoStaleModelStringInResponse(t *testing.T) {
	tool := newAboutToolGeorgeReport()
	tool.SetLiveDispatch(func() (string, string) {
		return "openrouter", "anthropic/claude-fable-5"
	})

	in, _ := json.Marshal(carlosAboutInput{})
	raw, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	body := string(raw)

	// "google/gemini-3.5-flash" is the stale slug from the bug report.
	// It must NOT appear anywhere in the response after the override
	// is wired - any surface that surfaces it back to the model
	// reopens the bug.
	if strings.Contains(body, "google/gemini-3.5-flash") {
		t.Errorf("stale model slug leaked into envelope:\n%s", body)
	}
	// The fresh slug must appear at least twice: active.model AND
	// providers.openrouter.default_model.
	if got := strings.Count(body, "anthropic/claude-fable-5"); got < 2 {
		t.Errorf("live model should appear in BOTH active and providers sections; got %d occurrences in:\n%s", got, body)
	}
}

// TestCarlosAbout_LiveDispatch_SectionConsistency pins the
// across-section invariant: section="active" and section="providers"
// must agree on the effective model. The bug was that they
// disagreed and the model preferred the providers value.
func TestCarlosAbout_LiveDispatch_SectionConsistency(t *testing.T) {
	tool := newAboutToolGeorgeReport()
	tool.SetLiveDispatch(func() (string, string) {
		return "openrouter", "anthropic/claude-fable-5"
	})

	active := runAbout(t, tool, "active")
	provs := runAbout(t, tool, "providers")

	if active.Active.Model != provs.Providers["openrouter"].DefaultModel {
		t.Errorf("active.model = %q but providers[openrouter].default_model = %q (must match)",
			active.Active.Model, provs.Providers["openrouter"].DefaultModel)
	}
	if active.Active.Provider != "openrouter" {
		t.Errorf("active.provider = %q, want openrouter", active.Active.Provider)
	}
}

// TestCarlosAbout_LiveDispatch_ClosureReadsLatestVariableValue proves
// the runtime wiring pattern works end-to-end: SetLiveDispatch
// captures a closure that reads a Go variable by reference, so
// mutating the variable after wiring still surfaces the new value on
// the next call. This is exactly the shape runtime_tui.go uses
// (closure captures liveDispatch; swapModel mutates it).
func TestCarlosAbout_LiveDispatch_ClosureReadsLatestVariableValue(t *testing.T) {
	tool := newAboutToolGeorgeReport()

	// Capture a variable by reference, like the runtime does.
	type dispatch struct{ provider, model string }
	live := &dispatch{provider: "openrouter", model: "google/gemini-3.5-flash"}
	tool.SetLiveDispatch(func() (string, string) {
		return live.provider, live.model
	})

	// First call: still on the original model. Override matches frame
	// config so the response is unchanged from no-override behavior.
	first := runAbout(t, tool, "active")
	if first.Active.Model != "google/gemini-3.5-flash" {
		t.Errorf("pre-swap model = %q, want google/gemini-3.5-flash", first.Active.Model)
	}

	// Simulate /model swap by mutating the captured variable.
	live.model = "anthropic/claude-fable-5"

	// Second call: closure re-reads, surfaces the new model. If the
	// closure had captured by value the test would fail here.
	second := runAbout(t, tool, "active")
	if second.Active.Model != "anthropic/claude-fable-5" {
		t.Errorf("post-swap model = %q, want anthropic/claude-fable-5", second.Active.Model)
	}
}

// TestCarlosAbout_LiveDispatch_SequentialSwapsTrack pins that
// multiple back-to-back swaps each surface in the next response. The
// runtime calls swapModel multiple times across a session; each must
// land cleanly.
func TestCarlosAbout_LiveDispatch_SequentialSwapsTrack(t *testing.T) {
	tool := newAboutToolGeorgeReport()
	type dispatch struct{ provider, model string }
	live := &dispatch{provider: "openrouter", model: "google/gemini-3.5-flash"}
	tool.SetLiveDispatch(func() (string, string) {
		return live.provider, live.model
	})

	swaps := []string{
		"anthropic/claude-sonnet-4-6",
		"anthropic/claude-opus-4.8",
		"anthropic/claude-fable-5",
		"google/gemini-3.5-pro",
	}
	for _, target := range swaps {
		live.model = target
		resp := runAbout(t, tool, "active")
		if resp.Active.Model != target {
			t.Errorf("after swap to %q, active.model = %q", target, resp.Active.Model)
		}
	}
}

// TestCarlosAbout_LiveDispatch_DoesNotMutateInternalProviders is the
// isolation guarantee: a call that fires the providers-section
// override must NOT mutate the tool's internal providers map.
// Otherwise the override would leak across calls and across providers
// (a swap to provider A would persist into a subsequent reading of
// provider B's default_model after un-setting the override).
func TestCarlosAbout_LiveDispatch_DoesNotMutateInternalProviders(t *testing.T) {
	tool := newAboutToolGeorgeReport()
	tool.SetLiveDispatch(func() (string, string) {
		return "openrouter", "anthropic/claude-fable-5"
	})

	// Fire the override.
	_ = runAbout(t, tool, "providers")

	// Internal map must still hold the original DefaultModel.
	if got := tool.providers["openrouter"].DefaultModel; got != "google/gemini-3.5-flash" {
		t.Errorf("internal providers map mutated by override; got %q want %q",
			got, "google/gemini-3.5-flash")
	}

	// And: turning the override OFF should immediately surface the
	// stored config again, which it can only do if the internal map
	// stayed intact.
	tool.SetLiveDispatch(nil)
	resp := runAbout(t, tool, "providers")
	if got := resp.Providers["openrouter"].DefaultModel; got != "google/gemini-3.5-flash" {
		t.Errorf("after SetLiveDispatch(nil), default_model = %q, want stored fallback", got)
	}
}

// TestCarlosAbout_LiveDispatch_CanBeUnset covers the "test seam
// disabled" path: setting the closure to nil reverts to frame-config
// behavior on the very next call, with no lingering override state.
func TestCarlosAbout_LiveDispatch_CanBeUnset(t *testing.T) {
	tool := newAboutTool()
	tool.SetLiveDispatch(func() (string, string) {
		return "openrouter", "anthropic/claude-fable-5"
	})
	if got := runAbout(t, tool, "active").Active.Model; got != "anthropic/claude-fable-5" {
		t.Fatalf("setup: expected override; got %q", got)
	}

	tool.SetLiveDispatch(nil)
	if got := runAbout(t, tool, "active").Active.Model; got != "claude-sonnet-4-6" {
		t.Errorf("after unset, model = %q, want frame fallback claude-sonnet-4-6", got)
	}
}

// TestCarlosAbout_LiveDispatch_ConcurrentReadsSafe runs the tool
// under concurrent Execute calls so `go test -race` catches any read
// path that mutates shared state (e.g. accidentally writing to the
// tool's providers map instead of the response copy). The override
// closure is the only mutable shared state touched per call; this
// test surfaces a regression there immediately.
func TestCarlosAbout_LiveDispatch_ConcurrentReadsSafe(t *testing.T) {
	tool := newAboutToolGeorgeReport()
	tool.SetLiveDispatch(func() (string, string) {
		return "openrouter", "anthropic/claude-fable-5"
	})

	const goroutines = 16
	const iterations = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				in, _ := json.Marshal(carlosAboutInput{})
				raw, err := tool.Execute(context.Background(), in)
				if err != nil {
					t.Errorf("execute: %v", err)
					return
				}
				if !strings.Contains(string(raw), "anthropic/claude-fable-5") {
					t.Errorf("missing live model under concurrent read")
					return
				}
			}
		}()
	}
	wg.Wait()

	// Final correctness check after the storm.
	if tool.providers["openrouter"].DefaultModel != "google/gemini-3.5-flash" {
		t.Errorf("internal providers map mutated by concurrent overrides: %q",
			tool.providers["openrouter"].DefaultModel)
	}
}

// TestCarlosAbout_LiveDispatch_FilteredSectionsStillBehave guards the
// orthogonality: section filters (vault, user) are unrelated to the
// model identity, so the override path mustn't break them, and they
// must NOT carry model info if the override is wired.
func TestCarlosAbout_LiveDispatch_FilteredSectionsStillBehave(t *testing.T) {
	tool := newAboutToolGeorgeReport()
	tool.SetLiveDispatch(func() (string, string) {
		return "openrouter", "anthropic/claude-fable-5"
	})

	v := runAbout(t, tool, "vault")
	if v.Vault == nil || v.Vault.Path == "" {
		t.Errorf("vault section broken under override; got %+v", v.Vault)
	}
	if v.Active != nil {
		t.Errorf("section=vault should not surface active; got %+v", v.Active)
	}
	u := runAbout(t, tool, "user")
	if u.User != "George" {
		t.Errorf("user section broken under override; got %q", u.User)
	}
}

// TestCarlosAbout_LiveDispatch_FramesListActiveFrameAbsorbsOverride
// pins the active-frame entry in the frames list reflecting the
// runtime override; non-active frames keep their stored config. The
// asymmetry matters: the override is session-specific to the active
// frame, so a /model swap in the personal session does NOT
// reconfigure the work frame. But the active-frame entry must agree
// with resp.Active or the envelope is internally contradictory and
// the model picks the wrong slug.
func TestCarlosAbout_LiveDispatch_FramesListActiveFrameAbsorbsOverride(t *testing.T) {
	tool := newAboutTool() // active=personal; also has work frame
	tool.SetLiveDispatch(func() (string, string) {
		return "openrouter", "anthropic/claude-fable-5"
	})
	resp := runAbout(t, tool, "frames")
	for _, f := range resp.Frames {
		switch f.Name {
		case "personal":
			if f.Model != "anthropic/claude-fable-5" {
				t.Errorf("active frame entry must absorb live override; got model=%q", f.Model)
			}
			if f.Provider != "openrouter" {
				t.Errorf("active frame entry must absorb live provider; got %q", f.Provider)
			}
		case "work":
			if f.Provider != "anthropic" {
				t.Errorf("non-active frame must keep stored provider; got %q", f.Provider)
			}
		}
	}
}

func TestCarlosAbout_NoFramesWiredOmitsActiveAndFrames(t *testing.T) {
	tool := NewCarlosAboutTool(
		config.VaultConfig{Path: "/v"},
		frame.Config{},
		"",
		nil,
		"Boss",
	)
	resp := runAbout(t, tool, "")
	if resp.Active != nil {
		t.Errorf("no frames wired -> active should be nil; got %+v", resp.Active)
	}
	if len(resp.Frames) != 0 {
		t.Errorf("no frames wired -> frames should be empty; got %+v", resp.Frames)
	}
	if resp.User != "Boss" {
		t.Errorf("user should still surface; got %q", resp.User)
	}
}
