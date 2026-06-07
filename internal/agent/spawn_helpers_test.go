package agent

// Whitebox tests for spawn.go's helpers: composeInitialPrompt branches,
// buildChildRegistry, buildChildToolSpecs, newSpawnIDStrong.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// Minimal stub tool for the helper tests.
type stubHelperTool struct{ name string }

func (s stubHelperTool) Name() string                          { return s.name }
func (s stubHelperTool) Description() string                   { return "stub " + s.name }
func (s stubHelperTool) Schema() []byte                        { return []byte(`{"type":"object"}`) }
func (s stubHelperTool) Execute(context.Context, []byte) ([]byte, error) { return []byte("ok"), nil }

var _ tools.Tool = stubHelperTool{}

func TestComposeInitialPrompt_AllFields(t *testing.T) {
	c := SpawnContract{
		Objective:       "find references to X",
		OutputFormat:    "{\"refs\":[...]}",
		SuccessCriteria: "<= 2 turns",
		MaxTurns:        5,
		MaxTokens:       1000,
		MaxWallClock:    10 * time.Second,
	}
	got := composeInitialPrompt(c)
	for _, want := range []string{
		"# Objective", "find references to X",
		"# Output format", "{\"refs\":[...]}",
		"# Success criteria", "<= 2 turns",
		"# Boundaries", "max turns: 5", "max tokens: 1000", "max wall clock: 10s",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in prompt; got %q", want, got)
		}
	}
}

func TestComposeInitialPrompt_OnlyObjective(t *testing.T) {
	got := composeInitialPrompt(SpawnContract{Objective: "do x"})
	if !strings.Contains(got, "# Objective") {
		t.Errorf("expected Objective header")
	}
	// Boundaries section omitted when all caps are zero.
	if strings.Contains(got, "# Boundaries") {
		t.Errorf("Boundaries section should not appear for zero caps; got %q", got)
	}
}

func TestComposeInitialPrompt_EmptyContractStillProducesOutput(t *testing.T) {
	got := composeInitialPrompt(SpawnContract{})
	if got == "" {
		t.Error("empty contract should still produce a default prompt")
	}
	if !strings.Contains(got, "no objective specified") {
		t.Errorf("expected fallback marker; got %q", got)
	}
}

func TestBuildChildRegistry_EmptyAllowlistYieldsEmptyRegistry(t *testing.T) {
	base := tools.NewRegistry()
	base.Register(stubHelperTool{name: "read"})
	got := buildChildRegistry(base, nil)
	if got == nil {
		t.Fatal("registry should be non-nil")
	}
	if _, ok := got.Get("read"); ok {
		t.Error("empty allowlist should yield empty registry")
	}
}

func TestBuildChildRegistry_NilBaseYieldsEmpty(t *testing.T) {
	got := buildChildRegistry(nil, []string{"read"})
	if _, ok := got.Get("read"); ok {
		t.Error("nil base should yield empty registry")
	}
}

func TestBuildChildRegistry_FiltersToAllowlist(t *testing.T) {
	base := tools.NewRegistry()
	base.Register(stubHelperTool{name: "read"})
	base.Register(stubHelperTool{name: "write"})
	base.Register(stubHelperTool{name: "bash"})

	got := buildChildRegistry(base, []string{"read", "write"})
	if _, ok := got.Get("read"); !ok {
		t.Error("expected read in child registry")
	}
	if _, ok := got.Get("write"); !ok {
		t.Error("expected write in child registry")
	}
	if _, ok := got.Get("bash"); ok {
		t.Error("bash should not be in child registry")
	}
}

func TestBuildChildRegistry_UnknownToolSilentlyDropped(t *testing.T) {
	base := tools.NewRegistry()
	base.Register(stubHelperTool{name: "read"})
	got := buildChildRegistry(base, []string{"read", "nonexistent"})
	if _, ok := got.Get("read"); !ok {
		t.Error("expected read in registry")
	}
	if _, ok := got.Get("nonexistent"); ok {
		t.Error("nonexistent should be dropped")
	}
}

func TestBuildChildToolSpecs_EmptyAllowlistReturnsNil(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(stubHelperTool{name: "x"})
	if got := buildChildToolSpecs(reg, nil); got != nil {
		t.Errorf("empty allowlist should yield nil specs, got %v", got)
	}
}

func TestBuildChildToolSpecs_NilRegYieldsNil(t *testing.T) {
	if got := buildChildToolSpecs(nil, []string{"x"}); got != nil {
		t.Errorf("nil reg should yield nil, got %v", got)
	}
}

func TestBuildChildToolSpecs_SkipsUnknownButReturnsPresent(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(stubHelperTool{name: "read"})
	reg.Register(stubHelperTool{name: "write"})

	got := buildChildToolSpecs(reg, []string{"read", "ghost", "write"})
	if len(got) != 2 {
		t.Fatalf("got %d specs, want 2", len(got))
	}
	if got[0].Name != "read" || got[1].Name != "write" {
		t.Errorf("spec ordering off: %+v", got)
	}
}

func TestNewSpawnIDStrong_IsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		id := newSpawnIDStrong()
		if id == "" {
			t.Fatal("ID was empty")
		}
		if !strings.HasPrefix(id, "a-") {
			t.Errorf("ID should start with a-, got %q", id)
		}
		if seen[id] {
			t.Errorf("duplicate ID %q at iter %d", id, i)
		}
		seen[id] = true
		// Force tick to avoid same-nanosecond collisions on fast machines.
		time.Sleep(time.Microsecond)
	}
}

// Hit extractFinalText branches.
func TestExtractFinalText_EmptyContent(t *testing.T) {
	if got := extractFinalText(providers.Message{}); got != "" {
		t.Errorf("empty content → %q want empty", got)
	}
}

func TestExtractFinalText_JoinsMultipleBlocks(t *testing.T) {
	m := providers.Message{Content: []providers.Block{
		{Kind: "text", Text: "alpha"},
		{Kind: "tool_use", Text: "noise"},
		{Kind: "text", Text: "beta"},
		{Kind: "", Text: "gamma"}, // empty kind treated as text
	}}
	got := extractFinalText(m)
	if !strings.Contains(got, "alpha") || !strings.Contains(got, "beta") || !strings.Contains(got, "gamma") {
		t.Errorf("missing text blocks in %q", got)
	}
	if strings.Contains(got, "noise") {
		t.Error("tool_use block should be skipped")
	}
}

// mustMarshal is panic-on-failure; ensure happy path returns bytes.
func TestMustMarshal_RoundTrips(t *testing.T) {
	type X struct{ S string }
	got := mustMarshal(X{S: "hi"})
	var x X
	if err := json.Unmarshal(got, &x); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if x.S != "hi" {
		t.Errorf("round-trip got %q", x.S)
	}
}
