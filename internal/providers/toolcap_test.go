package providers

import (
	"fmt"
	"strings"
	"testing"
)

func TestMaxToolsFor(t *testing.T) {
	cases := []struct {
		provider, model string
		want            int
	}{
		{"openrouter", "x-ai/grok-4", 200},
		{"openrouter", "x-ai/grok-2-1212", 200},
		{"openrouter", "grok-beta", 200},
		{"xai", "GROK-4", 200}, // case-insensitive
		{"anthropic", "claude-opus-4-8", 0},
		{"openrouter", "z-ai/glm-5.2", 0},
		{"openai", "gpt-5", 0},
		{"", "", 0},
	}
	for _, c := range cases {
		if got := MaxToolsFor(c.provider, c.model); got != c.want {
			t.Errorf("MaxToolsFor(%q,%q)=%d want %d", c.provider, c.model, got, c.want)
		}
	}
}

func specs(names ...string) []ToolSpec {
	out := make([]ToolSpec, len(names))
	for i, n := range names {
		out[i] = ToolSpec{Name: n}
	}
	return out
}

func names(s []ToolSpec) []string {
	out := make([]string, len(s))
	for i, sp := range s {
		out[i] = sp.Name
	}
	return out
}

func TestCapTools_NoCapOrUnderCap(t *testing.T) {
	in := specs("read", "bash", "do__a")
	// max <= 0 => unchanged, same backing slice.
	got, dropped := CapTools(in, 0)
	if len(got) != 3 || dropped != nil {
		t.Errorf("max=0 should pass through: got %v dropped %v", names(got), dropped)
	}
	// len <= max => unchanged.
	got, dropped = CapTools(in, 3)
	if len(got) != 3 || dropped != nil {
		t.Errorf("len==max should pass through: got %v dropped %v", names(got), dropped)
	}
	got, dropped = CapTools(in, 10)
	if len(got) != 3 || dropped != nil {
		t.Errorf("len<max should pass through: got %v dropped %v", names(got), dropped)
	}
}

func TestCapTools_DropsMCPOverflowKeepsBuiltins(t *testing.T) {
	// 3 built-ins + 4 MCP, cap 5 => keep all built-ins + first 2 MCP, drop 2 MCP.
	in := specs("bash", "do__a", "read", "do__b", "grep", "do__c", "do__d")
	kept, dropped := CapTools(in, 5)
	if len(kept) != 5 {
		t.Fatalf("kept=%v want 5", names(kept))
	}
	// All built-ins survive.
	for _, b := range []string{"bash", "read", "grep"} {
		if !contains(names(kept), b) {
			t.Errorf("built-in %q dropped; kept=%v", b, names(kept))
		}
	}
	// MCP kept from the front: do__a, do__b; dropped: do__c, do__d.
	if !contains(names(kept), "do__a") || !contains(names(kept), "do__b") {
		t.Errorf("expected first two MCP kept; kept=%v", names(kept))
	}
	wantDropped := []string{"do__c", "do__d"}
	if strings.Join(dropped, ",") != strings.Join(wantDropped, ",") {
		t.Errorf("dropped=%v want %v", dropped, wantDropped)
	}
	// Original relative order preserved in kept.
	wantKept := []string{"bash", "do__a", "read", "do__b", "grep"}
	if strings.Join(names(kept), ",") != strings.Join(wantKept, ",") {
		t.Errorf("kept order=%v want %v", names(kept), wantKept)
	}
}

func TestCapTools_BuiltinsAloneExceedCap(t *testing.T) {
	// Degenerate: 4 built-ins, cap 2, plus an MCP tool. Keep first 2 built-ins,
	// drop the rest including all MCP.
	in := specs("a", "b", "c", "d", "srv__x")
	kept, dropped := CapTools(in, 2)
	if strings.Join(names(kept), ",") != "a,b" {
		t.Errorf("kept=%v want a,b", names(kept))
	}
	if !contains(dropped, "srv__x") || !contains(dropped, "c") || !contains(dropped, "d") {
		t.Errorf("dropped=%v should include c,d,srv__x", dropped)
	}
}

func TestCapTools_RealisticGrokOverflow(t *testing.T) {
	// 40 built-ins + 327 MCP = 367 tools, cap 200 (the reported bug).
	in := make([]ToolSpec, 0, 367)
	for i := 0; i < 40; i++ {
		in = append(in, ToolSpec{Name: fmt.Sprintf("builtin_%02d", i)})
	}
	for i := 0; i < 327; i++ {
		in = append(in, ToolSpec{Name: fmt.Sprintf("do__t%03d", i)})
	}
	kept, dropped := CapTools(in, 200)
	if len(kept) != 200 {
		t.Fatalf("kept=%d want 200", len(kept))
	}
	if len(dropped) != 167 {
		t.Errorf("dropped=%d want 167", len(dropped))
	}
	// Every built-in survives.
	for _, s := range kept[:0] {
		_ = s
	}
	builtinKept := 0
	for _, s := range kept {
		if !isMCPTool(s.Name) {
			builtinKept++
		}
	}
	if builtinKept != 40 {
		t.Errorf("built-ins kept=%d want all 40", builtinKept)
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
