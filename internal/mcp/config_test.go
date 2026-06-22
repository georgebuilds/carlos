package mcp

import (
	"net/http"
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

// TestTransportKind pins the normalization: empty/whitespace means stdio
// (so pre-transport configs round-trip), and the value is case-folded so a
// hand-edited "HTTP" connects the same as "http".
func TestTransportKind(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", TransportStdio},
		{"   ", TransportStdio},
		{"stdio", TransportStdio},
		{"http", TransportHTTP},
		{"HTTP", TransportHTTP},
		{" Sse ", TransportSSE},
	}
	for _, tc := range cases {
		if got := (ServerConfig{Transport: tc.in}).TransportKind(); got != tc.want {
			t.Errorf("TransportKind(%q): want %q got %q", tc.in, tc.want, got)
		}
	}
}

// TestValidate covers the per-transport coherence rules: name always
// required, stdio needs a command, http/sse need a url, and an unknown
// transport is rejected up front rather than as a murky connect failure.
func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ServerConfig
		wantErr bool
	}{
		{"stdio ok", ServerConfig{Name: "a", Command: "/bin/echo"}, false},
		{"stdio explicit ok", ServerConfig{Name: "a", Transport: "stdio", Command: "/bin/echo"}, false},
		{"stdio no command", ServerConfig{Name: "a"}, true},
		{"http ok", ServerConfig{Name: "a", Transport: "http", URL: "https://x/mcp"}, false},
		{"sse ok", ServerConfig{Name: "a", Transport: "sse", URL: "https://x/sse"}, false},
		{"http no url", ServerConfig{Name: "a", Transport: "http"}, true},
		{"no name", ServerConfig{Command: "/bin/echo"}, true},
		{"unknown transport", ServerConfig{Name: "a", Transport: "carrierpigeon", URL: "x"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("want nil, got %v", err)
			}
		})
	}
}

// TestHTTPClientWithHeaders covers both branches: no headers returns nil so
// the SDK uses http.DefaultClient, and a header map produces a client whose
// RoundTripper injects the headers with ${VAR} expanded against the env.
func TestHTTPClientWithHeaders(t *testing.T) {
	if got := httpClientWithHeaders(nil); got != nil {
		t.Errorf("no headers: want nil client, got %v", got)
	}

	t.Setenv("CARLOS_MCP_TEST_TOKEN", "sekret")
	got := httpClientWithHeaders(map[string]string{
		"Authorization": "Bearer ${CARLOS_MCP_TEST_TOKEN}",
		"X-Static":      "fixed",
	})
	if got == nil {
		t.Fatal("with headers: want non-nil client")
	}
	rt, ok := got.Transport.(*headerRoundTripper)
	if !ok {
		t.Fatalf("want *headerRoundTripper, got %T", got.Transport)
	}
	if rt.headers["Authorization"] != "Bearer sekret" {
		t.Errorf("env expansion failed: %q", rt.headers["Authorization"])
	}
	if rt.headers["X-Static"] != "fixed" {
		t.Errorf("static header mangled: %q", rt.headers["X-Static"])
	}

	// The RoundTripper must set the headers on the outbound request and
	// must not mutate the caller's *http.Request (it clones first).
	var seen http.Header
	rt.base = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r.Header.Clone()
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid/mcp", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if seen.Get("Authorization") != "Bearer sekret" {
		t.Errorf("Authorization not injected: %q", seen.Get("Authorization"))
	}
	if req.Header.Get("Authorization") != "" {
		t.Errorf("original request was mutated: %q", req.Header.Get("Authorization"))
	}
}

// roundTripFunc adapts a func to http.RoundTripper for the header test.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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
