package mcp

import (
	"os"
	"strings"
	"testing"
)

// TestForFrame covers the "empty Frames means all frames" semantics
// that mirrors how skills gate to frame subsets, plus the "no servers
// at all" quick-path that's the common case for users who haven't
// touched MCP.
func TestForFrame(t *testing.T) {
	cfg := Config{
		Servers: []ServerConfig{
			{Name: "always", Command: "/bin/echo"}, // no Frames - available everywhere
			{Name: "work-only", Command: "/bin/echo", Frames: []string{"work"}},
			{Name: "personal", Command: "/bin/echo", Frames: []string{"personal"}},
			{Name: "either", Command: "/bin/echo", Frames: []string{"work", "personal"}},
		},
	}
	cases := []struct {
		frame string
		want  []string
	}{
		{"work", []string{"always", "work-only", "either"}},
		{"personal", []string{"always", "personal", "either"}},
		{"unknown", []string{"always"}},
		{"", []string{"always", "work-only", "personal", "either"}}, // empty == legacy single-shelf
	}
	for _, tc := range cases {
		t.Run(tc.frame, func(t *testing.T) {
			got := cfg.ForFrame(tc.frame)
			if len(got) != len(tc.want) {
				t.Fatalf("ForFrame(%q): want %d entries, got %d", tc.frame, len(tc.want), len(got))
			}
			for i, name := range tc.want {
				if got[i].Name != name {
					t.Errorf("entry %d: want %q got %q", i, name, got[i].Name)
				}
			}
		})
	}
}

// TestForFrame_Empty pins the no-servers quick path: ForFrame returns
// nil (not an empty allocated slice) so the boot path doesn't pay an
// allocation when MCP isn't in use.
func TestForFrame_Empty(t *testing.T) {
	cfg := Config{}
	if got := cfg.ForFrame("anything"); got != nil {
		t.Errorf("ForFrame on empty Config: want nil, got %v", got)
	}
}

// TestExpandEnv covers the two value-add behaviors over a plain copy
// of os.Environ: overrides land after the base (so a user-specified
// PATH wins), and ${VAR} substitution resolves against the current
// process environment.
func TestExpandEnv(t *testing.T) {
	t.Setenv("CARLOS_MCP_TEST_BASE", "from-host")
	t.Setenv("CARLOS_MCP_TEST_PASSTHRU", "secret-value")

	got := expandEnv(map[string]string{
		"CARLOS_MCP_TEST_OVERRIDE": "literal",
		"CARLOS_MCP_TEST_REF":      "before-${CARLOS_MCP_TEST_PASSTHRU}-after",
	})

	// Base env must still be present (overrides append, they don't replace).
	if !contains(got, "CARLOS_MCP_TEST_BASE=from-host") {
		t.Errorf("expected base env CARLOS_MCP_TEST_BASE to pass through; got: %v", filterTest(got))
	}
	// Literal override.
	if !contains(got, "CARLOS_MCP_TEST_OVERRIDE=literal") {
		t.Errorf("literal override missing; got: %v", filterTest(got))
	}
	// ${VAR} expansion.
	if !contains(got, "CARLOS_MCP_TEST_REF=before-secret-value-after") {
		t.Errorf("env expansion failed; got: %v", filterTest(got))
	}

	// Overrides must come AFTER the base so exec.Cmd.Env's
	// "last write wins" semantics work for users who want to override
	// e.g. PATH.
	overrideIdx := indexPrefix(got, "CARLOS_MCP_TEST_OVERRIDE=")
	baseIdx := indexPrefix(got, "CARLOS_MCP_TEST_BASE=")
	if overrideIdx <= baseIdx {
		t.Errorf("overrides should come after base env; base=%d override=%d", baseIdx, overrideIdx)
	}
}

// TestExpandEnv_Empty: with no overrides, expandEnv returns the host
// env verbatim - the fast path that ConnectAll hits when the user's
// MCP block has no Env maps.
func TestExpandEnv_Empty(t *testing.T) {
	got := expandEnv(nil)
	if len(got) != len(os.Environ()) {
		t.Errorf("expected verbatim os.Environ; got %d entries vs %d", len(got), len(os.Environ()))
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

func indexPrefix(haystack []string, prefix string) int {
	for i, h := range haystack {
		if strings.HasPrefix(h, prefix) {
			return i
		}
	}
	return -1
}

func filterTest(env []string) []string {
	var out []string
	for _, e := range env {
		if strings.HasPrefix(e, "CARLOS_MCP_TEST_") {
			out = append(out, e)
		}
	}
	return out
}
